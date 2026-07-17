package sqlite_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
)

const sqliteTestProjectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestIssueCreationPersistsProjectionEventAndCounter(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	description := "Detailed description"
	criteria := "Acceptance criteria"
	epic, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type:               domain.TypeEpic,
		Title:              "Platform work",
		Description:        &description,
		AcceptanceCriteria: &criteria,
		Priority:           domain.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}

	reason := "waiting on dependency"
	parentDisplayID := strings.ToLower(epic.DisplayID)
	task, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type:               domain.TypeTask,
		Title:              "Implement slice",
		Description:        &description,
		AcceptanceCriteria: &criteria,
		Status:             domain.StatusBlocked,
		BlockedReason:      &reason,
		ParentID:           &parentDisplayID,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	bug, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeBug, Title: "Fix regression"})
	if err != nil {
		t.Fatalf("create bug: %v", err)
	}
	if epic.DisplayID != "ISSUE-1" || task.DisplayID != "ISSUE-2" || bug.DisplayID != "ISSUE-3" {
		t.Fatalf("display IDs = %q, %q, %q", epic.DisplayID, task.DisplayID, bug.DisplayID)
	}
	if task.SequenceNo != 2 || task.Issue.Version != 1 || !task.Issue.CreatedAt.Equal(now) || !task.Issue.UpdatedAt.Equal(now) {
		t.Fatalf("task result = %#v", task)
	}

	var stored struct {
		id, issueType, title, status, priority, parentID, blockedReason, createdAt, updatedAt string
		description, criteria                                                                 sql.NullString
		sequenceNo, version                                                                   int64
		createdBySessionID, closedAt, archivedAt, archivedBySessionID                         any
	}
	var event struct {
		eventType, payload, createdAt string
		sessionID, attemptID          any
	}
	var nextNumber int64
	err = db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT id, sequence_no, type, title, description, acceptance_criteria,
			status, priority, parent_id, blocked_reason, version, created_by_session_id, created_at, updated_at,
			closed_at, archived_at, archived_by_session_id
			FROM issues WHERE id = ?`, task.ID).Scan(
			&stored.id, &stored.sequenceNo, &stored.issueType, &stored.title, &stored.description, &stored.criteria,
			&stored.status, &stored.priority, &stored.parentID, &stored.blockedReason, &stored.version, &stored.createdBySessionID,
			&stored.createdAt, &stored.updatedAt, &stored.closedAt, &stored.archivedAt, &stored.archivedBySessionID,
		); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT event_type, session_id, attempt_id, payload, created_at
			FROM issue_events WHERE issue_id = ? ORDER BY id`, task.ID).Scan(
			&event.eventType, &event.sessionID, &event.attemptID, &event.payload, &event.createdAt,
		); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&nextNumber)
	})
	if err != nil {
		t.Fatalf("read persisted issue: %v", err)
	}
	if stored.id != task.ID || stored.sequenceNo != 2 || stored.issueType != "task" || stored.title != "Implement slice" ||
		!stored.description.Valid || stored.description.String != description || !stored.criteria.Valid || stored.criteria.String != criteria ||
		stored.status != "blocked" || stored.priority != "medium" ||
		stored.parentID != epic.ID || stored.blockedReason != reason || stored.version != 1 ||
		stored.createdBySessionID != nil || stored.closedAt != nil || stored.archivedAt != nil || stored.archivedBySessionID != nil ||
		stored.createdAt != now.Format(time.RFC3339Nano) || stored.updatedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("stored issue = %#v", stored)
	}
	if event.eventType != "issue_created" || event.sessionID != nil || event.attemptID != nil || event.createdAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("event metadata = %#v", event)
	}
	const wantPayloadPrefix = `{"sequence_no":2,"type":"task","status":"blocked","priority":"medium","parent_id":`
	if event.payload != wantPayloadPrefix+fmt.Sprintf("%q}", epic.ID) {
		t.Fatalf("event payload = %s", event.payload)
	}
	if !json.Valid([]byte(event.payload)) || nextNumber != 4 {
		t.Fatalf("event JSON valid = %v, next issue number = %d", json.Valid([]byte(event.payload)), nextNumber)
	}
}

func TestIssueCreateIdempotencyReplayAndConflict(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	key := "issue-retry"
	input := domain.CreateIssueInput{Type: domain.TypeTask, Title: "Retry issue", IdempotencyKey: &key}
	first, err := service.CreateIssue(ctx, input)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := service.CreateIssue(ctx, input)
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if !reflect.DeepEqual(first.Issue, second.Issue) {
		t.Fatalf("replay mismatch: %#v != %#v", first.Issue, second.Issue)
	}
	changed := input
	changed.Title = "Changed title"
	if _, err := service.CreateIssue(ctx, changed); !errors.Is(err, &domain.Error{Code: domain.CodeIdempotencyConflict}) {
		t.Fatalf("conflict = %v", err)
	}
	var issues, events, records int64
	var nextIssueNumber int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'issue_created'").Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM idempotency_records WHERE operation = 'create_issue' AND idempotency_key = ?", key).Scan(&records); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&nextIssueNumber)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != 1 || events != 1 || records != 1 || nextIssueNumber != 2 {
		t.Fatalf("durable state = issues %d events %d records %d next_issue_number %d", issues, events, records, nextIssueNumber)
	}
}

func TestIssueReadByInternalAndDisplayIDMapsProjectionWithoutSideEffects(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	description := "read description"
	criteria := "read criteria"
	reason := "read blocker"
	epic, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeEpic, Title: "Read epic"})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	issueResult, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type:               domain.TypeTask,
		Title:              "Read issue",
		Description:        &description,
		AcceptanceCriteria: &criteria,
		Status:             domain.StatusBlocked,
		BlockedReason:      &reason,
		ParentID:           &epic.ID,
		Priority:           domain.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sessionID := "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	closedAt := now.Add(time.Hour)
	archivedAt := now.Add(2 * time.Hour)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		timestamp := now.Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(
			id, client_name, started_at, last_seen_at
		) VALUES (?, 'test', ?, ?)`, sessionID, timestamp, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE issues
			SET created_by_session_id = ?, closed_at = ?, archived_at = ?,
				archived_by_session_id = ?, updated_at = ?, version = 2
			WHERE id = ?`, sessionID, closedAt.Format(time.RFC3339Nano),
			archivedAt.Format(time.RFC3339Nano), sessionID, archivedAt.Format(time.RFC3339Nano),
			issueResult.ID)
		return err
	}); err != nil {
		t.Fatalf("seed issue metadata: %v", err)
	}

	var before struct {
		issues, events, nextIssueNumber int64
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&before.issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&before.events); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&before.nextIssueNumber)
	}); err != nil {
		t.Fatalf("read side-effect baseline: %v", err)
	}

	byID, err := service.GetIssue(ctx, issueResult.ID)
	if err != nil {
		t.Fatalf("GetIssue(internal ID): %v", err)
	}
	byDisplay, err := service.GetIssue(ctx, "issue-2")
	if err != nil {
		t.Fatalf("GetIssue(display ID): %v", err)
	}
	if !reflect.DeepEqual(byID, byDisplay) {
		t.Fatalf("lookup projections differ:\nby ID: %#v\nby display: %#v", byID, byDisplay)
	}
	if byID.DisplayID != "ISSUE-2" || byID.ID != issueResult.ID || byID.SequenceNo != 2 ||
		byID.Description == nil || *byID.Description != description ||
		byID.AcceptanceCriteria == nil || *byID.AcceptanceCriteria != criteria ||
		byID.ParentID == nil || *byID.ParentID != epic.ID ||
		byID.BlockedReason == nil || *byID.BlockedReason != reason ||
		byID.CreatedBySessionID == nil || *byID.CreatedBySessionID != sessionID ||
		byID.ClosedAt == nil || !byID.ClosedAt.Equal(closedAt) ||
		byID.ArchivedAt == nil || !byID.ArchivedAt.Equal(archivedAt) ||
		byID.ArchivedBySessionID == nil || *byID.ArchivedBySessionID != sessionID ||
		byID.Version != 2 {
		t.Fatalf("read projection = %#v", byID)
	}

	var after struct {
		issues, events, nextIssueNumber int64
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&after.issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&after.events); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&after.nextIssueNumber)
	}); err != nil {
		t.Fatalf("read side-effect result: %v", err)
	}
	if before != after {
		t.Fatalf("read changed database counts: before=%#v after=%#v", before, after)
	}
}

func TestIssueReadMapsNullProjectionFieldsAndMissingIssue(t *testing.T) {
	service, _, _ := openIssueService(t)
	ctx := context.Background()
	result, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeBug, Title: "Null fields"})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	issue, err := service.GetIssue(ctx, result.ID)
	if err != nil {
		t.Fatalf("GetIssue(): %v", err)
	}
	if issue.Description != nil || issue.AcceptanceCriteria != nil || issue.ParentID != nil ||
		issue.BlockedReason != nil || issue.CreatedBySessionID != nil ||
		issue.ClosedAt != nil || issue.ArchivedAt != nil || issue.ArchivedBySessionID != nil {
		t.Fatalf("null projection fields = %#v", issue)
	}

	_, err = service.GetIssue(ctx, "ISSUE-999")
	assertDomainCode(t, err, domain.CodeIssueNotFound)
	if strings.Contains(err.Error(), "no rows") || strings.Contains(err.Error(), "sqlite") {
		t.Fatalf("not-found error exposes storage text: %v", err)
	}
}

func TestIssueCreationPersistsAcrossDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.FixedZone("test", 2*60*60)).UTC()
	fakeClock := clock.NewFakeClock(now)
	path := filepath.Join(t.TempDir(), "issues.db")

	var db *sqlite.DB
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(context.Background()); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		}
	})

	var err error
	db, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		timestamp := now.Format(time.RFC3339Nano)
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at)
			VALUES (?, 1, ?, ?)`, sqliteTestProjectID, timestamp, timestamp)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	repository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatalf("NewIssueRepository() error = %v", err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	service, err := application.NewIssueService(repository, fakeClock, generator)
	if err != nil {
		t.Fatalf("NewIssueService() error = %v", err)
	}

	result, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type:     domain.TypeTask,
		Title:    "Persist through restart",
		Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	if err := db.Close(ctx); err != nil {
		db = nil
		t.Fatalf("Close() error = %v", err)
	}
	db = nil

	db, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	migrationResult, err := migrations.Migrate(ctx, db, fakeClock)
	if err != nil {
		t.Fatalf("Migrate() after reopen: %v", err)
	}
	if migrationResult.Version != migrations.CurrentVersion() || migrationResult.Applied != 0 {
		t.Fatalf("migration result after reopen = %#v", migrationResult)
	}

	var stored struct {
		id, issueType, title, status, priority, createdAt, updatedAt string
		sequenceNo, version                                          int64
	}
	var event struct {
		issueID, eventType, payload, createdAt string
	}
	var nextNumber int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT id, sequence_no, type, title, status, priority, version, created_at, updated_at
			FROM issues WHERE id = ?`, result.ID).Scan(
			&stored.id, &stored.sequenceNo, &stored.issueType, &stored.title, &stored.status, &stored.priority,
			&stored.version, &stored.createdAt, &stored.updatedAt,
		); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT issue_id, event_type, payload, created_at
			FROM issue_events WHERE issue_id = ? ORDER BY id`, result.ID).Scan(
			&event.issueID, &event.eventType, &event.payload, &event.createdAt,
		); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects WHERE id = ?", sqliteTestProjectID).Scan(&nextNumber)
	}); err != nil {
		t.Fatalf("read persisted rows after reopen: %v", err)
	}

	if stored.id != result.ID || stored.sequenceNo != 1 || stored.issueType != "task" ||
		stored.title != "Persist through restart" || stored.status != "open" || stored.priority != "high" ||
		stored.version != 1 || stored.createdAt != now.Format(time.RFC3339Nano) ||
		stored.updatedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("stored issue after reopen = %#v", stored)
	}
	if event.issueID != result.ID || event.eventType != "issue_created" ||
		event.payload != `{"sequence_no":1,"type":"task","status":"open","priority":"high"}` ||
		event.createdAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("stored event after reopen = %#v", event)
	}
	if nextNumber != 2 {
		t.Fatalf("next issue number after reopen = %d, want 2", nextNumber)
	}
}

func TestIssueCreationRejectsMissingWrongTypeAndArchivedParents(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	notFound := "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	invalidIdentifier := "not-an-issue-reference"
	_, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "invalid", ParentID: &invalidIdentifier})
	assertDomainCode(t, err, domain.CodeInvalidArgument)

	_, err = service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "missing", ParentID: &notFound})
	assertDomainCode(t, err, domain.CodeInvalidEpicParent)

	task, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "not epic"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeBug, Title: "wrong type", ParentID: &task.ID})
	assertDomainCode(t, err, domain.CodeInvalidEpicParent)

	epic, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeEpic, Title: "archived epic"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET archived_at = ? WHERE id = ?", now.Format(time.RFC3339Nano), epic.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "archived parent", ParentID: &epic.ID})
	assertDomainCode(t, err, domain.CodeInvalidEpicParent)

	_, err = service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeEpic, Title: "nested epic", ParentID: &task.ID,
	})
	assertDomainCode(t, err, domain.CodeInvalidEpicParent)

	var issues, events, nextNumber int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&events); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&nextNumber)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != 2 || events != 2 || nextNumber != 3 {
		t.Fatalf("after rejected parents: issues=%d events=%d next_issue_number=%d", issues, events, nextNumber)
	}
}

func TestIssueCreationRollsBackWhenEventInsertFails(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_issue_created_event
			BEFORE INSERT ON issue_events
			BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "must rollback"})
	assertDomainCode(t, err, domain.CodeStorageConstraint)

	var issues, events, nextNumber int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&events); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&nextNumber)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != 0 || events != 0 || nextNumber != 1 {
		t.Fatalf("after rollback: issues=%d events=%d next_issue_number=%d", issues, events, nextNumber)
	}
}

func TestIssueCreationRollsBackWhenIdempotencyInsertFails(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	key := "issue-idempotency-rollback"
	const triggerName = "test_fail_create_issue_idempotency_insert"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER `+triggerName+`
			BEFORE INSERT ON idempotency_records
			WHEN NEW.operation = 'create_issue'
			BEGIN
				SELECT RAISE(ABORT, 'forced create issue idempotency insert failure');
			END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `DROP TRIGGER `+triggerName)
			return err
		}); err != nil {
			t.Errorf("drop test trigger: %v", err)
		}
	}()

	_, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "must rollback", IdempotencyKey: &key})
	assertDomainCode(t, err, domain.CodeStorageConstraint)

	var issues, events, records int64
	var nextNumber int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM idempotency_records WHERE operation = 'create_issue' AND idempotency_key = ?", key).Scan(&records); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&nextNumber)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != 0 || events != 0 || records != 0 || nextNumber != 1 {
		t.Fatalf("after rollback: issues=%d events=%d records=%d next_issue_number=%d", issues, events, records, nextNumber)
	}
}

func TestIssueCreationRejectsDatabaseWithoutProjectRow(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM projects")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "no project"})
	assertDomainCode(t, err, domain.CodeProjectNotInitialized)
}

func TestIssueCreationAllocatesUniqueMonotonicNumbersConcurrently(t *testing.T) {
	service, db, _ := openIssueService(t)
	const count = 40
	results := make(chan application.CreateIssueResult, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	group.Add(count)
	for index := range count {
		go func(index int) {
			defer group.Done()
			result, err := service.CreateIssue(context.Background(), domain.CreateIssueInput{
				Type:  domain.TypeTask,
				Title: fmt.Sprintf("concurrent %d", index),
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(index)
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("CreateIssue() error = %v", err)
	}

	sequenceNumbers := make([]int, 0, count)
	seenIDs := make(map[string]struct{}, count)
	for result := range results {
		sequenceNumbers = append(sequenceNumbers, int(result.SequenceNo))
		if _, exists := seenIDs[result.ID]; exists {
			t.Errorf("duplicate issue ID %s", result.ID)
		}
		seenIDs[result.ID] = struct{}{}
	}
	if len(sequenceNumbers) != count {
		t.Fatalf("created %d issues, want %d", len(sequenceNumbers), count)
	}
	sort.Ints(sequenceNumbers)
	for index, sequenceNo := range sequenceNumbers {
		if sequenceNo != index+1 {
			t.Fatalf("sorted sequence numbers = %v", sequenceNumbers)
		}
	}

	var issues, events, nextNumber int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'issue_created'").Scan(&events); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&nextNumber)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != count || events != count || nextNumber != count+1 {
		t.Fatalf("issues=%d events=%d next_issue_number=%d", issues, events, nextNumber)
	}
}

func openIssueService(t *testing.T) (*application.IssueService, *sqlite.DB, time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.FixedZone("test", 2*60*60)).UTC()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "issues.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	fakeClock := clock.NewFakeClock(now)
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		timestamp := now.Format(time.RFC3339Nano)
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at)
			VALUES (?, 1, ?, ?)`, sqliteTestProjectID, timestamp, timestamp)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	repository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatalf("NewIssueRepository() error = %v", err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	service, err := application.NewIssueService(repository, fakeClock, generator)
	if err != nil {
		t.Fatalf("NewIssueService() error = %v", err)
	}
	return service, db, now
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, &domain.Error{Code: code}) {
		t.Fatalf("error = %v, want domain code %s", err, code)
	}
}
