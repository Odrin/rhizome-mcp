package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
)

const (
	checkDatabaseWritable      = "database_writable"
	checkDataDirectoryWritable = "data_directory_writable"
	checkFreeDiskSpace         = "free_disk_space"
	checkWALSize               = "wal_size"
	checkExpiredActiveAttempts = "expired_active_attempts"
	checkIntegrity             = "integrity_check"
)

// DoctorCheck is one deterministic, named doctor verification result.
type DoctorCheck struct {
	Name    string
	Healthy bool
	Message string
}

// DoctorReport summarizes the current database and embedded schema versions.
// Checks always appear in the documented fixed order.
type DoctorReport struct {
	Full                  bool
	ExpectedSchemaVersion int
	CurrentSchemaVersion  int
	Checks                []DoctorCheck
}

// Healthy reports whether every named check passed.
func (report DoctorReport) Healthy() bool {
	for _, check := range report.Checks {
		if !check.Healthy {
			return false
		}
	}
	return true
}

// Doctor runs the lightweight Phase 1 checks plus operational read-only checks.
func (project *Project) Doctor(ctx context.Context, full bool) (DoctorReport, error) {
	report := DoctorReport{Full: full, ExpectedSchemaVersion: migrations.CurrentVersion()}
	if project == nil {
		return report, domain.NewError(CodeHealthCheck, "project doctor check failed", false,
			domain.Detail{Field: checkPing, Code: "CHECK_FAILED", Message: "project is not open"})
	}

	project.mu.RLock()
	defer project.mu.RUnlock()
	if project.closed || project.Database == nil {
		return report, domain.NewError(CodeHealthCheck, "project doctor check failed", false,
			domain.Detail{Field: checkPing, Code: "CHECK_FAILED", Message: "project is closed"})
	}

	healthChecks, currentVersion, details, err := project.collectHealthChecks(ctx, report.ExpectedSchemaVersion)
	report.Checks = append(report.Checks, makeDoctorChecks(healthChecks)...)
	report.CurrentSchemaVersion = currentVersion
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return report, err
		}
		return report, domain.WrapError(err, CodeHealthCheck, "project doctor check failed", false,
			domain.Detail{Field: checkPing, Code: "CHECK_FAILED", Message: "database query failed"})
	}

	if err := project.collectOperationalChecks(ctx, &report); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return report, err
		}
		return report, domain.WrapError(err, CodeHealthCheck, "project doctor check failed", false,
			domain.Detail{Field: checkPing, Code: "CHECK_FAILED", Message: "database query failed"})
	}

	if len(details) != 0 {
		return report, domain.NewError(CodeHealthCheck, "project doctor check failed", false, makeDoctorDetails(report.Checks)...)
	}
	if !report.Healthy() {
		return report, domain.NewError(CodeHealthCheck, "project doctor check failed", false, makeDoctorDetails(report.Checks)...)
	}
	return report, nil
}

func makeDoctorChecks(checks []HealthCheck) []DoctorCheck {
	result := make([]DoctorCheck, 0, len(checks))
	for _, check := range checks {
		result = append(result, DoctorCheck{Name: check.Name, Healthy: check.Healthy, Message: check.Message})
	}
	return result
}

func makeDoctorDetails(checks []DoctorCheck) []domain.Detail {
	details := make([]domain.Detail, 0, len(checks))
	for _, check := range checks {
		if check.Healthy {
			continue
		}
		details = append(details, domain.Detail{Field: check.Name, Code: "CHECK_FAILED", Message: "failed"})
	}
	return details
}

func (project *Project) collectOperationalChecks(ctx context.Context, report *DoctorReport) error {
	checks := []struct {
		name string
		run  func(context.Context, sqlite.Queryer) (string, error)
	}{
		{checkDatabaseWritable, func(ctx context.Context, query sqlite.Queryer) (string, error) {
			_ = query
			return checkDatabaseWritableFile(project.DatabasePath)
		}},
		{checkDataDirectoryWritable, func(ctx context.Context, query sqlite.Queryer) (string, error) {
			_ = query
			return checkDataDirectoryWritableFile(project.DatabasePath)
		}},
		{checkFreeDiskSpace, func(ctx context.Context, query sqlite.Queryer) (string, error) {
			_ = query
			return checkFreeDiskSpaceOnPath(project.DatabasePath)
		}},
		{checkWALSize, func(ctx context.Context, query sqlite.Queryer) (string, error) {
			_ = query
			return checkWALSizeOnPath(project.DatabasePath)
		}},
		{checkExpiredActiveAttempts, func(ctx context.Context, query sqlite.Queryer) (string, error) {
			return checkExpiredActiveAttemptsQuery(ctx, query, project.clock)
		}},
	}
	if report.Full {
		checks = append(checks, struct {
			name string
			run  func(context.Context, sqlite.Queryer) (string, error)
		}{checkIntegrity, func(ctx context.Context, query sqlite.Queryer) (string, error) {
			return checkIntegrityQuery(ctx, query)
		}})
	}

	return project.Database.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		for _, item := range checks {
			message, checkErr := item.run(ctx, query)
			if checkErr == nil {
				report.Checks = append(report.Checks, DoctorCheck{Name: item.name, Healthy: true, Message: message})
				continue
			}
			report.Checks = append(report.Checks, DoctorCheck{Name: item.name, Healthy: false, Message: "failed"})
			if errors.Is(checkErr, context.Canceled) || errors.Is(checkErr, context.DeadlineExceeded) {
				return checkErr
			}
		}
		return nil
	})
}

func checkDatabaseWritableFile(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return "ok", nil
}

func checkDataDirectoryWritableFile(path string) (string, error) {
	parent := filepath.Dir(path)
	tmp, err := os.CreateTemp(parent, ".rhizome-doctor-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		return "", errors.Join(err, os.Remove(name))
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return "ok", nil
}

func checkFreeDiskSpaceOnPath(path string) (string, error) {
	databaseSize, err := statFileSize(path)
	if err != nil {
		return "", err
	}
	walPath := path + "-wal"
	walSize, err := statFileSize(walPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			walSize = 0
		} else {
			return "", err
		}
	}
	required := uint64(2 * (databaseSize + walSize))
	const minRequired = 64 << 20
	if required < minRequired {
		required = minRequired
	}
	available, err := freeDiskSpace(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	if available < required {
		return "", fmt.Errorf("available=%d required=%d", available, required)
	}
	return fmt.Sprintf("available=%d required=%d", available, required), nil
}

func checkWALSizeOnPath(path string) (string, error) {
	walPath := path + "-wal"
	size, err := statFileSize(walPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			size = 0
		} else {
			return "", err
		}
	}
	if size > 100<<20 {
		return "", fmt.Errorf("wal size is %d bytes", size)
	}
	return fmt.Sprintf("size=%d", size), nil
}

func checkExpiredActiveAttemptsQuery(ctx context.Context, query sqlite.Queryer, clock clock.Clock) (string, error) {
	if clock == nil {
		return "", errors.New("project clock is unavailable")
	}
	now := clock.Now().UTC().Format(time.RFC3339Nano)
	var count int
	if err := query.QueryRowContext(ctx, `SELECT count(*) FROM work_attempts WHERE status = 'active' AND lease_expires_at <= ?`, now).Scan(&count); err != nil {
		return "", err
	}
	if count != 0 {
		return "", fmt.Errorf("expired active attempts=%d", count)
	}
	return fmt.Sprintf("count=%d", count), nil
}

func checkIntegrityQuery(ctx context.Context, query sqlite.Queryer) (string, error) {
	rows, err := query.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return "", err
		}
		count++
		if result != "ok" {
			return "", errors.New("integrity check failed")
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count != 1 {
		return "", errors.New("integrity check returned an unexpected result")
	}
	return "ok", nil
}

func statFileSize(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.Size() < 0 {
		return 0, fmt.Errorf("invalid file size")
	}
	return uint64(info.Size()), nil
}
