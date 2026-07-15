package runtime_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/projectconfig"
	projectruntime "rhizome-mcp/internal/runtime"
)

const (
	projectID      = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	otherProjectID = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
)

var testTime = time.Date(2026, 7, 13, 12, 30, 45, 123456789, time.FixedZone("test", 3*60*60))

type fixedGenerator string

func (generator fixedGenerator) New() (string, error) { return string(generator), nil }

func TestOpenProjectEndToEndFromNestedPathAndReopen(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	nested := filepath.Join(repository, "src", "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeClock := clock.NewFakeClock(testTime)

	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: nested,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	if project.Root != repository || project.ProjectID != projectID {
		t.Fatalf("opened identity = root %q ID %q", project.Root, project.ProjectID)
	}
	if project.SchemaVersion != migrations.CurrentVersion() {
		t.Fatalf("schema version = %d, want %d", project.SchemaVersion, migrations.CurrentVersion())
	}
	if isWithin(project.DatabasePath, repository) {
		t.Fatalf("database path %q is inside repository %q", project.DatabasePath, repository)
	}
	assertProjectRow(t, project, projectID, testTime.UTC().Format(time.RFC3339Nano))
	assertHealthy(t, project)
	if err := project.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := project.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	fakeClock.Advance(time.Hour)
	reopened, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        fakeClock,
	})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	assertProjectRow(t, reopened, projectID, testTime.UTC().Format(time.RFC3339Nano))

	identityBytes, err := os.ReadFile(filepath.Join(repository, projectconfig.IdentityFileName))
	if err != nil {
		t.Fatal(err)
	}
	var identity map[string]any
	if err := json.Unmarshal(identityBytes, &identity); err != nil {
		t.Fatal(err)
	}
	if len(identity) != 2 || identity["version"] != float64(1) || identity["project_id"] != projectID {
		t.Fatalf("identity contents = %#v, want only version and project_id", identity)
	}
}

func TestOpenProjectResolvesDataRootFromInputs(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	inputs := projectconfig.PathInputs{GOOS: "darwin", HomeDir: home}
	dataRoot, err := projectconfig.ResolveDataRoot(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectconfig.Initialize(repository, fixedGenerator(projectID), dataRoot); err != nil {
		t.Fatal(err)
	}
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		PathInputs:   inputs,
		Clock:        clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = project.Close(context.Background()) })
	resolvedDataRoot, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if project.DatabasePath != filepath.Join(resolvedDataRoot, "projects", projectID, "tasks.db") {
		t.Fatalf("database path = %q", project.DatabasePath)
	}
}

func TestOpenProjectRejectsMismatchedPreseededProject(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	databasePath, err := projectconfig.ProjectDatabasePath(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(context.Background(), databasePath, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(context.Background(), db, clock.NewFakeClock(testTime)); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		now := testTime.UTC().Format(time.RFC3339Nano)
		_, err := tx.ExecContext(ctx, "INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)", otherProjectID, now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err = projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        clock.NewFakeClock(testTime),
	})
	assertDomainCode(t, err, projectruntime.CodeProjectMismatch)
}

func TestOpenProjectDetectsTamperedMigrationAndCleansUp(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatal(err)
	}
	databasePath := project.DatabasePath
	if err := project.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	inspect, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspect.Exec("UPDATE schema_migrations SET checksum = ?", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if err := inspect.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        clock.NewFakeClock(testTime),
	})
	assertDomainCode(t, err, domain.CodeStorageMigration)

	renamed := databasePath + ".renamed"
	if err := os.Rename(databasePath, renamed); err != nil {
		if goruntime.GOOS == "windows" {
			t.Fatalf("rename after failed startup indicates leaked handle: %v", err)
		}
		t.Skipf("filesystem does not support verification rename: %v", err)
	}
	reopened, err := sqlite.Open(context.Background(), renamed, sqlite.Options{})
	if err != nil {
		t.Fatalf("reopen renamed database after startup failure: %v", err)
	}
	if err := reopened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProjectBackupCreatesValidatedBackup(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, "CREATE TABLE backup_test (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO backup_test(value) VALUES (?)", "backup-data")
		return err
	}); err != nil {
		t.Fatalf("write backup data: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup", "backup.db")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := project.Backup(context.Background(), backupPath)
	if err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if report.SchemaVersion != migrations.CurrentVersion() {
		t.Fatalf("schema version = %d, want %d", report.SchemaVersion, migrations.CurrentVersion())
	}
	if report.OutputPath != filepath.Clean(backupPath) {
		t.Fatalf("output path = %q, want %q", report.OutputPath, filepath.Clean(backupPath))
	}

	backupDB, err := sqlite.Open(context.Background(), report.OutputPath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open validation backup: %v", err)
	}
	defer func() { _ = backupDB.Close(context.Background()) }()

	var value string
	if err := backupDB.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT value FROM backup_test WHERE id = 1").Scan(&value)
	}); err != nil {
		t.Fatalf("read backup data: %v", err)
	}
	if value != "backup-data" {
		t.Fatalf("backup value = %q, want backup-data", value)
	}

	var count int
	if err := project.Database.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM backup_test").Scan(&count)
	}); err != nil {
		t.Fatalf("source read after backup: %v", err)
	}
	if count != 1 {
		t.Fatalf("source rows = %d, want 1", count)
	}
}

func TestProjectBackupRemovesOutputOnValidationOpenFailure(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = project.Close(context.Background()) }()

	backupPath := filepath.Join(t.TempDir(), "backup", "backup.db")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
		t.Fatal(err)
	}
	project.SQLite = sqlite.Options{RetryPolicy: &sqlite.RetryPolicy{Sleeper: nil}}
	_, err = project.Backup(context.Background(), backupPath)
	if err == nil {
		t.Fatal("Backup() unexpectedly succeeded")
	}
	assertDomainCode(t, err, projectruntime.CodeProjectBackup)
	if _, statErr := os.Stat(backupPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("backup output stat error = %v, want not-exist", statErr)
	}
}

func TestPhase1ExitGate(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	project, err := projectruntime.OpenProject(context.Background(), projectruntime.Options{
		StartingPath: repository,
		DataRoot:     dataRoot,
		Clock:        clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = project.Close(context.Background()) })

	report := assertHealthy(t, project)
	wantChecks := []string{
		"ping", "journal_mode_wal", "foreign_keys_enabled", "schema_version", "migration_history",
		"fts5", "quick_check", "foreign_key_check", "one_active_attempt_per_issue",
	}
	if len(report.Checks) != len(wantChecks) {
		t.Fatalf("health checks = %+v", report.Checks)
	}
	for index, want := range wantChecks {
		if report.Checks[index].Name != want || !report.Checks[index].Healthy {
			t.Fatalf("health check %d = %+v, want healthy %q", index, report.Checks[index], want)
		}
	}

	if err := project.Database.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		var version, historyRows, ftsRows int
		if err := query.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0), count(*) FROM schema_migrations").Scan(&version, &historyRows); err != nil {
			return err
		}
		if version != migrations.CurrentVersion() || historyRows != migrations.CurrentVersion() {
			t.Fatalf("migration version/history = %d/%d", version, historyRows)
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM search_index WHERE search_index MATCH 'phaseone'").Scan(&ftsRows); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("exit-gate read checks: %v", err)
	}

	issueID := "01C2H8V5M9Q1J7K3N6P4R0T2WX"
	attemptA := "01D2H8V5M9Q1J7K3N6P4R0T2WX"
	attemptB := "01E2H8V5M9Q1J7K3N6P4R0T2WX"
	now := testTime.UTC().Format(time.RFC3339Nano)
	if err := project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'exit gate', 'ready', 'medium', 1, ?, ?)`, issueID, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`, attemptA, issueID, now, now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err = project.Database.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'02', ?, ?, ?)`, attemptB, issueID, now, now, now)
		return err
	})
	assertDomainCode(t, err, domain.CodeStorageConstraint)

	// No-CGO compatibility is verified by the required command:
	// CGO_ENABLED=0 go test ./...
}

func initializeProject(t *testing.T) (string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(t.TempDir(), "application-data", "rhizome-mcp")
	project, err := projectconfig.Initialize(repository, fixedGenerator(projectID), dataRoot)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return project.Root, dataRoot
}

func assertHealthy(t *testing.T, project *projectruntime.Project) projectruntime.HealthReport {
	t.Helper()
	report, err := project.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v, report = %+v", err, report)
	}
	if !report.Healthy() || report.ExpectedSchemaVersion != migrations.CurrentVersion() || report.CurrentSchemaVersion != migrations.CurrentVersion() {
		t.Fatalf("unhealthy report = %+v", report)
	}
	return report
}

func assertProjectRow(t *testing.T, project *projectruntime.Project, wantID, wantCreatedAt string) {
	t.Helper()
	if err := project.Database.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		var id, createdAt, updatedAt string
		var next int
		if err := query.QueryRowContext(ctx, "SELECT id, next_issue_number, created_at, updated_at FROM projects").Scan(&id, &next, &createdAt, &updatedAt); err != nil {
			return err
		}
		if id != wantID || next != 1 || createdAt != wantCreatedAt || updatedAt != wantCreatedAt {
			t.Fatalf("project row = %q/%d/%q/%q", id, next, createdAt, updatedAt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, &domain.Error{Code: code}) {
		t.Fatalf("error = %v, want domain code %s", err, code)
	}
}

func isWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && (relative == "." || len(relative) < 3 || relative[:3] != ".."+string(filepath.Separator))
}
