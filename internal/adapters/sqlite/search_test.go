package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/pagination"
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

func TestSearchPageRenderingMatchesLegacyRankingAndSnippets(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	active, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "needle source", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "needle archived"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET archived_at = ? WHERE id = ?`, sqlite.FormatStorageTime(now), archived.ID); err != nil {
			return err
		}
		for index := 1; index <= 45; index++ {
			owner := active.ID
			if index%5 == 0 {
				owner = archived.ID
			}
			content := strings.Repeat("needle detail ", index%3+1) + "\u79df\u7ea6 \u03bb\u03cd\u03c3\u03b7 " + strings.Repeat("padding ", index%4*128)
			if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id,issue_id,content,created_at) VALUES(?,?,?,?)`, fmt.Sprintf("%026d", index), owner, content, sqlite.FormatStorageTime(now)); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO search_index(entity_type,entity_id,issue_id,title,content) VALUES('decision','00000000000000000000000046',NULL,'needle decision',?)`, "needle detail \u79df\u7ea6")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := sqlite.NewSearchRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{"needle", `"needle detail"`, "needle*", "title:needle", "content:needle", "\u79df\u7ea6", "needle OR \u79df\u7ea6", "NEAR(needle detail, 3)"}
	for _, query := range queries {
		for _, length := range []int{1, 12, 300, 1000} {
			for _, includeArchived := range []bool{false, true} {
				for _, types := range [][]domain.SearchEntityType{nil, {domain.SearchEntityTypeComment}, {domain.SearchEntityTypeDecision}} {
					input := domain.SearchInput{Query: query, Limit: 100, SnippetLength: length, IncludeArchived: includeArchived, EntityTypes: types}
					want := legacySearchPage(t, db, input)
					got, err := repository.Search(ctx, portsSearch(input))
					if err != nil {
						t.Fatalf("input=%#v: %v", input, err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("input=%#v\ngot=%#v\nwant=%#v", input, got, want)
					}
				}
			}
		}
		// Snippet length cannot affect rank or continuation. Sweep cursor
		// boundaries once per filter/query rather than once per length.
		for _, limit := range []int{1, 7, 20, 100} {
			for _, includeArchived := range []bool{false, true} {
				for _, types := range [][]domain.SearchEntityType{nil, {domain.SearchEntityTypeComment}, {domain.SearchEntityTypeDecision}} {
					input := domain.SearchInput{Query: query, Limit: limit, SnippetLength: 300, IncludeArchived: includeArchived, EntityTypes: types}
					seen := make(map[string]bool)
					for page := 0; page < 50; page++ {
						want := legacySearchPage(t, db, input)
						got, err := repository.Search(ctx, portsSearch(input))
						if err != nil {
							t.Fatalf("input=%#v: %v", input, err)
						}
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("input=%#v page=%d\ngot=%#v\nwant=%#v", input, page, got, want)
						}
						for _, item := range got.Results {
							key := string(item.EntityType) + ":" + item.EntityID
							if seen[key] {
								t.Fatalf("repeated result: %s", key)
							}
							seen[key] = true
						}
						if got.NextCursor == nil {
							break
						}
						input.Cursor = *got.NextCursor
						if page == 49 {
							t.Fatal("cursor did not terminate")
						}
					}
				}
			}
		}
	}
}

func legacySearchPage(t *testing.T, db *sqlite.DB, input domain.SearchInput) domain.SearchPage {
	t.Helper()
	input, err := input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	where := []string{"search_index MATCH ?"}
	args := []any{input.SnippetLength, input.Query}
	if !input.IncludeArchived {
		where = append(where, "(search_index.issue_id IS NULL OR issues.archived_at IS NULL)")
	}
	if len(input.EntityTypes) > 0 {
		placeholders := []string{}
		for _, kind := range input.EntityTypes {
			placeholders = append(placeholders, "?")
			args = append(args, string(kind))
		}
		where = append(where, "search_index.entity_type IN ("+strings.Join(placeholders, ",")+")")
	}
	statement := `WITH matches AS MATERIALIZED (
	 SELECT search_index.entity_type,search_index.entity_id,search_index.issue_id,search_index.title,
	 substr(snippet(search_index,-1,'[',']','...',64),1,?) AS snippet,bm25(search_index) AS score
	 FROM search_index LEFT JOIN issues ON issues.id = search_index.issue_id WHERE ` + strings.Join(where, " AND ") + `)
	 SELECT entity_type,entity_id,issue_id,title,snippet,score FROM matches`
	codec := pagination.NewCodec[searchPageBenchmarkCursor](0)
	if input.Cursor != "" {
		after, err := codec.Decode(input.Cursor)
		if err != nil {
			t.Fatal(err)
		}
		statement += ` WHERE score > ? OR (score = ? AND (entity_type > ? OR (entity_type = ? AND entity_id > ?)))`
		args = append(args, after.Score, after.Score, after.EntityType, after.EntityType, after.EntityID)
	}
	statement += ` ORDER BY score,entity_type,entity_id LIMIT ?`
	args = append(args, input.Limit+1)
	result := domain.SearchPage{Results: []domain.SearchResult{}}
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		rows, err := query.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.SearchResult
			var issueID sql.NullString
			if err := rows.Scan(&item.EntityType, &item.EntityID, &issueID, &item.Title, &item.Snippet, &item.Score); err != nil {
				return err
			}
			if issueID.Valid {
				item.IssueID = &issueID.String
			}
			result.Results = append(result.Results, item)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(result.Results) > input.Limit {
		result.HasMore = true
		result.Results = result.Results[:input.Limit]
		last := result.Results[len(result.Results)-1]
		cursor, err := codec.Encode(searchPageBenchmarkCursor{Score: last.Score, EntityType: string(last.EntityType), EntityID: last.EntityID})
		if err != nil {
			t.Fatal(err)
		}
		result.NextCursor = &cursor
	}
	return domain.CloneSearchPage(result)
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
