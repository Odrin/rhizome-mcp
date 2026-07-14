package sqlite_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

const commentTestID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"

func TestNewCommentRepositoryRejectsNilDatabase(t *testing.T) {
	_, err := sqlite.NewCommentRepository(nil)
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
}

func TestCommentRepositoryPersistsCommentAndCompactAttributedEvent(t *testing.T) {
	issues, db, now := openIssueService(t)
	repository, err := sqlite.NewCommentRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Commented"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	seedCommentSession(t, db, sessionID, now)

	comment, err := repository.AddComment(context.Background(), ports.AddCommentCommand{
		ID:         commentTestID,
		Input:      domain.AddCommentInput{IssueID: issue.DisplayID, Content: "  Markdown  ", SessionID: &sessionID},
		OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	if comment.ID != commentTestID || comment.IssueID != issue.ID || comment.Content != "  Markdown  " ||
		comment.CreatedBySessionID == nil || *comment.CreatedBySessionID != sessionID || comment.AuthorLabel != nil ||
		comment.EditedAt != nil || !comment.CreatedAt.Equal(now) {
		t.Fatalf("comment = %#v", comment)
	}

	var eventIssueID, eventType, payload, createdAt string
	var eventSessionID, attemptID sql.NullString
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT issue_id, event_type, session_id, attempt_id, payload, created_at
			FROM issue_events WHERE event_type = 'comment_added'`).Scan(
			&eventIssueID, &eventType, &eventSessionID, &attemptID, &payload, &createdAt)
	}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if eventIssueID != issue.ID || eventType != "comment_added" || !eventSessionID.Valid || eventSessionID.String != sessionID ||
		attemptID.Valid || decoded["comment_id"] != commentTestID || len(decoded) != 1 || createdAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("event = issue=%q type=%q session=%q attempt=%q payload=%q created=%q", eventIssueID, eventType, eventSessionID.String, attemptID.String, payload, createdAt)
	}
}

func TestCommentRepositoryPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.FixedZone("test", 2*60*60)).UTC()
	path := filepath.Join(t.TempDir(), "comments.db")
	fakeClock := clock.NewFakeClock(now)

	db, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		timestamp := now.Format(time.RFC3339Nano)
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at)
			VALUES (?, 1, ?, ?)`, sqliteTestProjectID, timestamp, timestamp)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := application.NewIssueService(issueRepository, fakeClock, generator)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Commented"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	seedCommentSession(t, db, sessionID, now)
	content := "\n  **meaningful Markdown**  \n"
	repository, err := sqlite.NewCommentRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AddComment(ctx, ports.AddCommentCommand{
		ID:         commentTestID,
		Input:      domain.AddCommentInput{IssueID: issue.DisplayID, Content: content, SessionID: &sessionID},
		OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(ctx); err != nil {
		t.Fatal(err)
	}

	db, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(ctx); err != nil {
			t.Errorf("reopened database close: %v", err)
		}
	})
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.NewCommentRepository(db); err != nil {
		t.Fatal(err)
	}

	var (
		commentCount, eventCount                       int
		commentID, issueID, storedContent, createdAt   string
		commentSessionID, authorLabel, editedAt        sql.NullString
		eventIssueID, eventType, eventPayload, eventAt string
		eventSessionID, eventAttemptID                 sql.NullString
	)
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM comments").Scan(&commentCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT id, issue_id, content, created_by_session_id,
			author_label, edited_at, created_at FROM comments WHERE id = ?`, commentTestID).Scan(
			&commentID, &issueID, &storedContent, &commentSessionID, &authorLabel, &editedAt, &createdAt); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'comment_added' AND issue_id = ?", issue.ID).Scan(&eventCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT issue_id, event_type, session_id, attempt_id, payload, created_at
			FROM issue_events WHERE event_type = 'comment_added' AND issue_id = ?`, issue.ID).Scan(
			&eventIssueID, &eventType, &eventSessionID, &eventAttemptID, &eventPayload, &eventAt)
	}); err != nil {
		t.Fatal(err)
	}
	if commentCount != 1 || commentID != commentTestID || issueID != issue.ID || storedContent != content ||
		!commentSessionID.Valid || commentSessionID.String != sessionID || authorLabel.Valid || editedAt.Valid ||
		createdAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("comment = id=%q issue=%q content=%q session=%q author=%q edited=%q created=%q",
			commentID, issueID, storedContent, commentSessionID.String, authorLabel.String, editedAt.String, createdAt)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(eventPayload), &payload); err != nil {
		t.Fatal(err)
	}
	var payloadCommentID string
	if value, ok := payload["comment_id"]; !ok {
		t.Fatalf("event payload = %q, missing comment_id", eventPayload)
	} else if err := json.Unmarshal(value, &payloadCommentID); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || eventIssueID != issue.ID || eventType != "comment_added" ||
		!eventSessionID.Valid || eventSessionID.String != sessionID || eventAttemptID.Valid ||
		len(payload) != 1 || payloadCommentID != commentTestID || eventAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("event = issue=%q type=%q session=%q attempt=%q payload=%q created=%q",
			eventIssueID, eventType, eventSessionID.String, eventAttemptID.String, eventPayload, eventAt)
	}
}

func TestCommentRepositoryAllowsNullSessionAndRejectsUnknownSessionAtomically(t *testing.T) {
	issues, db, now := openIssueService(t)
	repository, err := sqlite.NewCommentRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Commented"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AddComment(context.Background(), ports.AddCommentCommand{
		ID: commentTestID, Input: domain.AddCommentInput{IssueID: issue.ID, Content: "unattributed"}, OccurredAt: now,
	}); err != nil {
		t.Fatalf("NULL session AddComment() error = %v", err)
	}
	unknown := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	_, err = repository.AddComment(context.Background(), ports.AddCommentCommand{
		ID:    "01ARZ3NDEKTSV4RRFFQ69G5FAY",
		Input: domain.AddCommentInput{IssueID: issue.ID, Content: "must roll back", SessionID: &unknown}, OccurredAt: now,
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeStorageConstraint}) {
		t.Fatalf("unknown session error = %v, want storage constraint", err)
	}
	var comments, events int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM comments").Scan(&comments); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'comment_added'").Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if comments != 1 || events != 1 {
		t.Fatalf("unknown session was not rolled back: comments=%d events=%d", comments, events)
	}
}

func TestCommentRepositoryRejectsArchivedMissingAndEventFailureWithoutWrites(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *application.IssueService, *sqlite.CommentRepository, *sqlite.DB, time.Time)
	}{
		{
			name: "archived",
			run: func(t *testing.T, issues *application.IssueService, repository *sqlite.CommentRepository, db *sqlite.DB, now time.Time) {
				issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Archived"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := issues.ArchiveIssue(context.Background(), domain.ArchiveIssueInput{IssueID: issue.ID, ExpectedVersion: issue.Issue.Version}); err != nil {
					t.Fatal(err)
				}
				_, err = repository.AddComment(context.Background(), ports.AddCommentCommand{
					ID: commentTestID, Input: domain.AddCommentInput{IssueID: issue.ID, Content: "nope"}, OccurredAt: now,
				})
				assertDomainCode(t, err, domain.CodeIssueArchived)
				assertCommentCounts(t, db, 0, 0)
			},
		},
		{
			name: "missing",
			run: func(t *testing.T, _ *application.IssueService, repository *sqlite.CommentRepository, db *sqlite.DB, now time.Time) {
				_, err := repository.AddComment(context.Background(), ports.AddCommentCommand{
					ID: commentTestID, Input: domain.AddCommentInput{IssueID: "ISSUE-999", Content: "nope"}, OccurredAt: now,
				})
				assertDomainCode(t, err, domain.CodeIssueNotFound)
				assertCommentCounts(t, db, 0, 0)
			},
		},
		{
			name: "event failure",
			run: func(t *testing.T, issues *application.IssueService, repository *sqlite.CommentRepository, db *sqlite.DB, now time.Time) {
				issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Trigger"})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
					_, err := tx.ExecContext(ctx, `CREATE TRIGGER fail_comment_event
						BEFORE INSERT ON issue_events WHEN NEW.event_type = 'comment_added'
						BEGIN SELECT RAISE(ABORT, 'forced comment event failure'); END`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				_, err = repository.AddComment(context.Background(), ports.AddCommentCommand{
					ID: commentTestID, Input: domain.AddCommentInput{IssueID: issue.ID, Content: "rollback"}, OccurredAt: now,
				})
				if errors.Is(err, &domain.Error{Code: domain.CodeIssueNotFound}) || err == nil {
					t.Fatalf("event failure error = %v", err)
				}
				assertCommentCounts(t, db, 0, 0)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues, db, now := openIssueService(t)
			repository, err := sqlite.NewCommentRepository(db)
			if err != nil {
				t.Fatal(err)
			}
			test.run(t, issues, repository, db, now)
		})
	}
}

func seedCommentSession(t *testing.T, db *sqlite.DB, id string, now time.Time) {
	t.Helper()
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(
			id, client_name, started_at, last_seen_at
		) VALUES (?, 'comment-test', ?, ?)`, id, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func assertCommentCounts(t *testing.T, db *sqlite.DB, wantComments, wantEvents int) {
	t.Helper()
	var comments, events int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM comments").Scan(&comments); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'comment_added'").Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if comments != wantComments || events != wantEvents {
		t.Fatalf("comment state = comments=%d events=%d", comments, events)
	}
}
