package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"

	_ "modernc.org/sqlite"
)

// TestActivityRegistryMatchesDomainCategoriesAndEntityTypes proves
// activityRegistry is the single source of truth the ISSUE-216 refactor
// requires: every category in domain.AllActivityCategories has exactly one
// registry entry, every registry category/entity type passes the domain
// Valid() methods, and no two entries share a category or a rank.
func TestActivityRegistryMatchesDomainCategoriesAndEntityTypes(t *testing.T) {
	if len(activityRegistry) != len(domain.AllActivityCategories) {
		t.Fatalf("activityRegistry has %d entries, domain.AllActivityCategories has %d", len(activityRegistry), len(domain.AllActivityCategories))
	}

	seenCategories := make(map[domain.ActivityCategory]bool, len(activityRegistry))
	seenRanks := make(map[int]bool, len(activityRegistry))
	for _, spec := range activityRegistry {
		if seenCategories[spec.Category] {
			t.Fatalf("duplicate category %q in activityRegistry", spec.Category)
		}
		seenCategories[spec.Category] = true
		if seenRanks[spec.Rank] {
			t.Fatalf("duplicate rank %d in activityRegistry", spec.Rank)
		}
		seenRanks[spec.Rank] = true
		if spec.Rank < 1 {
			t.Fatalf("registry entry %q has non-positive rank %d", spec.Category, spec.Rank)
		}
		if !spec.Category.Valid() {
			t.Fatalf("registry category %q fails domain.ActivityCategory.Valid()", spec.Category)
		}
		if !spec.EntityType.Valid() {
			t.Fatalf("registry entity type %q fails domain.ActivityEntityType.Valid()", spec.EntityType)
		}
		if spec.Batch == nil {
			t.Fatalf("registry entry %q has no Batch func", spec.Category)
		}
	}
	for _, category := range domain.AllActivityCategories {
		if !seenCategories[category] {
			t.Fatalf("domain.AllActivityCategories has %q with no matching activityRegistry entry", category)
		}
	}
	if activityMaxRank != len(activityRegistry) {
		t.Fatalf("activityMaxRank = %d, want %d (ranks must be contiguous starting at 1)", activityMaxRank, len(activityRegistry))
	}
}

type countingActivityQueryer struct {
	*sql.DB
	selects int
}

func (query *countingActivityQueryer) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	query.selects++
	return query.DB.QueryContext(ctx, statement, args...)
}

func TestActivityBatchHydrationOrderCountAndErrors(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE comments(id TEXT PRIMARY KEY, issue_id TEXT, content TEXT, created_by_session_id TEXT, author_label TEXT, created_at TEXT, edited_at TEXT)`,
		`CREATE TABLE issue_events(id INTEGER PRIMARY KEY, issue_id TEXT, event_type TEXT, session_id TEXT, attempt_id TEXT, payload TEXT, created_at TEXT)`,
		`CREATE TABLE gate_evidence(id TEXT PRIMARY KEY, attempt_id TEXT, issue_id TEXT, key TEXT, result TEXT, summary TEXT, details TEXT, artifact_ids_json TEXT, version INTEGER, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE review_requests(id TEXT PRIMARY KEY, target_id TEXT, issue_id TEXT, target_issue_version INTEGER, target_event_id INTEGER, artifact_ids_json TEXT, status TEXT, supersedes_id TEXT, active_attempt_id TEXT, version INTEGER, created_at TEXT, resolved_at TEXT)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	issueID := fmt.Sprintf("%026d", 1)
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	stamp := formatStorageTime(now)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("%026d", i+2)
		if _, err := db.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, ?, ?)`,
			id, issueID, fmt.Sprintf("comment %d", i), stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO issue_events(id, issue_id, event_type, payload, created_at) VALUES (?, ?, 'event', ?, ?)`,
			i+1, issueID, fmt.Sprintf(`{"index":%d}`, i), stamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []int{1, 20, 100} {
		for _, mixed := range []bool{false, true} {
			name := fmt.Sprintf("size=%d/mixed=%t", size, mixed)
			t.Run(name, func(t *testing.T) {
				descriptors := make([]activityDescriptor, size)
				for i := range descriptors {
					descriptors[i] = activityDescriptor{EntityType: domain.ActivityEntityTypeComment, EntityID: fmt.Sprintf("%026d", i+2), OccurredAt: now}
					if mixed && i%2 == 1 {
						descriptors[i] = activityDescriptor{EntityType: domain.ActivityEntityTypeEvent, EntityID: fmt.Sprint(i + 1), OccurredAt: now}
					}
				}
				query := &countingActivityQueryer{DB: db}
				items, err := loadActivityItems(ctx, query, issueID, descriptors)
				if err != nil {
					t.Fatal(err)
				}
				wantSelects := 1
				if mixed && size > 1 {
					wantSelects++
				}
				if query.selects != wantSelects {
					t.Fatalf("hydration SELECTs = %d, want %d", query.selects, wantSelects)
				}
				for i, item := range items {
					if item.EntityType != descriptors[i].EntityType || item.EntityID != descriptors[i].EntityID || item.IssueID != issueID || !item.OccurredAt.Equal(now) {
						t.Fatalf("item %d = %#v, descriptor = %#v", i, item, descriptors[i])
					}
					if item.Comment != nil && item.Comment.Content != fmt.Sprintf("comment %d", i) {
						t.Fatalf("comment %d = %#v", i, item.Comment)
					}
					if item.Event != nil && string(item.Event.Payload) != fmt.Sprintf(`{"index":%d}`, i) {
						t.Fatalf("event %d = %#v", i, item.Event)
					}
				}
			})
		}
	}
	missing := activityDescriptor{EntityType: domain.ActivityEntityTypeComment, EntityID: fmt.Sprintf("%026d", 999), OccurredAt: now}
	_, err = loadActivityItems(ctx, &countingActivityQueryer{DB: db}, issueID, []activityDescriptor{missing})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStorageCorrupt || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing payload error = %v, want storage corrupt wrapping sql.ErrNoRows", err)
	}
	missing.EntityType = domain.ActivityEntityTypeReview
	_, err = loadActivityItems(ctx, &countingActivityQueryer{DB: db}, issueID, []activityDescriptor{missing})
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStorageCorrupt || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing review error = %v, want storage corrupt wrapping sql.ErrNoRows", err)
	}
	missing.EntityType = domain.ActivityEntityTypeGateEvidence
	_, err = loadActivityItems(ctx, &countingActivityQueryer{DB: db}, issueID, []activityDescriptor{missing})
	domainErr = nil
	if !errors.Is(err, sql.ErrNoRows) || errors.As(err, &domainErr) {
		t.Fatalf("missing gate evidence error = %v, want original sql.ErrNoRows", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE comments SET content = '' WHERE id = ?`, fmt.Sprintf("%026d", 2)); err != nil {
		t.Fatal(err)
	}
	_, err = loadActivityItems(ctx, &countingActivityQueryer{DB: db}, issueID, []activityDescriptor{
		{EntityType: domain.ActivityEntityTypeEvent, EntityID: "2", OccurredAt: now},
		{EntityType: domain.ActivityEntityTypeComment, EntityID: fmt.Sprintf("%026d", 2), OccurredAt: now},
	})
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStorageCorrupt || len(domainErr.Details) != 1 || domainErr.Details[0].Field != "content" {
		t.Fatalf("corrupt payload error = %v, want content corruption", err)
	}
}
