package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
)

var migrationTime = time.Date(2026, 7, 13, 10, 11, 12, 123456789, time.FixedZone("test", 2*60*60))

func TestMigrateEmptyDatabaseCreatesCompleteSchema(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	result, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if result != (Result{Version: CurrentVersion(), Applied: 3}) {
		t.Fatalf("Migrate() result = %+v, want current version with three applied migrations", result)
	}

	inspect := openInspectionDB(t, path)
	ordinaryTables := []string{
		"agent_sessions", "artifacts", "attempt_notes", "comments", "decisions", "idempotency_records",
		"issue_events", "issue_labels", "issue_relations", "issues", "labels", "projects",
		"review_events", "review_outcomes", "review_requests", "review_targets", "schema_migrations", "work_attempts",
	}
	for _, table := range ordinaryTables {
		var tableType string
		var strict int
		err := inspect.QueryRow(`SELECT type, strict FROM pragma_table_list WHERE schema = 'main' AND name = ?`, table).Scan(&tableType, &strict)
		if err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if tableType != "table" || strict != 1 {
			t.Errorf("table %s type/strict = %s/%d, want table/1", table, tableType, strict)
		}
	}

	var searchSQL string
	if err := inspect.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'search_index'").Scan(&searchSQL); err != nil {
		t.Fatalf("inspect search_index: %v", err)
	}
	if !strings.Contains(strings.ToLower(searchSQL), "using fts5") {
		t.Fatalf("search_index SQL does not define FTS5: %s", searchSQL)
	}

	wantIndexes := []string{
		"idx_attempts_active_lease", "idx_attempts_issue_started", "idx_comments_issue_created",
		"idx_decisions_issue_status", "idx_decisions_supersedes", "idx_events_issue_id",
		"idx_issue_labels_label", "idx_issues_archived", "idx_issues_parent", "idx_issues_status_priority",
		"idx_labels_name_nocase", "idx_one_active_attempt_per_issue", "idx_relations_source", "idx_relations_target",
		"idx_review_requests_active_attempt", "idx_review_requests_active_target", "idx_review_targets_issue_version",
	}
	rows, err := inspect.Query("SELECT name FROM sqlite_schema WHERE type = 'index' AND name NOT LIKE 'sqlite_autoindex_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var gotIndexes []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		gotIndexes = append(gotIndexes, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotIndexes, wantIndexes) {
		t.Fatalf("indexes = %v, want %v", gotIndexes, wantIndexes)
	}

	var version int
	var name, checksum, appliedAt string
	if err := inspect.QueryRow(`SELECT version, name, checksum, applied_at
		FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &name, &checksum, &appliedAt); err != nil {
		t.Fatal(err)
	}
	if version != CurrentVersion() || name != "review_workflow" || checksum != reviewWorkflowChecksum {
		t.Fatalf("history = (%d, %q, %q), want current embedded migration", version, name, checksum)
	}
	actualChecksum := sha256.Sum256([]byte(reviewWorkflowSQL))
	if checksum != hex.EncodeToString(actualChecksum[:]) {
		t.Fatalf("stored checksum = %s, want SHA-256 of embedded bytes", checksum)
	}
	if appliedAt != migrationTime.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("applied_at = %q, want %q", appliedAt, migrationTime.UTC().Format(time.RFC3339Nano))
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	fakeClock := clock.NewFakeClock(migrationTime)
	first, err := Migrate(context.Background(), db, fakeClock)
	if err != nil {
		t.Fatal(err)
	}
	fakeClock.Advance(time.Hour)
	second, err := Migrate(context.Background(), db, fakeClock)
	if err != nil {
		t.Fatal(err)
	}
	if first.Applied != 3 || second != (Result{Version: CurrentVersion(), Applied: 0}) {
		t.Fatalf("results = %+v then %+v", first, second)
	}
	inspect := openInspectionDB(t, path)
	var count int
	var appliedAt string
	if err := inspect.QueryRow("SELECT count(*), min(applied_at) FROM schema_migrations").Scan(&count, &appliedAt); err != nil {
		t.Fatal(err)
	}
	if count != CurrentVersion() || appliedAt != migrationTime.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("history count/applied_at = %d/%q", count, appliedAt)
	}
}

func TestMigrateUpgradesExistingRowsIntoSearchIndex(t *testing.T) {
	t.Parallel()
	_, db := openMigrationDB(t)
	ctx := context.Background()
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:1]); err != nil {
		t.Fatalf("migrate initial schema: %v", err)
	}

	issueID := testID(1)
	commentID := testID(2)
	decisionID := testID(3)
	attemptID := testID(4)
	noteID := testID(5)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		insertIssueSQL := `INSERT INTO issues(
			id, sequence_no, type, title, description, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'indexed issue', 'issue body', 'ready', 'medium', 1, ?, ?)`
		if _, err := tx.ExecContext(ctx, insertIssueSQL, issueID, nowText(), nowText()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at)
			VALUES (?, ?, 'comment body', ?)`, commentID, issueID, nowText()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions(
			id, issue_id, title, summary, content, status, created_at
		) VALUES (?, ?, 'decision title', 'decision summary', 'decision body', 'active', ?)`,
			decisionID, issueID, nowText()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`,
			attemptID, issueID, nowText(), nowText(), nowText()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(
			id, attempt_id, kind, content, important, created_at
		) VALUES (?, ?, 'progress', 'note body', 0, ?)`, noteID, attemptID, nowText())
		return err
	}); err != nil {
		t.Fatalf("seed pre-upgrade rows: %v", err)
	}

	result, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime))
	if err != nil {
		t.Fatalf("upgrade migration: %v", err)
	}
	if result != (Result{Version: CurrentVersion(), Applied: 2}) {
		t.Fatalf("upgrade result = %+v, want two applied migrations", result)
	}

	var count int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM search_index").Scan(&count)
	}); err != nil {
		t.Fatalf("read rebuilt search index: %v", err)
	}
	if count != 4 {
		t.Fatalf("search index rows = %d, want 4", count)
	}
}

func TestVerifyHistoryIsReadOnlyAndDetectsTampering(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	if _, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime)); err != nil {
		t.Fatal(err)
	}

	var version int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		var err error
		version, err = VerifyHistory(ctx, query)
		return err
	}); err != nil {
		t.Fatalf("VerifyHistory() error = %v", err)
	}
	if version != CurrentVersion() {
		t.Fatalf("VerifyHistory() version = %d, want %d", version, CurrentVersion())
	}

	inspect := openInspectionDB(t, path)
	if _, err := inspect.Exec("UPDATE schema_migrations SET checksum = ?", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		_, err := VerifyHistory(ctx, query)
		return err
	})
	assertDomainCode(t, err, domain.CodeStorageMigration)
}

func TestConcurrentRunnersHaveOneMigrationOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	firstDB, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstDB.Close(context.Background()) })
	secondDB, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close(context.Background()) })

	start := make(chan struct{})
	results := make(chan Result, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*sqlite.DB{firstDB, secondDB} {
		wg.Add(1)
		go func(db *sqlite.DB) {
			defer wg.Done()
			<-start
			result, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime))
			results <- result
			errs <- err
		}(db)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Migrate() error = %v", err)
		}
	}
	applied := 0
	for result := range results {
		if result.Version != CurrentVersion() {
			t.Errorf("concurrent version = %d, want %d", result.Version, CurrentVersion())
		}
		applied += result.Applied
	}
	if applied != CurrentVersion() {
		t.Fatalf("total applied migrations = %d, want %d", applied, CurrentVersion())
	}
}

func TestMigrateRejectsTamperedAndMalformedHistory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		tamper string
	}{
		{name: "checksum", tamper: "UPDATE schema_migrations SET checksum = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'"},
		{name: "name", tamper: "UPDATE schema_migrations SET name = 'renamed_schema'"},
		{name: "future", tamper: "INSERT INTO schema_migrations VALUES (4, 'future_schema', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '2026-01-01T00:00:00Z')"},
		{name: "malformed version", tamper: "UPDATE schema_migrations SET version = 0 WHERE version = 1"},
		{name: "malformed checksum", tamper: "UPDATE schema_migrations SET checksum = 'not-sha256'"},
		{name: "malformed timestamp", tamper: "UPDATE schema_migrations SET applied_at = 'not-a-timestamp'"},
		{name: "non UTC timestamp", tamper: "UPDATE schema_migrations SET applied_at = '2026-01-01T01:00:00+01:00'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, db := openMigrationDB(t)
			if _, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime)); err != nil {
				t.Fatal(err)
			}
			inspect := openInspectionDB(t, path)
			if _, err := inspect.Exec(test.tamper); err != nil {
				t.Fatalf("tamper history: %v", err)
			}
			_, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime))
			assertDomainCode(t, err, domain.CodeStorageMigration)
		})
	}
}

func TestInvalidCatalogIsRejectedBeforeDatabaseWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		catalog []migration
	}{
		{name: "empty", catalog: nil},
		{name: "zero version", catalog: []migration{testMigration(0, "test_zero", "SELECT 1")}},
		{name: "duplicate version", catalog: []migration{testMigration(1, "test_one", "SELECT 1"), testMigration(1, "test_two", "SELECT 2")}},
		{name: "gap", catalog: []migration{testMigration(1, "test_one", "SELECT 1"), testMigration(3, "test_three", "SELECT 1")}},
		{name: "out of order", catalog: []migration{testMigration(2, "test_two", "SELECT 1"), testMigration(1, "test_one", "SELECT 1")}},
		{name: "invalid name", catalog: []migration{testMigration(1, "Bad Name", "SELECT 1")}},
		{name: "duplicate name", catalog: []migration{testMigration(1, "test_same", "SELECT 1"), testMigration(2, "test_same", "SELECT 2")}},
		{name: "invalid checksum", catalog: []migration{{version: 1, name: "test_one", checksum: strings.Repeat("0", 64), sql: "SELECT 1"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, db := openMigrationDB(t)
			_, err := run(context.Background(), db, clock.NewFakeClock(migrationTime), test.catalog)
			assertDomainCode(t, err, domain.CodeStorageMigration)
			inspect := openInspectionDB(t, path)
			var count int
			if err := inspect.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name = 'schema_migrations'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("invalid catalog touched the database")
			}
		})
	}
}

func TestBrokenMigrationRollsBackScriptAndHistory(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	catalog := []migration{
		testMigration(1, "test_base", "CREATE TABLE base_table (id INTEGER PRIMARY KEY) STRICT;"),
		testMigration(2, "test_broken", "CREATE TABLE must_rollback (id INTEGER) STRICT; INSERT INTO missing_table VALUES (1);"),
	}
	_, err := run(context.Background(), db, clock.NewFakeClock(migrationTime), catalog)
	assertDomainCode(t, err, domain.CodeStorageMigration)

	inspect := openInspectionDB(t, path)
	for _, table := range []string{"schema_migrations", "base_table", "must_rollback"} {
		var count int
		if err := inspect.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("table %s survived failed migration transaction", table)
		}
	}
}

func TestMigrateReportsForeignKeyViolationsDeterministically(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	if _, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime)); err != nil {
		t.Fatal(err)
	}
	inspect := openInspectionDB(t, path)
	if _, err := inspect.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"INSERT INTO comments(id, issue_id, content, created_at) VALUES ('00000000000000000000000002', '99999999999999999999999999', 'two', '2026-01-01T00:00:00Z')",
		"INSERT INTO comments(id, issue_id, content, created_at) VALUES ('00000000000000000000000001', '88888888888888888888888888', 'one', '2026-01-01T00:00:00Z')",
	} {
		if _, err := inspect.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := inspect.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime))
	assertDomainCode(t, err, domain.CodeStorageMigration)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want domain error", err)
	}
	if len(domainErr.Details) != 2 {
		t.Fatalf("foreign-key details = %+v, want two", domainErr.Details)
	}
	if domainErr.Details[0].Field >= domainErr.Details[1].Field {
		t.Fatalf("foreign-key details are not deterministic: %+v", domainErr.Details)
	}
	for _, detail := range domainErr.Details {
		if detail.Code != "FOREIGN_KEY_VIOLATION" || !strings.Contains(detail.Message, "table=comments") || !strings.Contains(detail.Message, "parent=issues") {
			t.Errorf("foreign-key detail = %+v", detail)
		}
	}
}

func TestInitialSchemaCriticalConstraintsAndFTS(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	if _, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime)); err != nil {
		t.Fatal(err)
	}
	inspect := openInspectionDB(t, path)
	if _, err := inspect.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}

	issueA := testID(1)
	issueB := testID(2)
	insertIssue(t, inspect, issueA, 1, "ready", nil)
	insertIssue(t, inspect, issueB, 2, "ready", nil)

	assertSQLFails(t, inspect, "INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'duplicate', 'ready', 'medium', 1, ?, ?)", testID(3), nowText(), nowText())
	assertSQLFails(t, inspect, "UPDATE issues SET sequence_no = 3 WHERE id = ?", issueA)
	assertSQLFails(t, inspect, "INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 3, 'task', 'bad status', 'in_progress', 'medium', 1, ?, ?)", testID(3), nowText(), nowText())
	assertSQLFails(t, inspect, "INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 3, 'task', 'blocked', 'blocked', 'medium', 1, ?, ?)", testID(3), nowText(), nowText())
	assertSQLFails(t, inspect, "UPDATE issues SET status = 'ready', blocked_reason = 'stale' WHERE id = ?", issueA)
	assertSQLFails(t, inspect, "INSERT INTO issues(id, sequence_no, type, title, status, priority, parent_id, version, created_at, updated_at) VALUES (?, 3, 'task', 'bad parent', 'ready', 'medium', ?, 1, ?, ?)", testID(3), testID(99), nowText(), nowText())

	if _, err := inspect.Exec("INSERT INTO labels(id, name, created_at) VALUES (?, 'Database', ?)", testID(10), nowText()); err != nil {
		t.Fatal(err)
	}
	assertSQLFails(t, inspect, "INSERT INTO labels(id, name, created_at) VALUES (?, 'database', ?)", testID(11), nowText())

	assertSQLFails(t, inspect, "INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, 'blocks', ?)", testID(20), issueA, issueA, nowText())
	assertSQLFails(t, inspect, "INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, 'related_to', ?)", testID(21), issueB, issueA, nowText())
	if _, err := inspect.Exec("INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, 'related_to', ?)", testID(22), issueA, issueB, nowText()); err != nil {
		t.Fatal(err)
	}
	assertSQLFails(t, inspect, "INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, 'related_to', ?)", testID(23), issueA, issueB, nowText())

	insertActiveAttempt(t, inspect, testID(30), issueA)
	assertSQLFails(t, inspect, `INSERT INTO work_attempts(
		id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash,
		lease_expires_at, started_at, last_heartbeat_at
	) VALUES (?, ?, 'work', 'active', 1, 0, ?, ?, ?, ?)`, testID(31), issueA, []byte("hash-two"), nowText(), nowText(), nowText())

	assertSQLFails(t, inspect, "INSERT INTO attempt_notes(id, attempt_id, kind, content, important, created_at) VALUES (?, ?, 'progress', 'bad boolean', 2, ?)", testID(40), testID(30), nowText())
	assertSQLFails(t, inspect, "INSERT INTO idempotency_records(idempotency_key, operation, request_hash, response_json, created_at) VALUES ('key', 'create_issue', X'01', 'not-json', ?)", nowText())
	if _, err := inspect.Exec("INSERT INTO idempotency_records(idempotency_key, operation, request_hash, response_json, created_at) VALUES ('key', 'create_issue', X'01', '{}', ?)", nowText()); err != nil {
		t.Fatal(err)
	}
	assertSQLFails(t, inspect, "INSERT INTO idempotency_records(idempotency_key, operation, request_hash, response_json, created_at) VALUES ('key', 'create_issue', X'01', '{}', ?)", nowText())

	result, err := inspect.Exec("INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (?, 'issue_created', '{}', ?)", issueA, nowText())
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := result.LastInsertId()
	if err != nil || eventID <= 0 {
		t.Fatalf("event ID = %d, error = %v", eventID, err)
	}
	assertSQLFails(t, inspect, "UPDATE issue_events SET id = id + 1 WHERE id = ?", eventID)
	assertSQLFails(t, inspect, "DELETE FROM issue_events WHERE id = ?", eventID)

	if _, err := inspect.Exec("INSERT INTO search_index(entity_type, entity_id, issue_id, title, content) VALUES ('issue', ?, ?, 'Renewable lease', 'heartbeat coordination')", issueA, issueA); err != nil {
		t.Fatal(err)
	}
	var entityID string
	if err := inspect.QueryRow("SELECT entity_id FROM search_index WHERE search_index MATCH 'renewable AND heartbeat'").Scan(&entityID); err != nil {
		t.Fatalf("FTS5 query: %v", err)
	}
	if entityID != issueA {
		t.Fatalf("FTS entity = %q, want %q", entityID, issueA)
	}
}

func TestMigrationRunsAgainstSQLiteOpenBootstrap(t *testing.T) {
	t.Parallel()
	_, db := openMigrationDB(t)
	result, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime))
	if err != nil {
		t.Fatalf("Migrate() after sqlite.Open() = %v", err)
	}
	if result.Version != CurrentVersion() {
		t.Fatalf("schema version = %d, want %d", result.Version, CurrentVersion())
	}
}

func openMigrationDB(t *testing.T) (string, *sqlite.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return path, db
}

func openInspectionDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
			t.Errorf("inspection Close() error = %v", err)
		}
	})
	return db
}

func testMigration(version int, name, script string) migration {
	sum := sha256.Sum256([]byte(script))
	return migration{version: version, name: name, checksum: hex.EncodeToString(sum[:]), sql: script}
}

func testID(number int) string {
	return fmt.Sprintf("%026d", number)
}

func nowText() string {
	return migrationTime.UTC().Format(time.RFC3339Nano)
}

func insertIssue(t *testing.T, db *sql.DB, id string, sequence int, status string, blockedReason any) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO issues(
		id, sequence_no, type, title, status, priority, blocked_reason, version, created_at, updated_at
	) VALUES (?, ?, 'task', ?, ?, 'medium', ?, 1, ?, ?)`, id, sequence, "issue "+id, status, blockedReason, nowText(), nowText())
	if err != nil {
		t.Fatalf("insert issue %s: %v", id, err)
	}
}

func insertActiveAttempt(t *testing.T, db *sql.DB, id, issueID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO work_attempts(
		id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash,
		lease_expires_at, started_at, last_heartbeat_at
	) VALUES (?, ?, 'work', 'active', 1, 0, ?, ?, ?, ?)`, id, issueID, []byte("hash-one"), nowText(), nowText(), nowText())
	if err != nil {
		t.Fatalf("insert active attempt: %v", err)
	}
}

func assertSQLFails(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err == nil {
		t.Fatalf("SQL unexpectedly succeeded: %s", strings.Join(strings.Fields(query), " "))
	}
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, &domain.Error{Code: code}) {
		t.Fatalf("error = %v, want domain code %s", err, code)
	}
}
