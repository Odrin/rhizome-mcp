package runtime_test

import (
	"context"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestProjectDoctorHealthyNormalAndFull(t *testing.T) {
	t.Setenv("HTTP_ADDRESS", "")

	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	report, err := project.Doctor(context.Background(), false)
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if !report.Healthy() {
		t.Fatalf("Doctor() unhealthy report = %+v", report)
	}
	wantChecks := []string{"ping", "journal_mode_wal", "foreign_keys_enabled", "schema_version", "migration_history", "fts5", "quick_check", "foreign_key_check", "one_active_attempt_per_issue", "database_writable", "data_directory_writable", "free_disk_space", "wal_size", "expired_active_attempts", "http_address"}
	if len(report.Checks) != len(wantChecks) {
		t.Fatalf("doctor checks = %+v", report.Checks)
	}
	for index, want := range wantChecks {
		if report.Checks[index].Name != want {
			t.Fatalf("doctor check %d = %q, want %q", index, report.Checks[index].Name, want)
		}
	}

	fullReport, err := project.Doctor(context.Background(), true)
	if err != nil {
		t.Fatalf("Doctor(full) error = %v", err)
	}
	if !fullReport.Healthy() {
		t.Fatalf("Doctor(full) unhealthy report = %+v", fullReport)
	}
	if fullReport.Checks[len(fullReport.Checks)-1].Name != "integrity_check" {
		t.Fatalf("full doctor last check = %q", fullReport.Checks[len(fullReport.Checks)-1].Name)
	}
}

func TestProjectDoctorReportsInvalidHTTPAddressConfiguration(t *testing.T) {
	t.Setenv("HTTP_ADDRESS", "localhost:0")

	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	report, err := project.Doctor(context.Background(), false)
	if err == nil {
		t.Fatalf("Doctor() unexpectedly succeeded: %+v", report)
	}
	assertDomainCode(t, err, projectruntime.CodeHealthCheck)
	if report.Healthy() {
		t.Fatalf("Doctor() healthy report = %+v", report)
	}
	var found bool
	for _, check := range report.Checks {
		if check.Name == "http_address" {
			found = true
			if check.Healthy {
				t.Fatalf("http_address unexpectedly healthy: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("doctor report missing http_address check: %+v", report.Checks)
	}
}

func TestProjectDoctorReportsExpiredActiveAttemptsWithoutMutatingState(t *testing.T) {
	t.Setenv("HTTP_ADDRESS", "")

	repository, dataRoot := initializeProject(t)
	fakeClock := clock.NewFakeClock(testTime)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	issueID := "01F2H8V5M9Q1J7K3N6P4R0T2WX"
	attemptID := "01G2H8V5M9Q1J7K3N6P4R0T2WX"
	leaseExpiresAt := fakeClock.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	now := fakeClock.Now().UTC().Format(time.RFC3339Nano)
	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'doctor state', 'ready', 'medium', 1, ?, ?)`, issueID, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`, attemptID, issueID, leaseExpiresAt, now, now)
		return err
	}); err != nil {
		t.Fatalf("insert test attempt: %v", err)
	}

	report, err := project.Doctor(context.Background(), false)
	if err == nil {
		t.Fatalf("Doctor() unexpectedly succeeded: %+v", report)
	}
	assertDomainCode(t, err, projectruntime.CodeHealthCheck)
	if report.Healthy() {
		t.Fatalf("Doctor() healthy report = %+v", report)
	}
	var status string
	var eventCount int
	if err := project.Database.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT status FROM work_attempts WHERE id = ?", attemptID).Scan(&status); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&eventCount)
	}); err != nil {
		t.Fatalf("read attempt state: %v", err)
	}
	if status != "active" {
		t.Fatalf("attempt status = %q, want active", status)
	}
	if eventCount != 0 {
		t.Fatalf("issue event rows = %d, want 0", eventCount)
	}
	var found bool
	for _, check := range report.Checks {
		if check.Name == "expired_active_attempts" {
			found = true
			if check.Healthy {
				t.Fatalf("expired_active_attempts unexpectedly healthy: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("doctor report missing expired_active_attempts check: %+v", report.Checks)
	}
}
