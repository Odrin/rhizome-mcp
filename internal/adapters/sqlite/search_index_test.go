package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

var _ ports.SearchIndexRepository = (*sqlite.SearchIndexRepository)(nil)

func TestSearchIndexTracksSourceMutationsTransactionallyAndRebuilds(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	description := "issue searchable body"
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type:        domain.TypeTask,
		Title:       "searchable issue title",
		Description: &description,
		Status:      domain.StatusReady,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	const (
		commentID  = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		decisionID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
		attemptID  = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
		noteID     = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	)
	timestamp := now.Format(time.RFC3339Nano)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at)
			VALUES (?, ?, 'searchable comment body', ?)`, commentID, issue.ID, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions(
			id, issue_id, title, summary, content, status, created_at
		) VALUES (?, ?, 'searchable decision title', 'searchable decision summary', 'searchable decision body', 'active', ?)`,
			decisionID, issue.ID, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`,
			attemptID, issue.ID, timestamp, timestamp, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(
			id, attempt_id, kind, content, important, created_at
		) VALUES (?, ?, 'progress', 'searchable note body', 0, ?)`, noteID, attemptID, timestamp)
		return err
	}); err != nil {
		t.Fatalf("insert indexed sources: %v", err)
	}

	initial := searchIndexRows(t, db)
	if got, want := initial, []searchIndexRow{
		{EntityType: "attempt_note", EntityID: noteID, IssueID: issue.ID, Title: "", Content: "searchable note body"},
		{EntityType: "comment", EntityID: commentID, IssueID: issue.ID, Title: "", Content: "searchable comment body"},
		{EntityType: "decision", EntityID: decisionID, IssueID: issue.ID, Title: "searchable decision title", Content: "searchable decision summary\nsearchable decision body"},
		{EntityType: "issue", EntityID: issue.ID, IssueID: issue.ID, Title: "searchable issue title", Content: description},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("search index rows = %#v, want %#v", got, want)
	}

	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET description = 'updated searchable issue body' WHERE id = ?", issue.ID)
		return err
	}); err != nil {
		t.Fatalf("update indexed issue: %v", err)
	}
	updated := searchIndexRows(t, db)
	if updated[3].Content != "updated searchable issue body" {
		t.Fatalf("updated issue index content = %q", updated[3].Content)
	}

	rollbackCommentID := "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at)
			VALUES (?, ?, 'rolled back source', ?)`, rollbackCommentID, issue.ID, timestamp); err != nil {
			return err
		}
		return errors.New("force rollback")
	}); err == nil {
		t.Fatal("rolling back indexed source succeeded")
	}
	for _, row := range searchIndexRows(t, db) {
		if row.EntityID == rollbackCommentID {
			t.Fatal("rolled-back source remained in search index")
		}
	}

	repository, err := sqlite.NewSearchIndexRepository(db)
	if err != nil {
		t.Fatalf("NewSearchIndexRepository() error = %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM search_index")
		return err
	}); err != nil {
		t.Fatalf("clear search index: %v", err)
	}
	if err := repository.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if got, want := searchIndexRows(t, db), updated; !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt search index rows = %#v, want %#v", got, want)
	}
}

type searchIndexRow struct {
	EntityType string
	EntityID   string
	IssueID    string
	Title      string
	Content    string
}

func searchIndexRows(t *testing.T, db *sqlite.DB) []searchIndexRow {
	t.Helper()
	var result []searchIndexRow
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		rows, err := query.QueryContext(ctx, `SELECT entity_type, entity_id, issue_id, title, content
			FROM search_index`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row searchIndexRow
			if err := rows.Scan(&row.EntityType, &row.EntityID, &row.IssueID, &row.Title, &row.Content); err != nil {
				return err
			}
			result = append(result, row)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read search index: %v", err)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].EntityType != result[j].EntityType {
			return result[i].EntityType < result[j].EntityType
		}
		return result[i].EntityID < result[j].EntityID
	})
	return result
}
