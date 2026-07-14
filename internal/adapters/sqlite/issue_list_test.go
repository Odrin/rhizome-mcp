package sqlite_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
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
