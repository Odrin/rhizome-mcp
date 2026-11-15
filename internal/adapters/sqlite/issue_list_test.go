package sqlite_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand"
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
)

func TestListIssuesFiltersComputedFieldsOrderingAndLabels(t *testing.T) {
	service, _, _ := openIssueService(t)
	ctx := context.Background()
	epic, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeEpic, Title: "Epic", Labels: []string{"platform"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := epic.ID
	critical, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeBug, Title: "critical", Priority: domain.PriorityCritical,
		Labels: []string{"frontend"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "ready", Status: domain.StatusReady, Priority: domain.PriorityHigh,
		ParentID: &parent, Labels: []string{"backend"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	open, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "open", Priority: domain.PriorityHigh,
		ParentID: &parent, Labels: []string{"frontend", "backend"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	blockedReason := "waiting"
	blocked, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeBug, Title: "blocked", Status: domain.StatusBlocked,
		BlockedReason: &blockedReason, Priority: domain.PriorityMedium,
	})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "archived", Status: domain.StatusReady,
		Priority: domain.PriorityCritical,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: archived.ID, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}

	page, err := service.ListIssues(ctx, domain.ListIssuesInput{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 || page.HasMore || page.NextCursor != nil {
		t.Fatalf("default page = %#v", page)
	}
	gotIDs := make([]string, len(page.Items))
	for index, item := range page.Items {
		gotIDs[index] = item.ID
		if item.Labels == nil {
			t.Fatalf("item %s labels is nil", item.ID)
		}
	}
	wantIDs := []string{critical.ID, ready.ID, open.ID, epic.ID, blocked.ID}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("ordered IDs = %v, want %v", gotIDs, wantIDs)
	}
	if !page.Items[1].IsClaimable || page.Items[1].EffectiveStatus != domain.EffectiveStatusReady ||
		page.Items[4].IsBlocked == false || page.Items[4].IsClaimable {
		t.Fatalf("computed fields = %#v", page.Items)
	}
	if got := labelNames(page.Items[2].Labels); !reflect.DeepEqual(got, []string{"backend", "frontend"}) {
		t.Fatalf("labels = %v", got)
	}

	included, err := service.ListIssues(ctx, domain.ListIssuesInput{IncludeArchived: true, Types: []domain.Type{domain.TypeTask}})
	if err != nil {
		t.Fatal(err)
	}
	if len(included.Items) != 3 || included.Items[0].ID != archived.ID || included.Items[0].IsClaimable {
		t.Fatalf("archived inclusion = %#v", included)
	}
	for _, test := range []struct {
		name  string
		input domain.ListIssuesInput
		want  string
	}{
		{"type", domain.ListIssuesInput{Types: []domain.Type{domain.TypeBug}}, blocked.ID},
		{"status", domain.ListIssuesInput{Statuses: []domain.Status{domain.StatusReady}}, ready.ID},
		{"effective status", domain.ListIssuesInput{EffectiveStatuses: []domain.EffectiveStatus{domain.EffectiveStatusBlocked}}, blocked.ID},
		{"priority", domain.ListIssuesInput{Priorities: []domain.Priority{domain.PriorityCritical}}, critical.ID},
		{"any label", domain.ListIssuesInput{Labels: []string{" FRONTEND ", "missing"}}, critical.ID},
		{"parent", domain.ListIssuesInput{ParentIssueID: stringPointer(parent)}, ready.ID},
		{"blocked", domain.ListIssuesInput{IsBlocked: boolPointer(true)}, blocked.ID},
		{"claimable", domain.ListIssuesInput{IsClaimable: boolPointer(true)}, ready.ID},
	} {
		t.Run(test.name, func(t *testing.T) {
			filtered, err := service.ListIssues(ctx, test.input)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range filtered.Items {
				if item.ID == test.want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("filtered = %#v, want item %s", filtered.Items, test.want)
			}
		})
	}
}

// TestIssueRepositoryCountIssuesByEffectiveStatusExcludesArchivedAndGroupsCorrectly
// covers the board's status-summary aggregate (application.IssueService ->
// IssueRepository.CountIssuesByEffectiveStatus), never directly exercised
// by a repository-level test before (board_service_test.go only exercises
// it through a stub repository).
func TestIssueRepositoryCountIssuesByEffectiveStatusExcludesArchivedAndGroupsCorrectly(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()

	readyOne, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ready one", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ready two", Status: domain.StatusReady}); err != nil {
		t.Fatal(err)
	}
	blockedReason := "waiting"
	if _, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeBug, Title: "blocked one", Status: domain.StatusBlocked, BlockedReason: &blockedReason}); err != nil {
		t.Fatal(err)
	}
	archived, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "archived, must be excluded", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: archived.ID, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}

	attemptRepository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatal(err)
	}
	attemptService, err := application.NewAttemptService(attemptRepository, clock.NewFakeClock(now), generator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attemptService.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: readyOne.ID}); err != nil {
		t.Fatal(err)
	}

	repository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := repository.CountIssuesByEffectiveStatus(ctx, ports.CountIssuesByEffectiveStatusCommand{Now: now})
	if err != nil {
		t.Fatalf("CountIssuesByEffectiveStatus() error = %v", err)
	}

	byStatus := make(map[domain.EffectiveStatus]int64, len(counts))
	var total int64
	for _, count := range counts {
		byStatus[count.EffectiveStatus] = count.Count
		total += count.Count
	}
	if byStatus[domain.EffectiveStatusReady] != 1 {
		t.Errorf("ready count = %d, want 1 (readyOne is now in_progress, only ready two remains ready)", byStatus[domain.EffectiveStatusReady])
	}
	if byStatus[domain.EffectiveStatusInProgress] != 1 {
		t.Errorf("in_progress count = %d, want 1", byStatus[domain.EffectiveStatusInProgress])
	}
	if byStatus[domain.EffectiveStatusBlocked] != 1 {
		t.Errorf("blocked count = %d, want 1", byStatus[domain.EffectiveStatusBlocked])
	}
	if total != 3 {
		t.Fatalf("total counted = %d, want 3 (the archived issue must be excluded)", total)
	}

	serviceCounts, err := service.CountIssuesByEffectiveStatus(ctx)
	if err != nil {
		t.Fatalf("IssueService.CountIssuesByEffectiveStatus() error = %v", err)
	}
	if len(serviceCounts) != len(counts) {
		t.Fatalf("service-layer counts = %#v, want the same shape as the repository result %#v", serviceCounts, counts)
	}
}

// TestIssueRepositoryGetIssueProjectionMatchesListIssuesAndReportsNotFound
// covers GetIssueProjection directly (previously only reached indirectly
// through ClaimIssue's post-claim projection and the CLI's issue-show path,
// ISSUE-190/ISSUE-208), by both internal ID and display ID, and its
// not-found path.
func TestIssueRepositoryGetIssueProjectionMatchesListIssuesAndReportsNotFound(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()

	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "projected issue", Status: domain.StatusReady, Labels: []string{"alpha"}, CreateMissingLabels: true})
	if err != nil {
		t.Fatal(err)
	}

	repository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	byID, err := domain.ParseIssueIdentifier(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := repository.GetIssueProjection(ctx, ports.GetIssueProjectionCommand{Identifier: byID, Now: now})
	if err != nil {
		t.Fatalf("GetIssueProjection(by ID) error = %v", err)
	}
	if projection.ID != issue.ID || projection.EffectiveStatus != domain.EffectiveStatusReady || !projection.IsClaimable {
		t.Fatalf("projection = %#v, want a claimable, ready projection for %s", projection, issue.ID)
	}
	if len(projection.Labels) != 1 || projection.Labels[0].Name != "alpha" {
		t.Fatalf("projection labels = %#v, want [alpha]", projection.Labels)
	}

	byDisplayID, err := domain.ParseIssueIdentifier(issue.DisplayID)
	if err != nil {
		t.Fatal(err)
	}
	byDisplay, err := repository.GetIssueProjection(ctx, ports.GetIssueProjectionCommand{Identifier: byDisplayID, Now: now})
	if err != nil {
		t.Fatalf("GetIssueProjection(by display ID) error = %v", err)
	}
	if byDisplay.ID != issue.ID {
		t.Fatalf("projection by display id = %#v, want the same issue %s", byDisplay, issue.ID)
	}

	missing, err := domain.ParseIssueIdentifier("01ARZ3NDEKTSV4RRFFQ69G5FAZ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetIssueProjection(ctx, ports.GetIssueProjectionCommand{Identifier: missing, Now: now}); !errors.Is(err, &domain.Error{Code: domain.CodeIssueNotFound}) {
		t.Fatalf("GetIssueProjection(missing) error = %v, want ISSUE_NOT_FOUND", err)
	}

	serviceProjection, err := service.GetIssueProjection(ctx, issue.ID)
	if err != nil {
		t.Fatalf("IssueService.GetIssueProjection() error = %v", err)
	}
	if serviceProjection.ID != issue.ID {
		t.Fatalf("service-layer projection = %#v, want issue %s", serviceProjection, issue.ID)
	}
}

func TestListIssuesCursorTraversalAndCursorErrors(t *testing.T) {
	service, _, _ := openIssueService(t)
	ctx := context.Background()
	priorities := []domain.Priority{
		domain.PriorityLow, domain.PriorityMedium, domain.PriorityHigh, domain.PriorityCritical,
	}
	for index := 0; index < 7; index++ {
		if _, err := service.CreateIssue(ctx, domain.CreateIssueInput{
			Type: domain.TypeTask, Title: "issue", Priority: priorities[index%len(priorities)],
		}); err != nil {
			t.Fatal(err)
		}
	}
	var all []string
	var cursor string
	for {
		page, err := service.ListIssues(ctx, domain.ListIssuesInput{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			all = append(all, item.ID)
		}
		if !page.HasMore {
			break
		}
		cursor = *page.NextCursor
	}
	if len(all) != 7 {
		t.Fatalf("traversal returned %d items: %v", len(all), all)
	}
	seen := make(map[string]bool)
	for _, id := range all {
		if seen[id] {
			t.Fatalf("duplicate item %s", id)
		}
		seen[id] = true
	}
	for _, cursor := range []string{"%%%", strings.Repeat("a", 4097), unsupportedCursor()} {
		_, err := service.ListIssues(ctx, domain.ListIssuesInput{Cursor: cursor})
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument ||
			len(domainErr.Details) != 1 || !strings.Contains(domainErr.Details[0].Code, "CURSOR") {
			t.Fatalf("cursor %q error = %#v", cursor, err)
		}
	}
	empty, err := service.ListIssues(ctx, domain.ListIssuesInput{Types: []domain.Type{domain.TypeEpic}})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Items == nil || len(empty.Items) != 0 || empty.HasMore || empty.NextCursor != nil {
		t.Fatalf("empty page = %#v", empty)
	}
}

func labelNames(labels []domain.Label) []string {
	names := make([]string, len(labels))
	for index, label := range labels {
		names[index] = label.Name
	}
	return names
}

func stringPointer(value string) *string { return &value }
func boolPointer(value bool) *bool       { return &value }

func unsupportedCursor() string {
	raw, _ := json.Marshal(map[string]any{
		"version": 99,
		"payload": map[string]any{"priority_rank": 1, "is_claimable": false, "sequence_no": 1},
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func encodeIssueTestCursor(payload map[string]any) string {
	raw, _ := json.Marshal(map[string]any{
		"version": 1,
		"payload": payload,
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func openIssueServiceWithClock(t *testing.T) (*application.IssueService, *sqlite.DB, *clock.FakeClock) {
	t.Helper()
	now := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.UTC)
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "issues-sort.db"), sqlite.Options{})
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
		timestamp := sqlite.FormatStorageTime(now)
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
	generator, err := ids.NewGenerator(fakeClock, rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	service, err := application.NewIssueService(repository, fakeClock, generator)
	if err != nil {
		t.Fatalf("NewIssueService() error = %v", err)
	}
	return service, db, fakeClock
}

func collectAllPages(t *testing.T, service *application.IssueService, base domain.ListIssuesInput) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	cursor := base.Cursor
	for {
		input := base
		input.Cursor = cursor
		page, err := service.ListIssues(ctx, input)
		if err != nil {
			t.Fatalf("ListIssues(%#v) error = %v", input, err)
		}
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		if !page.HasMore {
			if page.NextCursor != nil {
				t.Fatalf("HasMore=false but NextCursor=%q", *page.NextCursor)
			}
			break
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			t.Fatalf("HasMore=true but NextCursor=%v", page.NextCursor)
		}
		cursor = *page.NextCursor
	}
	return ids
}

func TestListIssuesSortModesPaginationTieBreakersAndCursors(t *testing.T) {
	service, db, fakeClock := openIssueServiceWithClock(t)
	ctx := context.Background()

	// Issue 1 (seq 1): low priority, created at T0.
	first, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "first-low", Priority: domain.PriorityLow, Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Issue 2 & 3 (seq 2, 3): share exact same created_at (T0 + 1m) to test created_desc sequence_no DESC tie-breaker.
	fakeClock.Advance(time.Minute)
	second, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "second-critical", Priority: domain.PriorityCritical, Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "third-high-ready", Priority: domain.PriorityHigh, Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Issue 4 (seq 4): medium priority, created at T0 + 2m.
	fakeClock.Advance(time.Minute)
	fourth, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "fourth-medium", Priority: domain.PriorityMedium, Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Issue 5 (seq 5): low priority, created at T0 + 3m.
	fakeClock.Advance(time.Minute)
	fifth, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "fifth-low", Priority: domain.PriorityLow, Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Update first (seq 1) and fourth (seq 4) at the same timestamp (T0 + 4m) to test updated_desc ordering + tie-breaker,
	// then update second (seq 2) at T0 + 5m so updated_desc puts seq 2 first, then seq 4 before seq 1 (same updated_at), then seq 5, then seq 3.
	fakeClock.Advance(time.Minute)
	if _, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: first.ID, ExpectedVersion: 1, Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "first-low-updated"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: fourth.ID, ExpectedVersion: 1, Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "fourth-medium-updated"}},
	}); err != nil {
		t.Fatal(err)
	}
	fakeClock.Advance(time.Minute)
	if _, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: second.ID, ExpectedVersion: 1, Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "second-critical-updated"}},
	}); err != nil {
		t.Fatal(err)
	}

	wantOrders := []struct {
		sort domain.IssueSortOrder
		want []string
	}{
		{
			sort: domain.IssueSortPriority,
			// critical(seq2), high(seq3), medium(seq4), low+claimable(seq1), low+not-claimable(seq5)
			want: []string{second.ID, third.ID, fourth.ID, first.ID, fifth.ID},
		},
		{
			sort: domain.IssueSortCreatedDesc,
			// T0+3m(seq5), T0+2m(seq4), T0+1m tie broken by seq DESC (seq3, seq2), T0(seq1)
			want: []string{fifth.ID, fourth.ID, third.ID, second.ID, first.ID},
		},
		{
			sort: domain.IssueSortUpdatedDesc,
			// T0+5m(seq2), T0+4m tie broken by seq DESC (seq4, seq1), T0+3m(seq5), T0+1m(seq3)
			want: []string{second.ID, fourth.ID, first.ID, fifth.ID, third.ID},
		},
		{
			sort: domain.IssueSortSequenceDesc,
			want: []string{fifth.ID, fourth.ID, third.ID, second.ID, first.ID},
		},
	}

	cursorsBySort := make(map[domain.IssueSortOrder]string, len(wantOrders))
	for _, tc := range wantOrders {
		t.Run(string(tc.sort), func(t *testing.T) {
			got := collectAllPages(t, service, domain.ListIssuesInput{Limit: 2, Sort: tc.sort})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sort %s paginated IDs = %v, want %v", tc.sort, got, tc.want)
			}
			// Also test Limit=1 to ensure boundary cursors across every single item (including exact timestamp ties).
			gotSingle := collectAllPages(t, service, domain.ListIssuesInput{Limit: 1, Sort: tc.sort})
			if !reflect.DeepEqual(gotSingle, tc.want) {
				t.Fatalf("sort %s single-item paginated IDs = %v, want %v", tc.sort, gotSingle, tc.want)
			}
			firstPage, err := service.ListIssues(ctx, domain.ListIssuesInput{Limit: 2, Sort: tc.sort})
			if err != nil || firstPage.NextCursor == nil {
				t.Fatalf("first page for %s failed: page=%#v err=%v", tc.sort, firstPage, err)
			}
			cursorsBySort[tc.sort] = *firstPage.NextCursor
		})
	}

	// Legacy priority cursor (without "sort" key) remains compatible with default/priority sort
	// and is rejected with INVALID_CURSOR when supplied for any other sort mode.
	legacyPriorityCursor := encodeIssueTestCursor(map[string]any{
		"priority_rank": 4,
		"is_claimable":  false,
		"sequence_no":   second.SequenceNo,
	})
	legacyResumed, err := service.ListIssues(ctx, domain.ListIssuesInput{Limit: 10, Cursor: legacyPriorityCursor})
	if err != nil {
		t.Fatalf("legacy priority cursor failed: %v", err)
	}
	wantAfterSecond := []string{third.ID, fourth.ID, first.ID, fifth.ID}
	gotAfterSecond := make([]string, len(legacyResumed.Items))
	for index, item := range legacyResumed.Items {
		gotAfterSecond[index] = item.ID
	}
	if !reflect.DeepEqual(gotAfterSecond, wantAfterSecond) {
		t.Fatalf("legacy priority cursor items = %v, want %v", gotAfterSecond, wantAfterSecond)
	}

	// Mismatched cursor sort must fail with INVALID_ARGUMENT and detail code INVALID_CURSOR naming the mismatch.
	mismatchCases := []struct {
		name       string
		cursor     string
		targetSort domain.IssueSortOrder
		wantFrom   string
		wantTo     string
	}{
		{name: "legacy priority cursor with created_desc", cursor: legacyPriorityCursor, targetSort: domain.IssueSortCreatedDesc, wantFrom: "priority", wantTo: "created_desc"},
		{name: "priority cursor with updated_desc", cursor: cursorsBySort[domain.IssueSortPriority], targetSort: domain.IssueSortUpdatedDesc, wantFrom: "priority", wantTo: "updated_desc"},
		{name: "created_desc cursor with default priority", cursor: cursorsBySort[domain.IssueSortCreatedDesc], targetSort: "", wantFrom: "created_desc", wantTo: "priority"},
		{name: "updated_desc cursor with sequence_desc", cursor: cursorsBySort[domain.IssueSortUpdatedDesc], targetSort: domain.IssueSortSequenceDesc, wantFrom: "updated_desc", wantTo: "sequence_desc"},
		{name: "sequence_desc cursor with created_desc", cursor: cursorsBySort[domain.IssueSortSequenceDesc], targetSort: domain.IssueSortCreatedDesc, wantFrom: "sequence_desc", wantTo: "created_desc"},
	}
	for _, tc := range mismatchCases {
		t.Run("mismatch_"+tc.name, func(t *testing.T) {
			_, err := service.ListIssues(ctx, domain.ListIssuesInput{Sort: tc.targetSort, Cursor: tc.cursor})
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument ||
				len(domainErr.Details) != 1 || domainErr.Details[0].Field != "cursor" ||
				domainErr.Details[0].Code != "INVALID_CURSOR" ||
				!strings.Contains(domainErr.Message, tc.wantFrom) || !strings.Contains(domainErr.Message, tc.wantTo) {
				t.Fatalf("mismatch error = %#v, want INVALID_ARGUMENT/INVALID_CURSOR mentioning %q and %q", err, tc.wantFrom, tc.wantTo)
			}
		})
	}

	// Malformed payload validation per sort mode.
	badPayloads := []struct {
		sort    domain.IssueSortOrder
		payload map[string]any
	}{
		{domain.IssueSortPriority, map[string]any{"sort": "bogus", "sequence_no": 1}},
		{domain.IssueSortCreatedDesc, map[string]any{"sort": "created_desc", "sequence_no": 1}},
		{domain.IssueSortCreatedDesc, map[string]any{"sort": "created_desc", "created_at": "not-a-time", "sequence_no": 1}},
		{domain.IssueSortUpdatedDesc, map[string]any{"sort": "updated_desc", "sequence_no": 1}},
		{domain.IssueSortUpdatedDesc, map[string]any{"sort": "updated_desc", "updated_at": "not-a-time", "sequence_no": 1}},
		{domain.IssueSortSequenceDesc, map[string]any{"sort": "sequence_desc", "sequence_no": 0}},
	}
	for _, tc := range badPayloads {
		_, err := service.ListIssues(ctx, domain.ListIssuesInput{Sort: tc.sort, Cursor: encodeIssueTestCursor(tc.payload)})
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument ||
			len(domainErr.Details) != 1 || domainErr.Details[0].Field != "cursor" ||
			domainErr.Details[0].Code != "MALFORMED_CURSOR" {
			t.Fatalf("bad payload %v for %s error = %#v, want MALFORMED_CURSOR", tc.payload, tc.sort, err)
		}
	}

	// Verify raw column timestamp strings are captured verbatim into cursors so un-normalized whole-second
	// timestamps (e.g. legacy "...Z" strings) match SQL memcmp without reformatting mismatch.
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET created_at = ? WHERE id = ?`, "2026-07-14T10:12:12Z", third.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rawPage1, err := service.ListIssues(ctx, domain.ListIssuesInput{Limit: 3, Sort: domain.IssueSortCreatedDesc})
	if err != nil || !rawPage1.HasMore || rawPage1.NextCursor == nil {
		t.Fatalf("raw timestamp page 1 = %#v, err=%v", rawPage1, err)
	}
	rawPage2, err := service.ListIssues(ctx, domain.ListIssuesInput{Limit: 3, Sort: domain.IssueSortCreatedDesc, Cursor: *rawPage1.NextCursor})
	if err != nil {
		t.Fatalf("raw timestamp page 2 error = %v", err)
	}
	var rawAll []string
	for _, item := range rawPage1.Items {
		rawAll = append(rawAll, item.ID)
	}
	for _, item := range rawPage2.Items {
		rawAll = append(rawAll, item.ID)
	}
	wantRawAll := []string{fifth.ID, fourth.ID, third.ID, second.ID, first.ID}
	if !reflect.DeepEqual(rawAll, wantRawAll) {
		t.Fatalf("raw timestamp pagination = %v, want %v", rawAll, wantRawAll)
	}
}
