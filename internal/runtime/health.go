package runtime

import (
	"context"
	"errors"
	"fmt"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
)

const (
	checkPing             = "ping"
	checkJournalMode      = "journal_mode_wal"
	checkForeignKeys      = "foreign_keys_enabled"
	checkSchemaVersion    = "schema_version"
	checkMigrationHistory = "migration_history"
	checkFTS5             = "fts5"
	checkQuickCheck       = "quick_check"
	checkForeignKeyCheck  = "foreign_key_check"
	checkOneActiveAttempt = "one_active_attempt_per_issue"
)

// HealthCheck is one deterministic, named Phase 1 verification result.
type HealthCheck struct {
	Name    string
	Healthy bool
	Message string
}

// HealthReport summarizes the current database and embedded schema versions.
// Checks always appear in the documented fixed order.
type HealthReport struct {
	ExpectedSchemaVersion int
	CurrentSchemaVersion  int
	Checks                []HealthCheck
}

// Healthy reports whether every named check passed.
func (report HealthReport) Healthy() bool {
	for _, check := range report.Checks {
		if !check.Healthy {
			return false
		}
	}
	return true
}

// Health runs only the lightweight Phase 1 checks. It does not perform deep
// integrity, permissions, disk, WAL-size, expiry, backup, or repair work.
func (project *Project) Health(ctx context.Context) (HealthReport, error) {
	report := HealthReport{ExpectedSchemaVersion: migrations.CurrentVersion()}
	if project == nil {
		return report, domain.NewError(CodeHealthCheck, "project health check failed", false,
			domain.Detail{Field: checkPing, Code: "CHECK_FAILED", Message: "project is not open"})
	}

	project.mu.RLock()
	defer project.mu.RUnlock()
	if project.closed || project.Database == nil {
		return report, domain.NewError(CodeHealthCheck, "project health check failed", false,
			domain.Detail{Field: checkPing, Code: "CHECK_FAILED", Message: "project is closed"})
	}

	checks := []struct {
		name string
		run  func(context.Context, sqlite.Queryer) (string, error)
	}{
		{checkPing, checkPingDatabase},
		{checkJournalMode, checkWAL},
		{checkForeignKeys, checkForeignKeysEnabled},
		{checkSchemaVersion, func(ctx context.Context, query sqlite.Queryer) (string, error) {
			var current int
			if err := query.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
				return "", err
			}
			report.CurrentSchemaVersion = current
			if current != report.ExpectedSchemaVersion {
				return "", fmt.Errorf("schema version is %d; expected %d", current, report.ExpectedSchemaVersion)
			}
			return fmt.Sprintf("current=%d expected=%d", current, report.ExpectedSchemaVersion), nil
		}},
		{checkMigrationHistory, func(ctx context.Context, query sqlite.Queryer) (string, error) {
			version, err := migrations.VerifyHistory(ctx, query)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("verified through version %d", version), nil
		}},
		{checkFTS5, checkFTS5Available},
		{checkQuickCheck, checkQuickIntegrity},
		{checkForeignKeyCheck, checkForeignKeyIntegrity},
		{checkOneActiveAttempt, checkActiveAttemptInvariant},
	}

	details := make([]domain.Detail, 0)
	err := project.Database.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		for _, item := range checks {
			message, checkErr := item.run(ctx, query)
			if checkErr == nil {
				report.Checks = append(report.Checks, HealthCheck{Name: item.name, Healthy: true, Message: message})
				continue
			}
			report.Checks = append(report.Checks, HealthCheck{Name: item.name, Healthy: false, Message: "failed"})
			details = append(details, domain.Detail{Field: item.name, Code: "CHECK_FAILED", Message: safeCheckMessage(checkErr)})
			if errors.Is(checkErr, context.Canceled) || errors.Is(checkErr, context.DeadlineExceeded) {
				return checkErr
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return report, err
		}
		return report, domain.WrapError(err, CodeHealthCheck, "project health check failed", false,
			domain.Detail{Field: checkPing, Code: "CHECK_FAILED", Message: "database query failed"})
	}
	if len(details) != 0 {
		return report, domain.NewError(CodeHealthCheck, "project health check failed", false, details...)
	}
	return report, nil
}

func checkPingDatabase(ctx context.Context, query sqlite.Queryer) (string, error) {
	var one int
	if err := query.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return "", err
	}
	if one != 1 {
		return "", errors.New("unexpected ping result")
	}
	return "ok", nil
}

func checkWAL(ctx context.Context, query sqlite.Queryer) (string, error) {
	var mode string
	if err := query.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return "", err
	}
	if mode != "wal" {
		return "", fmt.Errorf("journal mode is %q", mode)
	}
	return "wal", nil
}

func checkForeignKeysEnabled(ctx context.Context, query sqlite.Queryer) (string, error) {
	var enabled int
	if err := query.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return "", err
	}
	if enabled != 1 {
		return "", errors.New("foreign keys are disabled")
	}
	return "enabled", nil
}

func checkFTS5Available(ctx context.Context, query sqlite.Queryer) (string, error) {
	var enabled, tableCount int
	if err := query.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&enabled); err != nil {
		return "", err
	}
	if err := query.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'search_index'").Scan(&tableCount); err != nil {
		return "", err
	}
	if enabled != 1 || tableCount != 1 {
		return "", errors.New("FTS5 or search index is unavailable")
	}
	return "available", nil
}

func checkQuickIntegrity(ctx context.Context, query sqlite.Queryer) (string, error) {
	rows, err := query.QueryContext(ctx, "PRAGMA quick_check")
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
			return "", errors.New("quick check reported a problem")
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count != 1 {
		return "", errors.New("quick check returned an unexpected result")
	}
	return "ok", nil
}

func checkForeignKeyIntegrity(ctx context.Context, query sqlite.Queryer) (string, error) {
	rows, err := query.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		return "", errors.New("foreign key violations found")
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "ok", nil
}

func checkActiveAttemptInvariant(ctx context.Context, query sqlite.Queryer) (string, error) {
	rows, err := query.QueryContext(ctx, `SELECT issue_id
		FROM work_attempts
		WHERE status = 'active'
		GROUP BY issue_id
		HAVING count(*) > 1
		ORDER BY issue_id
		LIMIT 1`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		return "", errors.New("multiple active attempts found for one issue")
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "ok", nil
}

func safeCheckMessage(err error) string {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		return domainErr.Message
	}
	return "verification failed"
}
