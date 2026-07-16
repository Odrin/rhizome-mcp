package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestSearchRanksPaginatesAndFiltersIndexedEntities(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	description := "renewable lease details"
	first, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "renewable lease", Description: &description, Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatalf("create first issue: %v", err)
	}
	second, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "lease handoff", Description: &description, Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create second issue: %v", err)
	}
	archived, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "archived lease", Description: &description,
	})
	if err != nil {
		t.Fatalf("create archived issue: %v", err)
	}
	timestamp := now.Format(time.RFC3339Nano)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at)
			VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAV', ?, 'lease comment', ?)`, first.ID, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET archived_at = ?, archived_by_session_id = NULL
			WHERE id = ?`, timestamp, archived.ID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed indexed sources: %v", err)
	}

	repository, err := sqlite.NewSearchRepository(db)
	if err != nil {
		t.Fatalf("NewSearchRepository() error = %v", err)
	}
	page, err := repository.Search(ctx, portsSearch(domain.SearchInput{Query: "lease", Limit: 1, SnippetLength: 12}))
	if err != nil {
		t.Fatalf("Search() first page error = %v", err)
	}
	if len(page.Results) != 1 || !page.HasMore || page.NextCursor == nil || len([]rune(page.Results[0].Snippet)) > 12 {
		t.Fatalf("first search page = %#v", page)
	}
	secondPage, err := repository.Search(ctx, portsSearch(domain.SearchInput{Query: "lease", Limit: 10, Cursor: *page.NextCursor}))
	if err != nil {
		t.Fatalf("Search() second page error = %v", err)
	}
	if len(secondPage.Results) == 0 || secondPage.HasMore {
		t.Fatalf("second search page = %#v", secondPage)
	}
	for _, result := range append(page.Results, secondPage.Results...) {
		if result.IssueID != nil && *result.IssueID == archived.ID {
			t.Fatal("archived issue appeared without include_archived")
		}
	}
	seen := make(map[string]struct{})
	for _, result := range append(page.Results, secondPage.Results...) {
		key := string(result.EntityType) + ":" + result.EntityID
		if _, exists := seen[key]; exists {
			t.Fatalf("cursor repeated result %s", key)
		}
		seen[key] = struct{}{}
	}

	issueID := first.DisplayID
	filtered, err := repository.Search(ctx, portsSearch(domain.SearchInput{
		Query: "lease", IssueID: &issueID, EntityTypes: []domain.SearchEntityType{domain.SearchEntityTypeComment},
	}))
	if err != nil {
		t.Fatalf("Search() filtered error = %v", err)
	}
	if len(filtered.Results) != 1 || filtered.Results[0].EntityType != domain.SearchEntityTypeComment ||
		filtered.Results[0].IssueID == nil || *filtered.Results[0].IssueID != first.ID {
		t.Fatalf("filtered results = %#v", filtered.Results)
	}

	included, err := repository.Search(ctx, portsSearch(domain.SearchInput{Query: "lease", IncludeArchived: true}))
	if err != nil {
		t.Fatalf("Search() include archived error = %v", err)
	}
	foundArchived := false
	for _, result := range included.Results {
		foundArchived = foundArchived || result.IssueID != nil && *result.IssueID == archived.ID
	}
	if !foundArchived {
		t.Fatal("include_archived did not include archived issue")
	}
	if _, err := repository.Search(ctx, portsSearch(domain.SearchInput{Query: "lease", Cursor: "bad"})); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("malformed cursor error = %v", err)
	}
	if _, err := repository.Search(ctx, portsSearch(domain.SearchInput{Query: `"`})); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("malformed FTS query error = %v", err)
	}
	if _, err := repository.Search(ctx, portsSearch(domain.SearchInput{
		Query:       "*",
		EntityTypes: []domain.SearchEntityType{domain.SearchEntityTypeDecision},
	})); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("wildcard decision query error = %v", err)
	}
	_ = second
}

func TestGetChangesReturnsOrderedFilteredIncrementalPages(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "change target"})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	var since int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM issue_events").Scan(&since)
	}); err != nil {
		t.Fatalf("read baseline event ID: %v", err)
	}
	timestamp := now.Add(time.Second).Format(time.RFC3339Nano)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		for _, event := range []struct {
			issueID   any
			eventType string
		}{{issue.ID, "comment_added"}, {nil, "project_event"}, {issue.ID, "status_changed"}} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(
				issue_id, event_type, payload, created_at
			) VALUES (?, ?, '{}', ?)`, event.issueID, event.eventType, timestamp); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	repository, err := sqlite.NewSearchRepository(db)
	if err != nil {
		t.Fatalf("NewSearchRepository() error = %v", err)
	}
	page, err := repository.GetChanges(ctx, portsChanges(domain.GetChangesInput{SinceEventID: since, Limit: 1}))
	if err != nil {
		t.Fatalf("GetChanges() first page error = %v", err)
	}
	if len(page.Events) != 1 || !page.HasMore || page.NextEventID != page.Events[0].ID || page.LatestEventID <= page.NextEventID {
		t.Fatalf("first changes page = %#v", page)
	}
	issueID := issue.DisplayID
	filtered, err := repository.GetChanges(ctx, portsChanges(domain.GetChangesInput{
		SinceEventID: since, IssueID: &issueID, EventTypes: []string{"status_changed"},
	}))
	if err != nil {
		t.Fatalf("GetChanges() filtered error = %v", err)
	}
	if len(filtered.Events) != 1 || filtered.Events[0].EventType != "status_changed" ||
		filtered.NextEventID != filtered.LatestEventID {
		t.Fatalf("filtered changes = %#v", filtered)
	}
}

func portsSearch(input domain.SearchInput) ports.SearchCommand {
	return ports.SearchCommand{Input: input}
}

func portsChanges(input domain.GetChangesInput) ports.GetChangesCommand {
	return ports.GetChangesCommand{Input: input}
}
