package runtime_test

import (
	"context"
	"errors"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestProjectHealthReportsNilAndClosedProject(t *testing.T) {
	t.Run("nil project", func(t *testing.T) {
		var project *projectruntime.Project
		report, err := project.Health(context.Background())
		if err == nil {
			t.Fatalf("Health() unexpectedly succeeded: %+v", report)
		}
		assertDomainCode(t, err, projectruntime.CodeHealthCheck)
		if len(report.Checks) != 0 {
			t.Fatalf("health checks = %+v, want none", report.Checks)
		}
		if report.ExpectedSchemaVersion != migrations.CurrentVersion() {
			t.Fatalf("expected schema version = %d, want %d", report.ExpectedSchemaVersion, migrations.CurrentVersion())
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) {
			t.Fatalf("error = %v, want *domain.Error", err)
		}
		if len(domainErr.Details) != 1 {
			t.Fatalf("details = %+v, want one detail", domainErr.Details)
		}
		if domainErr.Details[0].Field != "ping" || domainErr.Details[0].Message != "project is not open" {
			t.Fatalf("detail = %+v, want ping/project is not open", domainErr.Details[0])
		}
	})

	t.Run("closed project", func(t *testing.T) {
		repository, dataRoot := initializeProject(t)
		project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
			StartingPath: repository,
			DataRoot:     dataRoot,
			Clock:        clock.NewFakeClock(testTime),
		})
		if err != nil {
			t.Fatalf("OpenProject() error = %v", err)
		}
		if err := project.Close(context.Background()); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		report, err := project.Health(context.Background())
		if err == nil {
			t.Fatalf("Health() unexpectedly succeeded: %+v", report)
		}
		assertDomainCode(t, err, projectruntime.CodeHealthCheck)
		if len(report.Checks) != 0 {
			t.Fatalf("health checks = %+v, want none", report.Checks)
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) {
			t.Fatalf("error = %v, want *domain.Error", err)
		}
		if len(domainErr.Details) != 1 {
			t.Fatalf("details = %+v, want one detail", domainErr.Details)
		}
		if domainErr.Details[0].Field != "ping" || domainErr.Details[0].Message != "project is closed" {
			t.Fatalf("detail = %+v, want ping/project is closed", domainErr.Details[0])
		}
	})
}

func TestProjectHealthReportsInvariantFailureWithSafeDetails(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM schema_migrations")
		return err
	}); err != nil {
		t.Fatalf("mutate schema version: %v", err)
	}

	report, err := project.Health(context.Background())
	if err == nil {
		t.Fatalf("Health() unexpectedly succeeded: %+v", report)
	}
	assertDomainCode(t, err, projectruntime.CodeHealthCheck)
	if report.Healthy() {
		t.Fatalf("HealthReport unexpectedly healthy: %+v", report)
	}

	wantChecks := []string{"ping", "journal_mode_wal", "foreign_keys_enabled", "schema_version", "migration_history", "fts5", "quick_check", "foreign_key_check", "one_active_attempt_per_issue"}
	if len(report.Checks) != len(wantChecks) {
		t.Fatalf("health checks = %+v, want %d entries", report.Checks, len(wantChecks))
	}
	for index, want := range wantChecks {
		if report.Checks[index].Name != want {
			t.Fatalf("health check %d = %q, want %q", index, report.Checks[index].Name, want)
		}
	}

	var failedCheck *projectruntime.HealthCheck
	for index := range report.Checks {
		if report.Checks[index].Name == "schema_version" {
			failedCheck = &report.Checks[index]
			break
		}
	}
	if failedCheck == nil {
		t.Fatalf("report missing schema_version check: %+v", report.Checks)
	}
	if failedCheck.Healthy {
		t.Fatalf("schema_version unexpectedly healthy: %+v", *failedCheck)
	}
	if failedCheck.Message != "failed" {
		t.Fatalf("failed check message = %q, want %q", failedCheck.Message, "failed")
	}

	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	var foundDetail bool
	for _, detail := range domainErr.Details {
		if detail.Field == "schema_version" {
			foundDetail = true
			if detail.Message != "verification failed" {
				t.Fatalf("detail message = %q, want %q", detail.Message, "verification failed")
			}
			break
		}
	}
	if !foundDetail {
		t.Fatalf("missing detail for schema_version: %+v", domainErr.Details)
	}
}

func TestProjectHealthReportsCancellationWhilePreservingCollectedState(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := project.Health(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Health() error = %v, want context cancellation", err)
	}
	if len(report.Checks) != 0 {
		t.Fatalf("health checks = %+v, want none because cancellation occurred before checks were collected", report.Checks)
	}
	if report.ExpectedSchemaVersion != migrations.CurrentVersion() {
		t.Fatalf("expected schema version = %d, want %d", report.ExpectedSchemaVersion, migrations.CurrentVersion())
	}
}

func TestHealthReportHealthyReportsMixedCheckStates(t *testing.T) {
	healthyReport := projectruntime.HealthReport{Checks: []projectruntime.HealthCheck{{Name: "ping", Healthy: true, Message: "ok"}}}
	if !healthyReport.Healthy() {
		t.Fatalf("Healthy() = false, want true")
	}

	mixedReport := projectruntime.HealthReport{Checks: []projectruntime.HealthCheck{{Name: "ping", Healthy: true, Message: "ok"}, {Name: "one_active_attempt_per_issue", Healthy: false, Message: "failed"}}}
	if mixedReport.Healthy() {
		t.Fatalf("Healthy() = true, want false")
	}
}
