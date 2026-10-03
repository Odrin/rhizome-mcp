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

func TestMigrateCurrentSchemaWithReversedUnorderedScans(t *testing.T) {
	t.Parallel()
	_, db := openMigrationDB(t)
	ctx := context.Background()
	fakeClock := clock.NewFakeClock(migrationTime)
	if _, err := Migrate(ctx, db, fakeClock); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "PRAGMA reverse_unordered_selects = ON")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Migrate(ctx, db, fakeClock)
	if err != nil {
		t.Fatalf("healthy schema rejected with reversed unordered scans: %v", err)
	}
	if result != (Result{Version: CurrentVersion()}) {
		t.Fatalf("result = %+v, want unchanged current schema", result)
	}
}

func TestMigrateEmptyDatabaseCreatesCompleteSchema(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	result, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if result != (Result{Version: CurrentVersion(), Applied: CurrentVersion()}) {
		t.Fatalf("Migrate() result = %+v, want all embedded migrations", result)
	}

	inspect := openInspectionDB(t, path)
	ordinaryTables := []string{
		"agent_sessions", "artifacts", "attempt_notes", "comments", "decisions", "idempotency_records",
		"issue_events", "issue_labels", "issue_relations", "issues", "labels", "projects",
		"resource_reservations",
		"review_approvals", "review_follow_ups", "review_outcomes", "review_requests", "review_targets",
		"schema_migrations", "search_index_identity", "work_attempts",
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
		"agent_sessions_handle_hash_idx",
		"idx_artifacts_issue_created_id", "idx_attempt_notes_attempt_created_id",
		"idx_attempts_active_lease", "idx_attempts_issue_started", "idx_comments_issue_created",
		"idx_decisions_issue_status", "idx_decisions_supersedes", "idx_events_issue_id",
		"idx_gate_evidence_events_attempt", "idx_gate_evidence_issue",
		"idx_issue_labels_label", "idx_issues_archived", "idx_issues_parent", "idx_issues_status_priority",
		"idx_labels_name_nocase", "idx_one_active_attempt_per_issue", "idx_relations_source", "idx_relations_target",
		"idx_reservations_active", "idx_reservations_active_identity", "idx_reservations_attempt", "idx_reservations_issue",
		"idx_review_approvals_issue_purpose", "idx_review_approvals_request_purpose",
		"idx_review_requests_active_attempt", "idx_review_requests_active_issue_version", "idx_review_requests_active_target",
		"idx_review_requests_created_id", "idx_review_requests_open_issue_created_id", "idx_review_requests_status_created_id", "idx_review_targets_issue_version",
		"idx_search_index_identity_issue_type",
		"idx_workflow_policies_status_created", "idx_workflow_policy_events_policy",
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
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
	if version != CurrentVersion() || name != "activity_order" || checksum != activityOrderChecksum {
		t.Fatalf("history = (%d, %q, %q), want current embedded migration", version, name, checksum)
	}
	actualChecksum := sha256.Sum256([]byte(activityOrderSQL))
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
	if first.Applied != CurrentVersion() || second != (Result{Version: CurrentVersion(), Applied: 0}) {
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

func TestMigrateReviewIndexesUpgradeAndPlans(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path, db := openMigrationDB(t)
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:17]); err != nil {
		t.Fatal(err)
	}
	result, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime))
	if err != nil || result != (Result{Version: CurrentVersion(), Applied: 2}) {
		t.Fatalf("upgrade = %+v, %v", result, err)
	}
	inspect := openInspectionDB(t, path)
	for _, test := range []struct {
		query, index string
	}{
		{`SELECT id FROM review_requests ORDER BY created_at DESC, id DESC LIMIT 21`, "idx_review_requests_created_id"},
		{`SELECT id FROM review_requests WHERE status=? AND (created_at, id) < (?, ?) ORDER BY created_at DESC, id DESC LIMIT 21`, "idx_review_requests_status_created_id"},
		{`SELECT id FROM review_requests WHERE issue_id=? AND status='open' ORDER BY created_at DESC, id DESC LIMIT 1`, "idx_review_requests_open_issue_created_id"},
	} {
		args := []any{}
		if strings.Contains(test.query, "status=?") {
			args = []any{"open", nowText(), testID(1)}
		} else if strings.Contains(test.query, "issue_id=?") {
			args = []any{testID(1)}
		}
		rows, err := inspect.Query("EXPLAIN QUERY PLAN "+test.query, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), test.index) || strings.Contains(plan.String(), "USE TEMP B-TREE") {
			t.Fatalf("query plan = %s, want %s without sort", plan.String(), test.index)
		}
	}
}

func TestMigrateActivityOrderPopulatedUpgradeAndPlans(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path, db := openMigrationDB(t)
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:18]); err != nil {
		t.Fatal(err)
	}
	issueID, attemptID := testID(1), testID(2)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		for _, statement := range []string{
			fmt.Sprintf(`INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
				VALUES ('%s', 1, 'task', 'upgrade', 'ready', 'medium', 1, '%s', '%s')`, issueID, nowText(), nowText()),
			fmt.Sprintf(`INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
				lease_token_hash, lease_expires_at, started_at, last_heartbeat_at)
				VALUES ('%s', '%s', 'work', 'active', 1, 0, X'01', '%s', '%s', '%s')`, attemptID, issueID, nowText(), nowText(), nowText()),
			fmt.Sprintf(`INSERT INTO artifacts(id, issue_id, type, uri, created_at)
				VALUES ('%s', '%s', 'file', 'first', '%s'), ('%s', '%s', 'file', 'second', '%s')`,
				testID(3), issueID, nowText(), testID(4), issueID, nowText()),
			fmt.Sprintf(`INSERT INTO attempt_notes(id, attempt_id, kind, content, important, created_at)
				VALUES ('%s', '%s', 'progress', 'first', 0, '%s'), ('%s', '%s', 'progress', 'second', 0, '%s')`,
				testID(5), attemptID, nowText(), testID(6), attemptID, nowText()),
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime)); err != nil || result != (Result{Version: 19, Applied: 1}) {
		t.Fatalf("populated upgrade = %+v, %v", result, err)
	}
	inspect := openInspectionDB(t, path)
	for _, test := range []struct {
		statement string
		args      []any
		index     string
	}{
		{`SELECT id FROM artifacts WHERE issue_id=? ORDER BY created_at DESC, id ASC LIMIT 21`,
			[]any{issueID}, "idx_artifacts_issue_created_id"},
		{`SELECT attempt_notes.id FROM work_attempts CROSS JOIN attempt_notes
			ON attempt_notes.attempt_id=work_attempts.id WHERE work_attempts.issue_id=?
			ORDER BY attempt_notes.created_at DESC, attempt_notes.id ASC LIMIT 21`,
			[]any{issueID}, "idx_attempt_notes_attempt_created_id"},
	} {
		rows, err := inspect.Query("EXPLAIN QUERY PLAN "+test.statement, test.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan.WriteString(detail)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), test.index) ||
			(test.index == "idx_attempt_notes_attempt_created_id" && !strings.Contains(plan.String(), "idx_attempts_issue_started")) ||
			strings.Contains(plan.String(), "SCAN artifacts") || strings.Contains(plan.String(), "SCAN attempt_notes") {
			t.Fatalf("unscoped query plan for %s: %s", test.index, plan.String())
		}
	}
	for _, test := range []struct {
		statement string
		want      []string
	}{
		{`SELECT id FROM artifacts WHERE issue_id=? ORDER BY created_at DESC, id ASC`, []string{testID(3), testID(4)}},
		{`SELECT attempt_notes.id FROM work_attempts CROSS JOIN attempt_notes ON attempt_notes.attempt_id=work_attempts.id
			WHERE work_attempts.issue_id=? ORDER BY attempt_notes.created_at DESC, attempt_notes.id ASC`, []string{testID(5), testID(6)}},
	} {
		rows, err := inspect.Query(test.statement, issueID)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			got = append(got, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("rows = %v, want %v", got, test.want)
		}
	}
	var integrity string
	if err := inspect.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q, %v", integrity, err)
	}
	var violation string
	if err := inspect.QueryRow("SELECT `table` FROM pragma_foreign_key_check LIMIT 1").Scan(&violation); err != sql.ErrNoRows {
		t.Fatalf("foreign_key_check = %q, %v", violation, err)
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
	if result != (Result{Version: CurrentVersion(), Applied: CurrentVersion() - 1}) {
		t.Fatalf("upgrade result = %+v, want migrations after schema 1", result)
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

func TestMigrateReviewContextUpgradePreservesHistory(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	ctx := context.Background()
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:3]); err != nil {
		t.Fatalf("seed migrations through 003: %v", err)
	}

	issueID := testID(10)
	targetID := testID(11)
	reviewID := testID(12)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'reviewed issue', 'ready', 'medium', 1, ?, ?)`, issueID, nowText(), nowText()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(
			id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at
		) VALUES (?, ?, 1, 0, '[]', 1, ?)`, targetID, issueID, nowText()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO review_requests(
			id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status, supersedes_id,
			active_attempt_id, version, created_at, resolved_at
		) VALUES (?, ?, ?, 1, 0, '[]', 'open', NULL, NULL, 1, ?, NULL)`, reviewID, targetID, issueID, nowText())
		return err
	}); err != nil {
		t.Fatalf("seed review rows: %v", err)
	}

	inspect := openInspectionDB(t, path)
	var before []struct {
		version   int
		name      string
		checksum  string
		appliedAt string
	}
	rows, err := inspect.Query(`SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query history before upgrade: %v", err)
	}
	for rows.Next() {
		var row struct {
			version   int
			name      string
			checksum  string
			appliedAt string
		}
		if err := rows.Scan(&row.version, &row.name, &row.checksum, &row.appliedAt); err != nil {
			rows.Close()
			t.Fatalf("scan history before upgrade: %v", err)
		}
		before = append(before, row)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close history before upgrade: %v", err)
	}

	result, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime))
	if err != nil {
		t.Fatalf("upgrade to review_context: %v", err)
	}
	if result != (Result{Version: CurrentVersion(), Applied: CurrentVersion() - 3}) {
		t.Fatalf("upgrade result = %+v, want remaining embedded migrations", result)
	}

	var after []struct {
		version   int
		name      string
		checksum  string
		appliedAt string
	}
	rows, err = inspect.Query(`SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query history after upgrade: %v", err)
	}
	for rows.Next() {
		var row struct {
			version   int
			name      string
			checksum  string
			appliedAt string
		}
		if err := rows.Scan(&row.version, &row.name, &row.checksum, &row.appliedAt); err != nil {
			rows.Close()
			t.Fatalf("scan history after upgrade: %v", err)
		}
		after = append(after, row)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close history after upgrade: %v", err)
	}
	if len(after) != CurrentVersion() {
		t.Fatalf("history rows = %d, want %d", len(after), CurrentVersion())
	}
	for index, row := range before {
		if after[index].version != row.version || after[index].name != row.name || after[index].checksum != row.checksum || after[index].appliedAt != row.appliedAt {
			t.Fatalf("history row %d changed: before %+v after %+v", index, row, after[index])
		}
	}
	if after[len(after)-1].version != CurrentVersion() || after[len(after)-1].name != "activity_order" || after[len(after)-1].checksum != activityOrderChecksum {
		t.Fatalf("new history row = %+v, want activity_order migration", after[len(after)-1])
	}
	var count int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM search_index WHERE entity_type = 'review' AND entity_id = ?", reviewID).Scan(&count)
	}); err != nil {
		t.Fatalf("read review search index row: %v", err)
	}
	if count != 1 {
		t.Fatalf("review search index rows = %d, want 1", count)
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

func TestMigrateCurrentSchemaReadsWhileWriterOwnsLock(t *testing.T) {
	path, db := openMigrationDB(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime)); err != nil {
		t.Fatal(err)
	}
	writer := openInspectionDB(t, path)
	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	// A current-schema open must finish its complete FK check while another
	// connection holds the writer lock, rather than waiting for it.
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	result, err := Migrate(deadline, db, clock.NewFakeClock(migrationTime))
	if err != nil {
		t.Fatalf("current-schema migration blocked by writer: %v", err)
	}
	if result != (Result{Version: CurrentVersion()}) {
		t.Fatalf("current-schema result = %+v", result)
	}
}

func TestConcurrentUpgradesRecheckHistoryUnderWriterLock(t *testing.T) {
	path, firstDB := openMigrationDB(t)
	ctx := context.Background()
	if _, err := run(ctx, firstDB, clock.NewFakeClock(migrationTime), embeddedCatalog[:16]); err != nil {
		t.Fatal(err)
	}
	secondDB, err := sqlite.Open(ctx, path, sqlite.Options{})
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
			result, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime))
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
			t.Fatalf("concurrent upgrade: %v", err)
		}
	}
	var applied, winners int
	for result := range results {
		if result.Version != CurrentVersion() {
			t.Fatalf("upgrade version = %d", result.Version)
		}
		applied += result.Applied
		if result.Applied > 0 {
			winners++
		}
	}
	if applied != CurrentVersion()-16 || winners != 1 {
		t.Fatalf("applied migrations = %d across %d owners, want %d across one owner", applied, winners, CurrentVersion()-16)
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
		{name: "future", tamper: fmt.Sprintf("INSERT INTO schema_migrations VALUES (%d, 'future_schema', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '2026-01-01T00:00:00Z')", CurrentVersion()+1)},
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

func TestMigrateFTSIdentityUpgradeAndIndexedLookups(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path, db := openMigrationDB(t)
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:16]); err != nil {
		t.Fatal(err)
	}
	inspect := openInspectionDB(t, path)
	issueID := testID(90)
	insertIssue(t, inspect, issueID, 1, "ready", nil)
	if _, err := inspect.Exec(`INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, 'upgrade comment', ?)`, testID(91), issueID, nowText()); err != nil {
		t.Fatal(err)
	}
	var oldRowid int64
	if err := inspect.QueryRow("SELECT rowid FROM search_index WHERE entity_type='issue' AND entity_id=?", issueID).Scan(&oldRowid); err != nil {
		t.Fatal(err)
	}
	result, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime))
	if err != nil || result != (Result{Version: CurrentVersion(), Applied: 3}) {
		t.Fatalf("upgrade = %+v, %v", result, err)
	}
	var count, mismatch int
	var mappedRowid int64
	if err := inspect.QueryRow("SELECT count(*) FROM search_index_identity").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := inspect.QueryRow(`SELECT count(*) FROM search_index LEFT JOIN search_index_identity AS identity ON identity.fts_rowid=search_index.rowid
		WHERE identity.fts_rowid IS NULL OR identity.entity_type IS NOT search_index.entity_type OR identity.entity_id IS NOT search_index.entity_id OR identity.issue_id IS NOT search_index.issue_id`).Scan(&mismatch); err != nil {
		t.Fatal(err)
	}
	if err := inspect.QueryRow("SELECT fts_rowid FROM search_index_identity WHERE entity_type='issue' AND entity_id=?", issueID).Scan(&mappedRowid); err != nil {
		t.Fatal(err)
	}
	if count != 2 || mismatch != 0 || mappedRowid != oldRowid {
		t.Fatalf("backfill count/mismatch/rowid = %d/%d/%d, want 2/0/%d", count, mismatch, mappedRowid, oldRowid)
	}
	if _, err := inspect.Exec(`INSERT INTO search_index_identity(entity_type, entity_id, issue_id) VALUES ('issue', ?, ?)`, issueID, issueID); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	for _, statement := range []string{
		`SELECT fts_rowid FROM search_index_identity WHERE entity_type='issue' AND entity_id=?`,
		`SELECT fts_rowid FROM search_index_identity WHERE issue_id=? AND entity_type='review'`,
	} {
		var selectID, parent, unused int
		var detail string
		if err := inspect.QueryRow("EXPLAIN QUERY PLAN "+statement, issueID).Scan(&selectID, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail, "SEARCH search_index_identity") || !strings.Contains(detail, "INDEX") {
			t.Fatalf("identity lookup does not seek an index: %s", detail)
		}
	}
	var integrity string
	if err := inspect.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity = %q, %v", integrity, err)
	}
}

func TestMigrateFTSIdentityFailureRollsBackSchemaAndHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path, db := openMigrationDB(t)
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:16]); err != nil {
		t.Fatal(err)
	}
	catalog := append([]migration(nil), embeddedCatalog...)
	catalog[16] = testMigration(17, "fts_identity", ftsIdentitySQL+"\nINSERT INTO missing_fts_migration_table VALUES (1);")
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), catalog); err == nil {
		t.Fatal("invalid migration succeeded")
	}
	inspect := openInspectionDB(t, path)
	var version, tables int
	if err := inspect.QueryRow("SELECT max(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := inspect.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='search_index_identity'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if version != 16 || tables != 0 {
		t.Fatalf("failed upgrade left version/table = %d/%d, want 16/0", version, tables)
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

// Regression for migration 007 on databases that already hold events: the
// append-only triggers on issue_events/review_events abort any UPDATE, so the
// timestamp rewrite must lift and restore them. Empty event tables hide this.
func TestFixedWidthTimestampMigrationRewritesPopulatedEventTables(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	ctx := context.Background()
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:6]); err != nil {
		t.Fatalf("seed migrations through 006: %v", err)
	}

	issueID := testID(20)
	targetID := testID(21)
	reviewID := testID(22)
	const wholeSecond = "2026-07-13T08:11:12Z"
	const shortFraction = "2026-07-13T08:11:12.5Z"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'issue with events', 'ready', 'medium', 1, ?, ?)`, issueID, wholeSecond, wholeSecond); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, payload, created_at)
			VALUES (?, 'issue_created', '{}', ?)`, issueID, wholeSecond); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(
			id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at
		) VALUES (?, ?, 1, 1, '[]', 1, ?)`, targetID, issueID, shortFraction); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(
			id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status, supersedes_id,
			active_attempt_id, version, created_at, resolved_at
		) VALUES (?, ?, ?, 1, 1, '[]', 'open', NULL, NULL, 1, ?, NULL)`, reviewID, targetID, issueID, shortFraction); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO review_events(request_id, target_id, event_type, payload, created_at)
			VALUES (?, ?, 'review_requested', '{}', ?)`, reviewID, targetID, shortFraction)
		return err
	}); err != nil {
		t.Fatalf("seed populated event tables: %v", err)
	}

	if _, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime)); err != nil {
		t.Fatalf("Migrate() over populated event tables: %v", err)
	}

	inspect := openInspectionDB(t, path)
	var issueEventAt, reviewEventAt string
	if err := inspect.QueryRow(`SELECT created_at FROM issue_events WHERE issue_id = ? AND source = 'issue'`, issueID).Scan(&issueEventAt); err != nil {
		t.Fatalf("read migrated issue event: %v", err)
	}
	if err := inspect.QueryRow(`SELECT created_at FROM issue_events WHERE issue_id = ? AND source = 'review'`, issueID).Scan(&reviewEventAt); err != nil {
		t.Fatalf("read migrated review event: %v", err)
	}
	if issueEventAt != "2026-07-13T08:11:12.000000000Z" {
		t.Fatalf("issue event created_at = %q, want fixed-width rewrite", issueEventAt)
	}
	if reviewEventAt != "2026-07-13T08:11:12.500000000Z" {
		t.Fatalf("review event created_at = %q, want fixed-width rewrite", reviewEventAt)
	}
	// The append-only guard must be back in place after the rewrite.
	if _, err := inspect.Exec(`UPDATE issue_events SET payload = '{"tampered":true}'`); err == nil {
		t.Fatal("issue_events UPDATE succeeded; append-only trigger was not restored")
	}
}

func TestMigrateReviewTargetSnapshotPreservesHistoryAndActiveUniqueness(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	ctx := context.Background()
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:15]); err != nil {
		t.Fatal(err)
	}
	issueID, targetID, requestID := testID(80), testID(81), testID(82)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
			VALUES (?, 1, 'task', 'snapshot history', 'review', 'medium', 1, ?, ?)`, issueID, nowText(), nowText()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at)
			VALUES (?, ?, 1, 0, '[]', '["implementation"]', 1, ?)`, targetID, issueID, nowText()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_target_gate_snapshots(target_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at)
			VALUES (?, '[]', '[]', ?, 1, ?)`, targetID, strings.Repeat("0", 64), nowText()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, version, created_at)
			VALUES (?, ?, ?, 1, 0, '[]', '["implementation"]', 'open', 1, ?)`, requestID, targetID, issueID, nowText())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inspect := openInspectionDB(t, path)
	var priorChecksums string
	if err := inspect.QueryRow(`SELECT group_concat(checksum, ',') FROM (SELECT checksum FROM schema_migrations ORDER BY version)`).Scan(&priorChecksums); err != nil {
		t.Fatal(err)
	}
	result, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:16])
	if err != nil || result != (Result{Version: 16, Applied: 1}) {
		t.Fatalf("upgrade = %+v, %v", result, err)
	}
	var checksums, status, fingerprint, purposes, requirements string
	var position int64
	if err := inspect.QueryRow(`SELECT group_concat(checksum, ',') FROM (SELECT checksum FROM schema_migrations WHERE version <= 15 ORDER BY version)`).Scan(&checksums); err != nil {
		t.Fatal(err)
	}
	if err := inspect.QueryRow(`SELECT request.status, target.latest_event_id, target.purposes_json, snapshot.requirements_json, snapshot.fingerprint
		FROM review_requests AS request JOIN review_targets AS target ON target.id = request.target_id
		JOIN review_target_gate_snapshots AS snapshot ON snapshot.target_id = target.id WHERE request.id = ?`, requestID).
		Scan(&status, &position, &purposes, &requirements, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if checksums != priorChecksums || status != "open" || position != 0 || purposes != `["implementation"]` || requirements != "[]" || fingerprint != strings.Repeat("0", 64) {
		t.Fatalf("migration changed history: %q %q %d %q %q %q", checksums, status, position, purposes, requirements, fingerprint)
	}
	var targetIndexUnique, requestIndexUnique, requestIndexPartial int
	if err := inspect.QueryRow(`SELECT "unique" FROM pragma_index_list('review_targets') WHERE name = 'idx_review_targets_issue_version'`).Scan(&targetIndexUnique); err != nil {
		t.Fatal(err)
	}
	if err := inspect.QueryRow(`SELECT "unique", partial FROM pragma_index_list('review_requests') WHERE name = 'idx_review_requests_active_issue_version'`).Scan(&requestIndexUnique, &requestIndexPartial); err != nil {
		t.Fatal(err)
	}
	if targetIndexUnique != 0 || requestIndexUnique != 1 || requestIndexPartial != 1 {
		t.Fatalf("index uniqueness/partial = %d/%d/%d", targetIndexUnique, requestIndexUnique, requestIndexPartial)
	}
	secondTargetID, secondRequestID := testID(83), testID(84)
	if _, err := inspect.Exec(`INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at)
		VALUES (?, ?, 1, 1, '[]', '["implementation"]', 1, ?)`, secondTargetID, issueID, nowText()); err != nil {
		t.Fatalf("fresh same-version target rejected: %v", err)
	}
	assertSQLFails(t, inspect, `INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, version, created_at)
		VALUES (?, ?, ?, 1, 1, '[]', '["implementation"]', 'open', 1, ?)`, secondRequestID, secondTargetID, issueID, nowText())
	if _, err := inspect.Exec(`INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, version, created_at, resolved_at)
		VALUES (?, ?, ?, 1, 1, '[]', '["implementation"]', 'cancelled', 1, ?, ?)`, secondRequestID, secondTargetID, issueID, nowText(), nowText()); err != nil {
		t.Fatalf("resolved same-version request rejected: %v", err)
	}
	assertSQLFails(t, inspect, `UPDATE review_requests SET status = 'open', resolved_at = NULL WHERE id = ?`, secondRequestID)
	assertSQLFails(t, inspect, `UPDATE review_target_gate_snapshots SET fingerprint = ? WHERE target_id = ?`, strings.Repeat("1", 64), targetID)
	var integrity string
	var foreignKeyViolations int
	if err := inspect.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if err := inspect.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" || foreignKeyViolations != 0 {
		t.Fatalf("upgraded integrity = %q, FK violations = %d", integrity, foreignKeyViolations)
	}
	if result, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:16]); err != nil || result.Applied != 0 {
		t.Fatalf("migration replay = %+v, %v", result, err)
	}
}

// Regression for migration 012 on databases that already hold review rows:
// pre-ISSUE-173 targets and requests must survive the upgrade covering the
// implementation purpose, and every target must end up with a frozen review
// gate snapshot so approving an in-flight request stays an unconditional
// snapshot read.
func TestReviewPurposeMigrationBackfillsExistingReviewRows(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	ctx := context.Background()
	if _, err := run(ctx, db, clock.NewFakeClock(migrationTime), embeddedCatalog[:11]); err != nil {
		t.Fatalf("seed migrations through 011: %v", err)
	}

	issueID := testID(40)
	targetID := testID(41)
	requestID := testID(42)
	const targetCreatedAt = "2026-07-13T08:11:12.500000000Z"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, status, priority, version, created_at, updated_at
		) VALUES (?, 1, 'task', 'reviewed issue', 'review', 'medium', 3, ?, ?)`, issueID, nowText(), nowText()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(
			id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at
		) VALUES (?, ?, 3, 7, '[]', 1, ?)`, targetID, issueID, targetCreatedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO review_requests(
			id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status,
			supersedes_id, active_attempt_id, version, created_at, resolved_at
		) VALUES (?, ?, ?, 3, 7, '[]', 'open', NULL, NULL, 1, ?, NULL)`, requestID, targetID, issueID, nowText())
		return err
	}); err != nil {
		t.Fatalf("seed pre-upgrade review rows: %v", err)
	}

	if _, err := Migrate(ctx, db, clock.NewFakeClock(migrationTime)); err != nil {
		t.Fatalf("Migrate() over existing review rows: %v", err)
	}

	inspect := openInspectionDB(t, path)
	var targetPurposes, requestPurposes string
	if err := inspect.QueryRow("SELECT purposes_json FROM review_targets WHERE id = ?", targetID).Scan(&targetPurposes); err != nil {
		t.Fatalf("read migrated target purposes: %v", err)
	}
	if err := inspect.QueryRow("SELECT purposes_json FROM review_requests WHERE id = ?", requestID).Scan(&requestPurposes); err != nil {
		t.Fatalf("read migrated request purposes: %v", err)
	}
	if targetPurposes != `["implementation"]` || requestPurposes != `["implementation"]` {
		t.Fatalf("purposes = %q/%q, want the implementation default on both projections", targetPurposes, requestPurposes)
	}

	var requirements, sourcePolicies, fingerprint, snapshotCreatedAt string
	var snapshotIssueVersion int
	if err := inspect.QueryRow(`SELECT requirements_json, source_policies_json, fingerprint, issue_version, created_at
		FROM review_target_gate_snapshots WHERE target_id = ?`, targetID).Scan(
		&requirements, &sourcePolicies, &fingerprint, &snapshotIssueVersion, &snapshotCreatedAt); err != nil {
		t.Fatalf("read backfilled target snapshot: %v", err)
	}
	if requirements != "[]" || sourcePolicies != "[]" {
		t.Fatalf("backfilled snapshot = %s/%s, want an empty requirement set", requirements, sourcePolicies)
	}
	if fingerprint != strings.Repeat("0", 64) {
		t.Fatalf("backfilled fingerprint = %q, want the legacy sentinel", fingerprint)
	}
	if snapshotIssueVersion != 3 || snapshotCreatedAt != targetCreatedAt {
		t.Fatalf("backfilled snapshot = (%d, %q), want the target's own version and creation time", snapshotIssueVersion, snapshotCreatedAt)
	}

	// The backfill must not disturb the table's immutability guarantee.
	assertSQLFails(t, inspect, "UPDATE review_target_gate_snapshots SET fingerprint = ?", strings.Repeat("1", 64))
}

// Migration 012's constraints: purposes are a bounded JSON array, and an
// approval record is written once and never changed (docs/02 §17.5).
func TestReviewApprovalConstraintsAndImmutability(t *testing.T) {
	t.Parallel()
	path, db := openMigrationDB(t)
	if _, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime)); err != nil {
		t.Fatal(err)
	}
	inspect := openInspectionDB(t, path)

	issueID := testID(50)
	targetID := testID(51)
	requestID := testID(52)
	attemptID := testID(53)
	approvalID := testID(54)
	insertIssue(t, inspect, issueID, 1, "review", nil)
	insertActiveAttempt(t, inspect, attemptID, issueID)
	if _, err := inspect.Exec(`INSERT INTO review_targets(
		id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at
	) VALUES (?, ?, 1, 5, '[]', 1, ?)`, targetID, issueID, nowText()); err != nil {
		t.Fatalf("insert review target: %v", err)
	}
	if _, err := inspect.Exec(`INSERT INTO review_requests(
		id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status,
		supersedes_id, active_attempt_id, version, created_at, resolved_at, purposes_json
	) VALUES (?, ?, ?, 1, 5, '[]', 'approved', NULL, NULL, 1, ?, ?, '["implementation","security"]')`,
		requestID, targetID, issueID, nowText(), nowText()); err != nil {
		t.Fatalf("insert review request: %v", err)
	}

	// Purposes must be a JSON array of one to ten entries.
	assertSQLFails(t, inspect, `UPDATE review_requests SET purposes_json = '"security"' WHERE id = ?`, requestID)
	assertSQLFails(t, inspect, `UPDATE review_requests SET purposes_json = '[]' WHERE id = ?`, requestID)
	assertSQLFails(t, inspect, `UPDATE review_requests SET purposes_json = '["p1","p2","p3","p4","p5","p6","p7","p8","p9","p10","p11"]' WHERE id = ?`, requestID)

	insertApproval := `INSERT INTO review_approvals(
		id, issue_id, target_id, request_id, attempt_id, purpose, target_issue_version, target_event_id, version, created_at
	) VALUES (?, ?, ?, ?, ?, ?, 1, 5, 1, ?)`
	if _, err := inspect.Exec(insertApproval, approvalID, issueID, targetID, requestID, attemptID, "security", nowText()); err != nil {
		t.Fatalf("insert review approval: %v", err)
	}
	// One purpose per request, granted once.
	assertSQLFails(t, inspect, insertApproval, testID(55), issueID, targetID, requestID, attemptID, "security", nowText())
	assertSQLFails(t, inspect, insertApproval, testID(56), issueID, targetID, requestID, attemptID, "  ", nowText())
	// A second purpose from the same request is a separate record.
	if _, err := inspect.Exec(insertApproval, testID(57), issueID, targetID, requestID, attemptID, "implementation", nowText()); err != nil {
		t.Fatalf("insert second purpose approval: %v", err)
	}

	assertSQLFails(t, inspect, "UPDATE review_approvals SET purpose = 'design' WHERE id = ?", approvalID)
	assertSQLFails(t, inspect, "DELETE FROM review_approvals WHERE id = ?", approvalID)
}
