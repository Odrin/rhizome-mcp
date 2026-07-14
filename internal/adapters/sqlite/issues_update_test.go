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

func TestIssueUpdatePersistsPatchStatusEventAndCanonicalParent(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	epic, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeEpic, Title: "Epic"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID:         issue.DisplayID,
		ExpectedVersion: 1,
		Changes: domain.IssuePatch{
			Title:    domain.OptionalValue[string]{Set: true, Value: "Updated"},
			Priority: domain.OptionalValue[domain.Priority]{Set: true, Value: domain.PriorityHigh},
			Status:   domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusReady},
			ParentID: domain.OptionalString{Set: true, Value: pointer(epic.DisplayID)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Version != 2 || result.Issue.SequenceNo != issue.SequenceNo ||
		result.Issue.DisplayID != issue.DisplayID || result.Issue.ParentID == nil ||
		*result.Issue.ParentID != epic.ID || !result.Issue.UpdatedAt.Equal(now) {
		t.Fatalf("update result = %#v", result)
	}
	wantChanged := []string{"parent_id", "priority", "status", "title"}
	if !equalStrings(result.ChangedFields, wantChanged) {
		t.Fatalf("changed fields = %v, want %v", result.ChangedFields, wantChanged)
	}
	var eventType, payload string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT event_type, payload FROM issue_events
			WHERE issue_id = ? ORDER BY id DESC LIMIT 1`, issue.ID).Scan(&eventType, &payload)
	}); err != nil {
		t.Fatal(err)
	}
	if eventType != "status_changed" {
		t.Fatalf("event type = %q", eventType)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["description"]; exists {
		t.Fatalf("unsafe description payload = %s", payload)
	}
}

func TestIssueUpdateBlockedTransitionsClosedAtAndRegularEvent(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeBug, Title: "Bug"})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := service.UpdateIssue(ctx, updateStatus(issue.ID, 1, domain.StatusReady, nil))
	if err != nil {
		t.Fatal(err)
	}
	reason := "dependency"
	blocked, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID:         issue.ID,
		ExpectedVersion: ready.Issue.Version,
		Changes: domain.IssuePatch{
			Status:        domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusBlocked},
			BlockedReason: domain.OptionalString{Set: true, Value: &reason},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	unblocked, err := service.UpdateIssue(ctx, updateStatus(issue.ID, blocked.Issue.Version, domain.StatusReady, nil))
	if err != nil {
		t.Fatal(err)
	}
	if unblocked.Issue.BlockedReason != nil {
		t.Fatalf("blocked reason not cleared: %#v", unblocked.Issue)
	}
	done, err := service.UpdateIssue(ctx, updateStatus(issue.ID, unblocked.Issue.Version, domain.StatusDone, nil))
	if err != nil {
		t.Fatal(err)
	}
	if done.Issue.ClosedAt == nil {
		t.Fatal("done issue has nil closed_at")
	}
	reopened, err := service.UpdateIssue(ctx, updateStatus(issue.ID, done.Issue.Version, domain.StatusReady, nil))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Issue.ClosedAt != nil {
		t.Fatal("reopened issue retained closed_at")
	}
	title := "Regular event"
	if _, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: reopened.Issue.Version,
		Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: title}},
	}); err != nil {
		t.Fatal(err)
	}
	var eventType string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT event_type FROM issue_events WHERE issue_id = ?
			ORDER BY id DESC LIMIT 1`, issue.ID).Scan(&eventType)
	}); err != nil {
		t.Fatal(err)
	}
	if eventType != "issue_updated" {
		t.Fatalf("event type = %q", eventType)
	}
}

func TestIssueUpdateConflictAndFailuresDoNotAppendEvents(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Task"})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []domain.UpdateIssueInput{
		{IssueID: issue.ID, ExpectedVersion: 1, Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "One"}}},
		{IssueID: issue.ID, ExpectedVersion: 1, Changes: domain.IssuePatch{Priority: domain.OptionalValue[domain.Priority]{Set: true, Value: domain.PriorityHigh}}},
	}
	errs := make(chan error, len(inputs))
	var group sync.WaitGroup
	for _, input := range inputs {
		group.Add(1)
		go func(input domain.UpdateIssueInput) {
			defer group.Done()
			_, err := service.UpdateIssue(context.Background(), input)
			errs <- err
		}(input)
	}
	group.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		if err == nil {
			successes++
		} else if domainErrorCode(err) == domain.CodeVersionConflict {
			conflicts++
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || !domainErr.Retryable {
				t.Fatalf("concurrent update conflict retryable = %v, want true", domainErr != nil && domainErr.Retryable)
			}
		} else {
			t.Fatalf("concurrent update error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	_, err = service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: 1,
		Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "Stale"}},
	})
	if domainErrorCode(err) != domain.CodeVersionConflict {
		t.Fatalf("stale update error = %v, want %s", err, domain.CodeVersionConflict)
	}
	var staleDomainErr *domain.Error
	if !errors.As(err, &staleDomainErr) || !staleDomainErr.Retryable {
		t.Fatalf("stale update error retryable = %v, want true", staleDomainErr != nil && staleDomainErr.Retryable)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET archived_at = ? WHERE id = ?", now.Format(time.RFC3339Nano), issue.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: 2,
		Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "Archived"}},
	})
	assertDomainCode(t, err, domain.CodeIssueArchived)
	_, err = service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: "ISSUE-999", ExpectedVersion: 1,
		Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "Missing"}},
	})
	assertDomainCode(t, err, domain.CodeIssueNotFound)
	var events int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issue.ID).Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("events = %d, want creation plus one successful update", events)
	}
}

func TestIssueUpdateRejectsInvalidParentsWithoutMutation(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Task"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: 1,
		Changes: domain.IssuePatch{ParentID: domain.OptionalString{Set: true, Value: pointer(issue.ID)}},
	})
	assertDomainCode(t, err, domain.CodeInvalidEpicParent)
	nonEpic, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: 1,
		Changes: domain.IssuePatch{ParentID: domain.OptionalString{Set: true, Value: pointer(nonEpic.ID)}},
	})
	assertDomainCode(t, err, domain.CodeInvalidEpicParent)
	var version, events int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT version FROM issues WHERE id = ?", issue.ID).Scan(&version); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issue.ID).Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if version != 1 || events != 1 {
		t.Fatalf("version=%d events=%d", version, events)
	}
}

func updateStatus(id string, version int64, status domain.Status, reason *string) domain.UpdateIssueInput {
	return domain.UpdateIssueInput{
		IssueID: id, ExpectedVersion: version,
		Changes: domain.IssuePatch{
			Status:        domain.OptionalValue[domain.Status]{Set: true, Value: status},
			BlockedReason: domain.OptionalString{Set: reason != nil, Value: reason},
		},
	}
}

func pointer(value string) *string { return &value }

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func domainErrorCode(err error) string {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		return domainErr.Code
	}
	return ""
}
