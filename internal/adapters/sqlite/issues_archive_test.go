package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

func TestIssueArchivePersistsProjectionByULIDAndDisplayIDAndEvent(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	first, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Archive by ULID",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeBug, Title: "Archive by display ID",
	})
	if err != nil {
		t.Fatal(err)
	}

	archived, err := service.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID: first.ID, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("ArchiveIssue(ULID): %v", err)
	}
	if archived.Issue.ID != first.ID || archived.Issue.DisplayID != first.DisplayID ||
		archived.Issue.SequenceNo != first.SequenceNo || archived.Issue.Version != 2 ||
		archived.Issue.ArchivedAt == nil || !archived.Issue.ArchivedAt.Equal(now) ||
		!archived.Issue.UpdatedAt.Equal(now) {
		t.Fatalf("archived ULID projection = %#v", archived.Issue)
	}

	archived, err = service.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID: second.DisplayID, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("ArchiveIssue(display ID): %v", err)
	}
	if archived.Issue.ID != second.ID || archived.Issue.DisplayID != second.DisplayID ||
		archived.Issue.SequenceNo != second.SequenceNo || archived.Issue.Version != 2 ||
		archived.Issue.ArchivedAt == nil {
		t.Fatalf("archived display projection = %#v", archived.Issue)
	}
	visible, err := service.GetIssue(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetIssue(archived): %v", err)
	}
	if visible.ArchivedAt == nil || visible.Version != 2 {
		t.Fatalf("archived issue is not visible through get: %#v", visible)
	}

	var eventType, payload string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `
			SELECT event_type, payload FROM issue_events
			WHERE issue_id = ? ORDER BY id DESC LIMIT 1`, first.ID).Scan(&eventType, &payload)
	}); err != nil {
		t.Fatal(err)
	}
	if eventType != "issue_archived" || payload != `{"version":2,"archived_at":"`+now.Format(time.RFC3339Nano)+`"}` {
		t.Fatalf("archive event = type %q payload %s", eventType, payload)
	}
	if !json.Valid([]byte(payload)) {
		t.Fatalf("archive event payload is invalid JSON: %s", payload)
	}
}

func TestIssueArchivePreservesRelatedDataAndBlocksActiveAttempt(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Protected"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	labelID := "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	relationID := "01BX5ZZKBKACTAV9WEVGEMMVS0"
	commentID := "01BX5ZZKBKACTAV9WEVGEMMVS1"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		timestamp := now.Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO labels(id, name, created_at) VALUES (?, 'preserve', ?)`, labelID, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_labels(issue_id, label_id) VALUES (?, ?)`, issue.ID, labelID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_relations(
			id, source_issue_id, target_issue_id, type, created_at
		) VALUES (?, ?, ?, 'blocks', ?)`, relationID, issue.ID, other.ID, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at)
			VALUES (?, ?, 'keep this', ?)`, commentID, issue.ID, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, ?, ?, ?, ?)`,
			"01BX5ZZKBKACTAV9WEVGEMMVS2", issue.ID, []byte("hash"), timestamp, timestamp, timestamp)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var before struct{ labels, relations, comments, events int }
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_labels WHERE issue_id = ?", issue.ID).Scan(&before.labels); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations WHERE source_issue_id = ?", issue.ID).Scan(&before.relations); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM comments WHERE issue_id = ?", issue.ID).Scan(&before.comments); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issue.ID).Scan(&before.events)
	}); err != nil {
		t.Fatal(err)
	}

	_, err = service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: issue.ID, ExpectedVersion: 1})
	assertDomainCode(t, err, domain.CodeActiveAttemptExists)
	var archiveError *domain.Error
	if !errors.As(err, &archiveError) || archiveError.Retryable {
		t.Fatalf("active attempt error retryable = %v", archiveError != nil && archiveError.Retryable)
	}

	var after struct {
		version, archived, labels, relations, comments, events int
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT version, count(archived_at) FROM issues WHERE id = ?", issue.ID).Scan(&after.version, &after.archived); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_labels WHERE issue_id = ?", issue.ID).Scan(&after.labels); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations WHERE source_issue_id = ?", issue.ID).Scan(&after.relations); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM comments WHERE issue_id = ?", issue.ID).Scan(&after.comments); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issue.ID).Scan(&after.events)
	}); err != nil {
		t.Fatal(err)
	}
	if after.version != 1 || after.archived != 0 || after.labels != before.labels ||
		after.relations != before.relations || after.comments != before.comments || after.events != before.events {
		t.Fatalf("database changed on active-attempt rejection: before=%#v after=%#v", before, after)
	}

	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE work_attempts SET status = 'completed', finished_at = ?
			WHERE issue_id = ? AND status = 'active'`,
			now.Format(time.RFC3339Nano), issue.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: issue.ID, ExpectedVersion: 1}); err != nil {
		t.Fatalf("ArchiveIssue(after attempt completion): %v", err)
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT version, count(archived_at) FROM issues WHERE id = ?", issue.ID).Scan(&after.version, &after.archived); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_labels WHERE issue_id = ?", issue.ID).Scan(&after.labels); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations WHERE source_issue_id = ?", issue.ID).Scan(&after.relations); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM comments WHERE issue_id = ?", issue.ID).Scan(&after.comments); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issue.ID).Scan(&after.events)
	}); err != nil {
		t.Fatal(err)
	}
	if after.version != 2 || after.archived != 1 || after.labels != before.labels ||
		after.relations != before.relations || after.comments != before.comments || after.events != before.events+1 {
		t.Fatalf("related data changed on archive: before=%#v after=%#v", before, after)
	}
}

func TestIssueArchiveClassifiesMissingArchivedAndStaleWithoutMutation(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Classify"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: "ISSUE-999", ExpectedVersion: 1})
	assertDomainCode(t, err, domain.CodeIssueNotFound)

	updated, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: 1,
		Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "Changed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: issue.ID, ExpectedVersion: 1})
	assertDomainCode(t, err, domain.CodeVersionConflict)
	var conflict *domain.Error
	if !errors.As(err, &conflict) || !conflict.Retryable {
		t.Fatalf("stale archive retryable = %v", conflict != nil && conflict.Retryable)
	}
	archived, err := service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: issue.ID, ExpectedVersion: updated.Issue.Version})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Issue.ArchivedAt == nil {
		t.Fatal("successful archive has nil archived_at")
	}
	_, err = service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: issue.ID, ExpectedVersion: archived.Issue.Version})
	assertDomainCode(t, err, domain.CodeIssueArchived)

	var events int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issue.ID).Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 3 {
		t.Fatalf("events = %d, want creation, update, and archive", events)
	}
}

func TestIssueArchiveRollsBackProjectionWhenEventAppendFails(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Rollback"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_issue_archived_event
			BEFORE INSERT ON issue_events
			WHEN NEW.event_type = 'issue_archived'
			BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: issue.ID, ExpectedVersion: 1})
	assertDomainCode(t, err, domain.CodeStorageConstraint)

	var version int
	var archivedAt any
	var events int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT version, archived_at FROM issues WHERE id = ?", issue.ID).Scan(&version, &archivedAt); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issue.ID).Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if version != 1 || archivedAt != nil || events != 1 {
		t.Fatalf("archive rollback state: version=%d archived_at=%v events=%d", version, archivedAt, events)
	}
}

func TestIssueArchiveCompetingRequestsAppendOneEvent(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Compete"})
	if err != nil {
		t.Fatal(err)
	}
	const requests = 2
	errs := make(chan error, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.ArchiveIssue(context.Background(), domain.ArchiveIssueInput{
				IssueID: issue.ID, ExpectedVersion: 1,
			})
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		code := domainErrorCode(err)
		if code != domain.CodeIssueArchived && code != domain.CodeVersionConflict {
			t.Fatalf("competing archive error = %v", err)
		}
		if code == domain.CodeVersionConflict {
			var conflict *domain.Error
			if !errors.As(err, &conflict) || !conflict.Retryable {
				t.Fatal("competing version conflict is not retryable")
			}
		}
	}
	if successes != 1 {
		t.Fatalf("successful archives = %d, want 1", successes)
	}
	var archiveEvents int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `
			SELECT count(*) FROM issue_events
			WHERE issue_id = ? AND event_type = 'issue_archived'`, issue.ID).Scan(&archiveEvents)
	}); err != nil {
		t.Fatal(err)
	}
	if archiveEvents != 1 {
		t.Fatalf("archive events = %d, want 1", archiveEvents)
	}
}
