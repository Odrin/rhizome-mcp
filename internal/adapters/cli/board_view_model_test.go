package cli

import (
	"bytes"
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

	// Asserted on the rendered table, so this fails if writeBoardTable stops
	// printing the docs/06 §7 truncation marker.
	var stdout bytes.Buffer
	cli := New(Services{}, &stdout, nil, nil, nil)
	if err := cli.writeBoardTable(result); err != nil {
		t.Fatalf("writeBoardTable: %v", err)
	}
	if !strings.Contains(stdout.String(), "truncated\ttrue\t(2 retained)") {
		t.Fatalf("table output = %q, want a truncated row with the retained count", stdout.String())
	}

	response := boardResponseFromDomain(result)
	if !response.PlanningGraph.Truncated {
		t.Fatal("expected Truncated to be true in BoardResponse")
	}
	if response.PlanningGraph.RetainedNodeCount != 2 {
		t.Fatalf("expected RetainedNodeCount to be 2, got %d", response.PlanningGraph.RetainedNodeCount)
	}
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

	var stdout bytes.Buffer
	cli := New(Services{}, &stdout, nil, nil, nil)
	if err := cli.writeBoardTable(result); err != nil {
		t.Fatalf("writeBoardTable: %v", err)
	}
	if strings.Contains(stdout.String(), "truncated") {
		t.Fatalf("table output = %q, want no truncation row", stdout.String())
	}

	response := boardResponseFromDomain(result)
	if response.PlanningGraph.Truncated {
		t.Fatal("expected Truncated to be false in BoardResponse")
	}
	if response.PlanningGraph.RetainedNodeCount != 2 {
		t.Fatalf("expected RetainedNodeCount to be 2, got %d", response.PlanningGraph.RetainedNodeCount)
	}
}
