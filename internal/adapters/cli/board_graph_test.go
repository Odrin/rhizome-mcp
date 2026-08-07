package cli

import (
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestBoardGraphDeterministicLayersAndWrapping(t *testing.T) {
	graph := domain.GraphResult{
		Nodes: []domain.IssueProjection{
			{Issue: domain.Issue{ID: "a", DisplayID: "ISSUE-1"}, EffectiveStatus: domain.EffectiveStatusReady},
			{Issue: domain.Issue{ID: "b", DisplayID: "ISSUE-2"}, EffectiveStatus: domain.EffectiveStatusBlocked},
			{Issue: domain.Issue{ID: "c", DisplayID: "ISSUE-3"}, EffectiveStatus: domain.EffectiveStatusReview},
			{Issue: domain.Issue{ID: "d", DisplayID: "ISSUE-4"}, EffectiveStatus: domain.EffectiveStatusDone},
			{Issue: domain.Issue{ID: "e", DisplayID: "ISSUE-5"}, EffectiveStatus: domain.EffectiveStatusInProgress},
			{Issue: domain.Issue{ID: "f", DisplayID: "ISSUE-6"}, EffectiveStatus: domain.EffectiveStatusReady},
			{Issue: domain.Issue{ID: "g", DisplayID: "ISSUE-7"}, EffectiveStatus: domain.EffectiveStatusReady},
			{Issue: domain.Issue{ID: "h", DisplayID: "ISSUE-8"}, EffectiveStatus: domain.EffectiveStatusReady},
			{Issue: domain.Issue{ID: "i", DisplayID: "ISSUE-9"}, EffectiveStatus: domain.EffectiveStatusReady},
			{Issue: domain.Issue{ID: "j", DisplayID: "ISSUE-10"}, EffectiveStatus: domain.EffectiveStatusReady},
		},
		Edges: []domain.GraphEdge{{SourceIssueID: "a", TargetIssueID: "b", Type: "contains"}, {SourceIssueID: "a", TargetIssueID: "c", Type: "blocks"}},
	}

	layers := boardGraphLayers(graph)
	if got, want := layers["a"], 0; got != want {
		t.Fatalf("layer(a) = %d, want %d", got, want)
	}
	if got, want := layers["b"], 1; got != want {
		t.Fatalf("layer(b) = %d, want %d", got, want)
	}
	if got, want := layers["c"], 1; got != want {
		t.Fatalf("layer(c) = %d, want %d", got, want)
	}
	if got, want := layers["d"], 0; got != want {
		t.Fatalf("layer(d) = %d, want %d", got, want)
	}

	svg := renderBoardGraphSVG(graph)
	if !strings.Contains(svg, `y="24"`) {
		t.Fatalf("expected wrapped first row to render at y=24: %s", svg)
	}
	if !strings.Contains(svg, `y="124"`) {
		t.Fatalf("expected wrapped second row to render at y=124: %s", svg)
	}
}

func TestBoardGraphCycleFallbackAndLinkBehavior(t *testing.T) {
	graph := domain.GraphResult{
		Nodes: []domain.IssueProjection{{Issue: domain.Issue{ID: "x", DisplayID: "ISSUE-20"}, EffectiveStatus: domain.EffectiveStatusReady}, {Issue: domain.Issue{ID: "y", DisplayID: "ISSUE-21"}, EffectiveStatus: domain.EffectiveStatusBlocked}},
		Edges: []domain.GraphEdge{{SourceIssueID: "x", TargetIssueID: "y", Type: "contains"}, {SourceIssueID: "y", TargetIssueID: "x", Type: "blocks"}},
	}

	layers := boardGraphLayers(graph)
	for _, nodeID := range []string{"x", "y"} {
		if got := layers[nodeID]; got != 1 {
			t.Fatalf("cycle node %s layer = %d, want 1", nodeID, got)
		}
	}

	staticSVG := renderBoardGraphSVG(graph)
	if strings.Contains(staticSVG, `<a href="/issues/ISSUE-20"`) {
		t.Fatalf("static graph unexpectedly linked node: %s", staticSVG)
	}
	if strings.Contains(staticSVG, `aria-label="ISSUE-20"`) {
		t.Fatalf("static graph unexpectedly included aria-label for node: %s", staticSVG)
	}

	servedSVG := renderServedBoardGraphSVG(graph)
	if !strings.Contains(servedSVG, `<a href="/issues/ISSUE-20" aria-label="ISSUE-20">`) {
		t.Fatalf("served graph missing linked node markup: %s", servedSVG)
	}
	if !strings.Contains(servedSVG, `href="/issues/ISSUE-21"`) {
		t.Fatalf("served graph missing second node link: %s", servedSVG)
	}
}

func TestBoardGraphUnicodeTruncationAndEmptyState(t *testing.T) {
	if got, want := truncateBoardGraphLabel("こんにちは世界", 4), "こんに…"; got != want {
		t.Fatalf("truncateBoardGraphLabel() = %q, want %q", got, want)
	}

	emptySVG := renderBoardGraphSVG(domain.GraphResult{})
	if !strings.Contains(emptySVG, "No planning graph nodes.") {
		t.Fatalf("empty graph SVG missing empty-state text: %s", emptySVG)
	}
	if !strings.Contains(emptySVG, `aria-label="Empty planning graph"`) {
		t.Fatalf("empty graph SVG missing empty-state aria-label: %s", emptySVG)
	}
}
