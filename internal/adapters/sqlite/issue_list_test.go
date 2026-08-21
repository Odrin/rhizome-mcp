package sqlite_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
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
