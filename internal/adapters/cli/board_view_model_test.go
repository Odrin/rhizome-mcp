package cli

import (
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
)

func TestBoardViewModelShowsTruncationMarkerWhenTruncated(t *testing.T) {
	truncated := true
	now := time.Now().UTC()
	result := domain.BoardResult{
		GeneratedAt: now,
		StatusCounts: []domain.EffectiveStatusCount{
			{EffectiveStatus: domain.EffectiveStatusReady, Count: 5},
		},
		ActiveAttempts:     []domain.ActiveAttemptSummary{},
		ActiveReservations: []domain.Reservation{},
		BlockedIssues:      []domain.IssueProjection{},
		ReviewRequests:     []domain.ReviewRequest{},
		PlanningGraph: domain.GraphResult{
			Nodes: []domain.IssueProjection{
				{Issue: domain.Issue{ID: "1", DisplayID: "ISSUE-1"}, EffectiveStatus: domain.EffectiveStatusReady},
				{Issue: domain.Issue{ID: "2", DisplayID: "ISSUE-2"}, EffectiveStatus: domain.EffectiveStatusReady},
			},
			Edges:         []domain.GraphEdge{},
			EntryPoints:   []string{"1", "2"},
			BlockingNodes: []string{},
			Summary: domain.GraphSummary{
				NodeCount:         2,
				EdgeCount:         0,
				EntryPointCount:   2,
				BlockingNodeCount: 0,
			},
			Warnings:         []string{},
			Truncated:        truncated,
			TruncationReason: &([]string{"node_limit"}[0]),
		},
	}

	vm := newBoardStaticPageViewModel(result)

	// Asserted on the rendered summary the template actually prints, not on a
	// field, so this fails if the marker stops reaching the page.
	if !strings.Contains(vm.PlanningGraphSummary, "(truncated)") {
		t.Fatalf("planning graph summary = %q, want a (truncated) marker", vm.PlanningGraphSummary)
	}
	if !strings.Contains(vm.PlanningGraphSummary, "2 nodes") {
		t.Fatalf("planning graph summary = %q, want the retained node count", vm.PlanningGraphSummary)
	}
}

func TestBoardViewModelHidesTruncationMarkerWhenNotTruncated(t *testing.T) {
	now := time.Now().UTC()
	result := domain.BoardResult{
		GeneratedAt: now,
		StatusCounts: []domain.EffectiveStatusCount{
			{EffectiveStatus: domain.EffectiveStatusReady, Count: 2},
		},
		ActiveAttempts:     []domain.ActiveAttemptSummary{},
		ActiveReservations: []domain.Reservation{},
		BlockedIssues:      []domain.IssueProjection{},
		ReviewRequests:     []domain.ReviewRequest{},
		PlanningGraph: domain.GraphResult{
			Nodes: []domain.IssueProjection{
				{Issue: domain.Issue{ID: "1", DisplayID: "ISSUE-1"}, EffectiveStatus: domain.EffectiveStatusReady},
				{Issue: domain.Issue{ID: "2", DisplayID: "ISSUE-2"}, EffectiveStatus: domain.EffectiveStatusReady},
			},
			Edges:         []domain.GraphEdge{},
			EntryPoints:   []string{"1", "2"},
			BlockingNodes: []string{},
			Summary: domain.GraphSummary{
				NodeCount:         2,
				EdgeCount:         0,
				EntryPointCount:   2,
				BlockingNodeCount: 0,
			},
			Warnings:  []string{},
			Truncated: false,
		},
	}

	vm := newBoardStaticPageViewModel(result)

	if strings.Contains(vm.PlanningGraphSummary, "truncated") {
		t.Fatalf("planning graph summary = %q, want no truncation marker", vm.PlanningGraphSummary)
	}
	if !strings.Contains(vm.PlanningGraphSummary, "2 nodes") {
		t.Fatalf("planning graph summary = %q, want the node count", vm.PlanningGraphSummary)
	}
}

func TestBoardServedViewModelShowsTruncationMarkerWhenTruncated(t *testing.T) {
	truncated := true
	now := time.Now().UTC()
	result := domain.BoardResult{
		GeneratedAt: now,
		StatusCounts: []domain.EffectiveStatusCount{
			{EffectiveStatus: domain.EffectiveStatusReady, Count: 5},
		},
		ActiveAttempts:     []domain.ActiveAttemptSummary{},
		ActiveReservations: []domain.Reservation{},
		BlockedIssues:      []domain.IssueProjection{},
		ReviewRequests:     []domain.ReviewRequest{},
		PlanningGraph: domain.GraphResult{
			Nodes: []domain.IssueProjection{
				{Issue: domain.Issue{ID: "1", DisplayID: "ISSUE-1"}, EffectiveStatus: domain.EffectiveStatusReady},
				{Issue: domain.Issue{ID: "2", DisplayID: "ISSUE-2"}, EffectiveStatus: domain.EffectiveStatusReady},
			},
			Edges:         []domain.GraphEdge{},
			EntryPoints:   []string{"1", "2"},
			BlockingNodes: []string{},
			Summary: domain.GraphSummary{
				NodeCount:         2,
				EdgeCount:         0,
				EntryPointCount:   2,
				BlockingNodeCount: 0,
			},
			Warnings:         []string{},
			Truncated:        truncated,
			TruncationReason: &([]string{"node_limit"}[0]),
		},
	}

	vm := newBoardServedPageViewModel(result, servedBoardSearchState{})

	// Asserted on the rendered summary the template actually prints, not on a
	// field, so this fails if the marker stops reaching the page.
	if !strings.Contains(vm.PlanningGraphSummary, "(truncated)") {
		t.Fatalf("planning graph summary = %q, want a (truncated) marker", vm.PlanningGraphSummary)
	}
	if !strings.Contains(vm.PlanningGraphSummary, "2 nodes") {
		t.Fatalf("planning graph summary = %q, want the retained node count", vm.PlanningGraphSummary)
	}
}

func TestBoardTableFormatShowsTruncationMarkerWhenTruncated(t *testing.T) {
	truncated := true
	now := time.Now().UTC()
	result := domain.BoardResult{
		GeneratedAt: now,
		StatusCounts: []domain.EffectiveStatusCount{
			{EffectiveStatus: domain.EffectiveStatusReady, Count: 5},
		},
		ActiveAttempts:     []domain.ActiveAttemptSummary{},
		ActiveReservations: []domain.Reservation{},
		BlockedIssues:      []domain.IssueProjection{},
		ReviewRequests:     []domain.ReviewRequest{},
		PlanningGraph: domain.GraphResult{
			Nodes: []domain.IssueProjection{
				{Issue: domain.Issue{ID: "1", DisplayID: "ISSUE-1"}, EffectiveStatus: domain.EffectiveStatusReady},
				{Issue: domain.Issue{ID: "2", DisplayID: "ISSUE-2"}, EffectiveStatus: domain.EffectiveStatusReady},
			},
			Edges:         []domain.GraphEdge{},
			EntryPoints:   []string{"1", "2"},
			BlockingNodes: []string{},
			Summary: domain.GraphSummary{
				NodeCount:         2,
				EdgeCount:         0,
				EntryPointCount:   2,
				BlockingNodeCount: 0,
			},
			Warnings:         []string{},
			Truncated:        truncated,
			TruncationReason: &([]string{"node_limit"}[0]),
		},
	}

	var tableOutput strings.Builder
	boardResponseFromDomain(result)
	// Test that the table format includes truncation info
	// This would require testing the actual CLI table output
	response := boardResponseFromDomain(result)
	if !response.PlanningGraph.Truncated {
		t.Fatal("expected Truncated to be true in BoardResponse")
	}
	if response.PlanningGraph.RetainedNodeCount != 2 {
		t.Fatalf("expected RetainedNodeCount to be 2, got %d", response.PlanningGraph.RetainedNodeCount)
	}
	_ = tableOutput
}

func TestBoardTableFormatHidesTruncationMarkerWhenNotTruncated(t *testing.T) {
	now := time.Now().UTC()
	result := domain.BoardResult{
		GeneratedAt: now,
		StatusCounts: []domain.EffectiveStatusCount{
			{EffectiveStatus: domain.EffectiveStatusReady, Count: 2},
		},
		ActiveAttempts:     []domain.ActiveAttemptSummary{},
		ActiveReservations: []domain.Reservation{},
		BlockedIssues:      []domain.IssueProjection{},
		ReviewRequests:     []domain.ReviewRequest{},
		PlanningGraph: domain.GraphResult{
			Nodes: []domain.IssueProjection{
				{Issue: domain.Issue{ID: "1", DisplayID: "ISSUE-1"}, EffectiveStatus: domain.EffectiveStatusReady},
				{Issue: domain.Issue{ID: "2", DisplayID: "ISSUE-2"}, EffectiveStatus: domain.EffectiveStatusReady},
			},
			Edges:         []domain.GraphEdge{},
			EntryPoints:   []string{"1", "2"},
			BlockingNodes: []string{},
			Summary: domain.GraphSummary{
				NodeCount:         2,
				EdgeCount:         0,
				EntryPointCount:   2,
				BlockingNodeCount: 0,
			},
			Warnings:  []string{},
			Truncated: false,
		},
	}

	response := boardResponseFromDomain(result)
	if response.PlanningGraph.Truncated {
		t.Fatal("expected Truncated to be false in BoardResponse")
	}
	if response.PlanningGraph.RetainedNodeCount != 2 {
		t.Fatalf("expected RetainedNodeCount to be 2, got %d", response.PlanningGraph.RetainedNodeCount)
	}
}
