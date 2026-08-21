package sqlite_test

import (
	"context"
	"encoding/json"
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
	timestamp := sqlite.FormatStorageTime(now)
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

func TestSearchClassifiesParserNoSuchColumnAsInvalidQuery(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	issueTitle := "renewable lease"
	_, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: issueTitle, Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET title = ? WHERE title = ?`, "multi-project router", issueTitle)
		return err
	}); err != nil {
		t.Fatalf("seed indexed title: %v", err)
	}
	_ = now

	repository, err := sqlite.NewSearchRepository(db)
	if err != nil {
		t.Fatalf("NewSearchRepository() error = %v", err)
	}
	for _, tc := range []struct {
		name    string
		query   string
		wantErr bool
	}{
		{name: "hyphenated token", query: "multi-project", wantErr: true},
		{name: "unknown prefix", query: "unknown:term", wantErr: true},
		{name: "reproduction query", query: "project_ref multi-project router global MCP workspace project root configured default roots stateless", wantErr: true},
		{name: "quoted phrase", query: `"multi-project"`, wantErr: false},
		{name: "boolean syntax", query: "renewable OR lease", wantErr: false},
		{name: "prefix syntax", query: "renewable*", wantErr: false},
		{name: "column filter", query: "title:renewable", wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := repository.Search(ctx, portsSearch(domain.SearchInput{Query: tc.query, Limit: 10, SnippetLength: 12}))
			if tc.wantErr {
				var domainErr *domain.Error
				if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument || len(domainErr.Details) != 1 || domainErr.Details[0].Code != "INVALID_FTS_QUERY" {
					t.Fatalf("Search(%q) error = %v, want invalid FTS query", tc.query, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Search(%q) error = %v", tc.query, err)
			}
		})
	}
}

func TestGetChangesReturnsOrderedFilteredIncrementalPages(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "change target"})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	otherIssue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "other change target"})
	if err != nil {
		t.Fatalf("create other issue: %v", err)
	}
	var since int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM issue_events").Scan(&since)
	}); err != nil {
		t.Fatalf("read baseline event ID: %v", err)
	}
	timestamp := sqlite.FormatStorageTime(now.Add(time.Second))
	relationPayload, err := json.Marshal(map[string]any{
		"relation_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "source_issue_id": issue.ID,
		"target_issue_id": otherIssue.ID, "relation_type": "related_to",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		for _, event := range []struct {
			issueID   any
			eventType string
			payload   string
		}{
			{issue.ID, "comment_added", "{}"},
			{issue.ID, "relation_added", string(relationPayload)},
			{otherIssue.ID, "relation_added", string(relationPayload)},
			{nil, "project_event", "{}"},
			{issue.ID, "status_changed", "{}"},
		} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(
				issue_id, event_type, payload, created_at
			) VALUES (?, ?, ?, ?)`, event.issueID, event.eventType, event.payload, timestamp); err != nil {
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
	relationType := []string{"relation_added"}
	globalRelations, err := repository.GetChanges(ctx, portsChanges(domain.GetChangesInput{
		SinceEventID: since, EventTypes: relationType,
	}))
	if err != nil {
		t.Fatalf("GetChanges() global relations error = %v", err)
	}
	if len(globalRelations.Events) != 1 || globalRelations.Events[0].IssueID == nil || *globalRelations.Events[0].IssueID != issue.ID {
		t.Fatalf("global relation changes = %#v, want one source event", globalRelations.Events)
	}
	for _, scopedIssue := range []string{issue.ID, otherIssue.ID} {
		scopedRelations, err := repository.GetChanges(ctx, portsChanges(domain.GetChangesInput{
			SinceEventID: since, IssueID: &scopedIssue, EventTypes: relationType,
		}))
		if err != nil {
			t.Fatalf("GetChanges() scoped relations error = %v", err)
		}
		if len(scopedRelations.Events) != 1 || scopedRelations.Events[0].IssueID == nil || *scopedRelations.Events[0].IssueID != scopedIssue {
			t.Fatalf("scoped relation changes for %s = %#v", scopedIssue, scopedRelations.Events)
		}
	}
}

func portsSearch(input domain.SearchInput) ports.SearchCommand {
	return ports.SearchCommand{Input: input}
}

func portsChanges(input domain.GetChangesInput) ports.GetChangesCommand {
	return ports.GetChangesCommand{Input: input}
}
