package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
)

func TestProjectRepositoryReturnsMetadataAndDeterministicMaximums(t *testing.T) {
	db, now := openProjectDatabase(t, "Project name", "Project instructions")
	ctx := context.Background()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations(version, name, checksum, applied_at)
			VALUES (3, 'later_migration', 'checksum', ?)`, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations(version, name, checksum, applied_at)
			VALUES (2, 'middle_migration', 'checksum', ?)`, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, payload, created_at)
			VALUES (NULL, 'project_event', '{}', ?)`, now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, payload, created_at)
			VALUES (NULL, 'project_event', '{}', ?)`, now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatalf("seed latest event: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	service, err := application.NewProjectService(repository)
	if err != nil {
		t.Fatalf("NewProjectService() error = %v", err)
	}
	got, err := service.GetProject(ctx)
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}

	if got.ID != sqliteTestProjectID || got.Name == nil || *got.Name != "Project name" ||
		got.Instructions == nil || *got.Instructions != "Project instructions" {
		t.Fatalf("project identity/text = %#v", got)
	}
	if got.NextIssueNumber != 7 || !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("project values = %#v", got)
	}
	if got.SchemaVersion != 3 || got.LatestEventID != 2 {
		t.Fatalf("derived values = schema %d, event %d; want 3, 2", got.SchemaVersion, got.LatestEventID)
	}
}

func TestProjectRepositoryMapsNullableMetadataAndNoEventToZero(t *testing.T) {
	db, now := openProjectDatabase(t, "", "")
	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	got, err := repository.GetProject(context.Background())
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if got.Name != nil || got.Instructions != nil {
		t.Fatalf("nullable values = name %#v, instructions %#v; want nil", got.Name, got.Instructions)
	}
	if got.LatestEventID != 0 {
		t.Fatalf("latest event ID = %d, want 0", got.LatestEventID)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %v, %v; want %v", got.CreatedAt, got.UpdatedAt, now)
	}
}

func TestProjectRepositoryMapsTimestampCorruptionToStableError(t *testing.T) {
	db, _ := openProjectDatabase(t, "name", "instructions")
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE projects SET created_at = 'not-a-timestamp'")
		return err
	}); err != nil {
		t.Fatalf("corrupt timestamp: %v", err)
	}
	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	_, err = repository.GetProject(context.Background())
	assertProjectDomainCode(t, err, domain.CodeStorageCorrupt)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || len(domainErr.Details) != 1 ||
		domainErr.Details[0].Field != "created_at" ||
		domainErr.Details[0].Code != "INVALID_TIMESTAMP" {
		t.Fatalf("corruption details = %#v", err)
	}
}

func TestProjectRepositoryRejectsMissingOrDuplicateProjectRows(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		db, _ := openProjectDatabase(t, "", "")
		if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM projects")
			return err
		}); err != nil {
			t.Fatalf("delete project: %v", err)
		}
		repository, err := sqlite.NewProjectRepository(db)
		if err != nil {
			t.Fatalf("NewProjectRepository() error = %v", err)
		}
		_, err = repository.GetProject(context.Background())
		assertProjectDomainCode(t, err, domain.CodeProjectNotInitialized)
	})

	t.Run("duplicate", func(t *testing.T) {
		db, now := openProjectDatabase(t, "", "")
		if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO projects(id, next_issue_number, created_at, updated_at)
				VALUES (?, 1, ?, ?)`,
				"01ARZ3NDEKTSV4RRFFQ69G5FAS", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
			return err
		}); err != nil {
			t.Fatalf("insert duplicate project: %v", err)
		}
		repository, err := sqlite.NewProjectRepository(db)
		if err != nil {
			t.Fatalf("NewProjectRepository() error = %v", err)
		}
		_, err = repository.GetProject(context.Background())
		assertProjectDomainCode(t, err, domain.CodeStorageCorrupt)
	})
}

func TestProjectRepositoryHasNoWriteSideEffects(t *testing.T) {
	db, _ := openProjectDatabase(t, "name", "instructions")
	var before, after struct {
		projects, events, migrations int
	}
	queryCounts := func(counts *struct {
		projects, events, migrations int
	}) error {
		return db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
			return query.QueryRowContext(ctx, `
				SELECT
					(SELECT count(*) FROM projects),
					(SELECT count(*) FROM issue_events),
					(SELECT count(*) FROM schema_migrations)`,
			).Scan(&counts.projects, &counts.events, &counts.migrations)
		})
	}
	if err := queryCounts(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := repository.GetProject(context.Background()); err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if err := queryCounts(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if before != after {
		t.Fatalf("counts changed from %#v to %#v", before, after)
	}
}

func openProjectDatabase(t *testing.T, name, instructions string) (*sqlite.DB, time.Time) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "project.db"), sqlite.Options{
		RetryPolicy: &sqlite.RetryPolicy{
			Delays:  []time.Duration{},
			Sleeper: sqlite.SleepFunc(func(context.Context, time.Duration) error { return nil }),
		},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	now := time.Date(2026, 7, 14, 10, 11, 12, 0, time.UTC)
	if _, err := migrations.Migrate(context.Background(), db, fixedMigrationClock{now: now}); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		var nameValue, instructionsValue any
		if name != "" {
			nameValue = name
		}
		if instructions != "" {
			instructionsValue = instructions
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO projects(id, name, instructions, next_issue_number, created_at, updated_at)
			VALUES (?, ?, ?, 7, ?, ?)`,
			sqliteTestProjectID, nameValue, instructionsValue,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return db, now
}

type fixedMigrationClock struct {
	now time.Time
}

func (clock fixedMigrationClock) Now() time.Time {
	return clock.now
}

func assertProjectDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, &domain.Error{Code: code}) {
		t.Fatalf("error = %v, want domain code %s", err, code)
	}
}
