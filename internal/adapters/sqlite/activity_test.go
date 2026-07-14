package sqlite_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"

	_ "modernc.org/sqlite"
)

const activityTestProjectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type activityTestCursor struct {
	OccurredAt string `json:"occurred_at"`
	TypeRank   int    `json:"type_rank"`
	SortID     string `json:"sort_id"`
}

func TestNewActivityRepositoryRejectsNilDatabase(t *testing.T) {
	_, err := sqlite.NewActivityRepository(nil)
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
}

func TestActivityRepositoryReturnsUnifiedIssueActivity(t *testing.T) {
	db, _, issue, now := newActivityTestFixture(t)
	repository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedActivityFixture(t, db, issue.ID, now); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issue.ID, Types: nil, Limit: 20}})
	if err != nil {
		t.Fatalf("GetIssueActivity() error = %v", err)
	}
	if result.Items == nil {
		t.Fatal("result.Items is nil")
	}
	if len(result.Items) != 7 {
		t.Fatalf("len(items) = %d, want 7", len(result.Items))
	}
	if result.HasMore || result.NextCursor != nil {
		t.Fatalf("unexpected pagination state: has_more=%v next=%v", result.HasMore, result.NextCursor)
	}

	want := []domain.ActivityEntityType{domain.ActivityEntityTypeComment, domain.ActivityEntityTypeDecision, domain.ActivityEntityTypeAttempt, domain.ActivityEntityTypeAttemptNote, domain.ActivityEntityTypeEvent, domain.ActivityEntityTypeEvent, domain.ActivityEntityTypeArtifact}
	if got := activityEntityTypes(result.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("entity types = %v, want %v", got, want)
	}
	for _, item := range result.Items {
		if item.IssueID != issue.ID {
			t.Fatalf("item issue id = %q, want %q", item.IssueID, issue.ID)
		}
		if item.OccurredAt.Location() != time.UTC {
			t.Fatalf("occurred_at location = %v, want UTC", item.OccurredAt.Location())
		}
		if !item.OccurredAt.Equal(now) {
			t.Fatalf("occurred_at = %v, want %v", item.OccurredAt, now)
		}
	}
	if result.Items[0].Comment == nil || result.Items[0].Comment.ID != "01ARZ3NDEKTSV4RRFFQ69G5FA2" {
		t.Fatalf("comment item = %#v", result.Items[0])
	}
	if result.Items[1].Decision == nil || result.Items[1].Decision.ID != "01ARZ3NDEKTSV4RRFFQ69G5FA3" {
		t.Fatalf("decision item = %#v", result.Items[1])
	}
	if result.Items[2].Attempt == nil || result.Items[2].Attempt.ID != "01ARZ3NDEKTSV4RRFFQ69G5FA4" {
		t.Fatalf("attempt item = %#v", result.Items[2])
	}
	if result.Items[3].AttemptNote == nil || result.Items[3].AttemptNote.ID != "01ARZ3NDEKTSV4RRFFQ69G5FA5" {
		t.Fatalf("attempt note item = %#v", result.Items[3])
	}
	if result.Items[4].Event == nil || result.Items[4].Event.EventType != "issue_created" {
		t.Fatalf("first event item = %#v", result.Items[4])
	}
	if result.Items[5].Event == nil || result.Items[5].Event.EventType != "activity_event" {
		t.Fatalf("second event item = %#v", result.Items[5])
	}
	if result.Items[6].Artifact == nil || result.Items[6].Artifact.ID != "01ARZ3NDEKTSV4RRFFQ69G5FA6" {
		t.Fatalf("artifact item = %#v", result.Items[6])
	}
	if _, ok := reflect.TypeOf(result.Items[2].Attempt).Elem().FieldByName("LeaseToken"); ok {
		t.Fatalf("attempt type unexpectedly has LeaseToken field")
	}
}

func TestActivityRepositoryFiltersAndSkipsOutOfScopeRows(t *testing.T) {
	db, _, issue, now := newActivityTestFixture(t)
	repository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedActivityFixture(t, db, issue.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_by_session_id, created_at) VALUES (?, NULL, 'global', 'global', 'global', 'active', NULL, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FBB", now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (NULL, 'global_event', '{}', ?)`, now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	for _, category := range []domain.ActivityCategory{domain.ActivityCategoryComments, domain.ActivityCategoryDecisions, domain.ActivityCategoryAttempts, domain.ActivityCategoryAttemptNotes, domain.ActivityCategoryEvents, domain.ActivityCategoryArtifacts} {
		result, err := repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issue.ID, Types: []domain.ActivityCategory{category}, Limit: 20}})
		if err != nil {
			t.Fatalf("category %s error = %v", category, err)
		}
		expectedCount := 1
		if category == domain.ActivityCategoryEvents {
			expectedCount = 2
		}
		if len(result.Items) != expectedCount {
			t.Fatalf("category %s len(items) = %d, want %d", category, len(result.Items), expectedCount)
		}
		if got := result.Items[0].EntityType; got != expectedActivityEntityTypeByCategory(category) {
			t.Fatalf("category %s entity type = %s, want %s", category, got, expectedActivityEntityTypeByCategory(category))
		}
		if category == domain.ActivityCategoryEvents {
			if got := []string{result.Items[0].Event.EventType, result.Items[1].Event.EventType}; !reflect.DeepEqual(got, []string{"issue_created", "activity_event"}) {
				t.Fatalf("event types = %v, want %v", got, []string{"issue_created", "activity_event"})
			}
		}
	}

	result, err := repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issue.ID, Types: nil, Limit: 20}})
	if err != nil {
		t.Fatalf("GetIssueActivity() default error = %v", err)
	}
	if len(result.Items) != 7 {
		t.Fatalf("default len(items) = %d, want 7", len(result.Items))
	}
	for _, item := range result.Items {
		if item.EntityType == domain.ActivityEntityTypeDecision && item.Decision != nil && item.Decision.ID == "01ARZ3NDEKTSV4RRFFQ69G5FBB" {
			t.Fatalf("global decision leaked into page: %#v", item)
		}
		if item.EntityType == domain.ActivityEntityTypeEvent && item.Event != nil && item.Event.EventType == "global_event" {
			t.Fatalf("global event leaked into page: %#v", item)
		}
	}
}

func TestActivityRepositoryResolvesIssueIdentifiers(t *testing.T) {
	db, _, issue, now := newActivityTestFixture(t)
	repository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedActivityFixture(t, db, issue.ID, now); err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []string{issue.ID, issue.DisplayID} {
		result, err := repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: identifier, Limit: 20}})
		if err != nil {
			t.Fatalf("GetIssueActivity(%q) error = %v", identifier, err)
		}
		if len(result.Items) != 7 {
			t.Fatalf("GetIssueActivity(%q) len(items) = %d, want 7", identifier, len(result.Items))
		}
	}
	_, err = repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: "ISSUE-999", Limit: 20}})
	assertDomainCode(t, err, domain.CodeIssueNotFound)
}

func TestActivityRepositoryPaginationTraversesEveryItemOnce(t *testing.T) {
	db, _, issue, now := newActivityTestFixture(t)
	repository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedActivityFixture(t, db, issue.ID, now); err != nil {
		t.Fatal(err)
	}
	var seenEntityIDs []string
	var seenEntityTypes []domain.ActivityEntityType
	var cursor *string
	for page := 0; page < 7; page++ {
		result, err := repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issue.ID, Limit: 1, Cursor: valueOrEmpty(cursor)}})
		if err != nil {
			t.Fatalf("page %d error = %v", page, err)
		}
		if len(result.Items) != 1 {
			t.Fatalf("page %d len(items) = %d, want 1", page, len(result.Items))
		}
		seenEntityIDs = append(seenEntityIDs, result.Items[0].EntityID)
		seenEntityTypes = append(seenEntityTypes, result.Items[0].EntityType)
		if page < 6 {
			if result.NextCursor == nil {
				t.Fatalf("page %d missing next cursor", page)
			}
			cursor = result.NextCursor
		} else if result.NextCursor != nil {
			t.Fatalf("page %d unexpected next cursor %v", page, result.NextCursor)
		}
	}
	wantEntityIDs := []string{"01ARZ3NDEKTSV4RRFFQ69G5FA2", "01ARZ3NDEKTSV4RRFFQ69G5FA3", "01ARZ3NDEKTSV4RRFFQ69G5FA4", "01ARZ3NDEKTSV4RRFFQ69G5FA5", "1", "2", "01ARZ3NDEKTSV4RRFFQ69G5FA6"}
	wantEntityTypes := []domain.ActivityEntityType{domain.ActivityEntityTypeComment, domain.ActivityEntityTypeDecision, domain.ActivityEntityTypeAttempt, domain.ActivityEntityTypeAttemptNote, domain.ActivityEntityTypeEvent, domain.ActivityEntityTypeEvent, domain.ActivityEntityTypeArtifact}
	if !reflect.DeepEqual(seenEntityIDs, wantEntityIDs) {
		t.Fatalf("seen IDs = %v, want %v", seenEntityIDs, wantEntityIDs)
	}
	if !reflect.DeepEqual(seenEntityTypes, wantEntityTypes) {
		t.Fatalf("seen types = %v, want %v", seenEntityTypes, wantEntityTypes)
	}
}

func TestActivityRepositoryCursorValidation(t *testing.T) {
	db, _, issue, now := newActivityTestFixture(t)
	repository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedActivityFixture(t, db, issue.ID, now); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		cursor    string
		wantCode  string
		wantField string
	}{
		{name: "malformed", cursor: "not-base64", wantCode: "MALFORMED_CURSOR", wantField: "cursor"},
		{name: "oversize", cursor: strings.Repeat("a", 4097), wantCode: "CURSOR_TOO_LARGE", wantField: "cursor"},
		{name: "unsupported version", cursor: encodeCursor(t, activityTestCursor{OccurredAt: now.Format(time.RFC3339Nano), TypeRank: 1, SortID: "01ARZ3NDEKTSV4RRFFQ69G5FA2"}, 2), wantCode: "UNSUPPORTED_CURSOR_VERSION", wantField: "cursor"},
		{name: "invalid payload", cursor: encodeCursor(t, activityTestCursor{OccurredAt: now.Format(time.RFC3339Nano), TypeRank: 0, SortID: "01ARZ3NDEKTSV4RRFFQ69G5FA2"}, 1), wantCode: "MALFORMED_CURSOR", wantField: "cursor"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issue.ID, Limit: 20, Cursor: test.cursor}})
			if err == nil {
				t.Fatalf("expected error")
			}
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("error = %v, want *domain.Error", err)
			}
			if domainErr.Code != domain.CodeInvalidArgument {
				t.Fatalf("code = %s, want %s", domainErr.Code, domain.CodeInvalidArgument)
			}
			if len(domainErr.Details) != 1 || domainErr.Details[0].Field != test.wantField || domainErr.Details[0].Code != test.wantCode {
				t.Fatalf("details = %+v, want field=%s code=%s", domainErr.Details, test.wantField, test.wantCode)
			}
		})
	}
}

func TestActivityRepositoryReturnsStorageCorruptForInvalidRows(t *testing.T) {
	db, dbPath, issue, now := newActivityTestFixture(t)
	repository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedActivityFixture(t, db, issue.ID, now); err != nil {
		t.Fatal(err)
	}

	rawConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := rawConn.Conn(context.Background())
	if err != nil {
		rawConn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		conn.Close()
		rawConn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		conn.Close()
		rawConn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (?, 'activity_event', '{bad json', ?)`, issue.ID, now.Format(time.RFC3339Nano)); err != nil {
		conn.Close()
		rawConn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		rawConn.Close()
		t.Fatal(err)
	}
	if err := rawConn.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issue.ID, Types: []domain.ActivityCategory{domain.ActivityCategoryEvents}, Limit: 20}})
	assertDomainCode(t, err, domain.CodeStorageCorrupt)

	rawConn, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err = rawConn.Conn(context.Background())
	if err != nil {
		rawConn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		conn.Close()
		rawConn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		conn.Close()
		rawConn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `UPDATE work_attempts SET status = 'invalid_status' WHERE id = ?`, "01ARZ3NDEKTSV4RRFFQ69G5FA4"); err != nil {
		conn.Close()
		rawConn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		rawConn.Close()
		t.Fatal(err)
	}
	if err := rawConn.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = repository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issue.ID, Types: []domain.ActivityCategory{domain.ActivityCategoryAttempts}, Limit: 20}})
	assertDomainCode(t, err, domain.CodeStorageCorrupt)
}

func TestActivityRepositoryPersistsAcrossCloseReopenAndSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "activity-persist.db")
	db := openTestDB(t, dbPath, false)
	now := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.FixedZone("test", 2*60*60)).UTC()
	fakeClock := clock.NewFakeClock(now)
	if _, err := migrations.Migrate(context.Background(), db, fakeClock); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`, activityTestProjectID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewIssueService(repository, fakeClock, generator)
	if err != nil {
		t.Fatal(err)
	}
	issueResult, err := service.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "activity"})
	if err != nil {
		t.Fatal(err)
	}
	if err := seedActivityFixture(t, db, issueResult.Issue.ID, now); err != nil {
		t.Fatal(err)
	}
	activityRepository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issueActivity, err := activityRepository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issueResult.Issue.ID, Limit: 20}})
	if err != nil {
		t.Fatalf("initial query error = %v", err)
	}
	if len(issueActivity.Items) != 7 {
		t.Fatalf("initial len(items) = %d, want 7", len(issueActivity.Items))
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(context.Background(), dbPath, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	reopenedRepository, err := sqlite.NewActivityRepository(reopened)
	if err != nil {
		t.Fatal(err)
	}
	issueActivity, err = reopenedRepository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issueResult.Issue.ID, Limit: 20}})
	if err != nil {
		t.Fatalf("reopened query error = %v", err)
	}
	if len(issueActivity.Items) != 7 {
		t.Fatalf("reopened len(items) = %d, want 7", len(issueActivity.Items))
	}

	writerConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Close()
	if _, err := writerConn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := writerConn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := writerConn.ExecContext(context.Background(), `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, ?, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FBC", issueResult.Issue.ID, "uncommitted", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	issueActivity, err = reopenedRepository.GetIssueActivity(context.Background(), ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: issueResult.Issue.ID, Limit: 20}})
	if err != nil {
		t.Fatalf("snapshot query error = %v", err)
	}
	if len(issueActivity.Items) != 7 {
		t.Fatalf("snapshot len(items) = %d, want 7", len(issueActivity.Items))
	}
	if _, err := writerConn.ExecContext(context.Background(), `COMMIT`); err != nil {
		t.Fatal(err)
	}
}

func seedActivityFixture(t *testing.T, db *sqlite.DB, issueID string, now time.Time) error {
	t.Helper()
	timestamp := now.Format(time.RFC3339Nano)
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, ?, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FA2", issueID, "first", timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES (?, ?, 'decision', 'decision', 'decision', 'active', ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FA3", issueID, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary) VALUES (?, ?, 'work', 'completed', 1, 0, ?, ?, ?, ?, ?, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FA4", issueID, []byte{1, 2, 3}, timestamp, timestamp, timestamp, timestamp, "done", "finished"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(id, attempt_id, kind, content, important, created_at) VALUES (?, ?, 'progress', 'note', 1, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FA5", "01ARZ3NDEKTSV4RRFFQ69G5FA4", timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (?, 'activity_event', ?, ?)`, issueID, `{"kind":"event"}`, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id, issue_id, attempt_id, type, uri, title, metadata, created_at) VALUES (?, ?, ?, 'file', 'artifact.txt', 'title', ?, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FA6", issueID, "01ARZ3NDEKTSV4RRFFQ69G5FA4", `{"name":"artifact"}`, timestamp)
		return err
	})
}

func newActivityTestFixture(t *testing.T) (*sqlite.DB, string, domain.Issue, time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.FixedZone("test", 2*60*60)).UTC()
	dbPath := filepath.Join(t.TempDir(), "activity.db")
	db := openTestDB(t, dbPath, true)
	fakeClock := clock.NewFakeClock(now)
	if _, err := migrations.Migrate(context.Background(), db, fakeClock); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`, activityTestProjectID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	repository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatalf("sqlite.NewIssueRepository() error = %v", err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	service, err := application.NewIssueService(repository, fakeClock, generator)
	if err != nil {
		t.Fatalf("NewIssueService() error = %v", err)
	}
	result, err := service.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "activity"})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	return db, dbPath, result.Issue, now
}

func openTestDB(t *testing.T, path string, addCleanup bool) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if addCleanup {
		t.Cleanup(func() {
			if err := db.Close(context.Background()); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		})
	}
	return db
}

func activityEntityTypes(items []domain.ActivityItem) []domain.ActivityEntityType {
	result := make([]domain.ActivityEntityType, len(items))
	for index, item := range items {
		result[index] = item.EntityType
	}
	return result
}

func expectedActivityEntityTypeByCategory(category domain.ActivityCategory) domain.ActivityEntityType {
	switch category {
	case domain.ActivityCategoryComments:
		return domain.ActivityEntityTypeComment
	case domain.ActivityCategoryDecisions:
		return domain.ActivityEntityTypeDecision
	case domain.ActivityCategoryAttempts:
		return domain.ActivityEntityTypeAttempt
	case domain.ActivityCategoryAttemptNotes:
		return domain.ActivityEntityTypeAttemptNote
	case domain.ActivityCategoryEvents:
		return domain.ActivityEntityTypeEvent
	case domain.ActivityCategoryArtifacts:
		return domain.ActivityEntityTypeArtifact
	default:
		return ""
	}
}

func encodeCursor(t *testing.T, payload activityTestCursor, version int) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Version int                `json:"version"`
		Payload activityTestCursor `json:"payload"`
	}{Version: version, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
