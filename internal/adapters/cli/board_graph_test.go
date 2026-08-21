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

func TestBoardIssueLinkUsesDisplayIDAndEscapesLabel(t *testing.T) {
	got := boardIssueLink("01ARZ3NDEKTSV4RRFFQ69G5FAV", "ISSUE-<script>")
	want := `<a href="/issues/ISSUE-%3Cscript%3E">ISSUE-&lt;script&gt;</a>`
	if got != want {
		t.Fatalf("boardIssueLink() = %q, want %q", got, want)
	}
}

func TestBoardIssueLinkForProjectionFallsBackToInternalID(t *testing.T) {
	node := domain.IssueProjection{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
	got := boardIssueLinkForProjection(node, nil)
	if !strings.Contains(got, "01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Fatalf("boardIssueLinkForProjection() = %q, want it to fall back to the internal ID when no display ID or mapping is available", got)
	}
}

func TestIssueDisplayIDForProjectionPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		node    domain.IssueProjection
		mapping map[string]string
		want    string
	}{
		{
			name: "uses the issue's DisplayID when set",
			node: domain.IssueProjection{Issue: domain.Issue{ID: "id-1", DisplayID: "ISSUE-2"}},
			want: "ISSUE-2",
		},
		{
			name: "trims surrounding whitespace from the DisplayID",
			node: domain.IssueProjection{Issue: domain.Issue{ID: "id-1", DisplayID: "  ISSUE-2  "}},
			want: "ISSUE-2",
		},
		{
			name:    "falls back to the mapping by internal ID",
			node:    domain.IssueProjection{Issue: domain.Issue{ID: "id-1"}},
			mapping: map[string]string{"id-1": " ISSUE-3 "},
			want:    "ISSUE-3",
		},
		{
			name: "empty when nothing resolves",
			node: domain.IssueProjection{Issue: domain.Issue{ID: "id-1"}},
			want: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := issueDisplayIDForProjection(test.node, test.mapping); got != test.want {
				t.Errorf("issueDisplayIDForProjection() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestIssueDisplayIDName(t *testing.T) {
	if got := issueDisplayIDName("id-1", nil); got != "" {
		t.Errorf("issueDisplayIDName(nil mapping) = %q, want empty", got)
	}
	if got := issueDisplayIDName("id-1", map[string]string{"id-2": "ISSUE-2"}); got != "" {
		t.Errorf("issueDisplayIDName(missing key) = %q, want empty", got)
	}
	if got := issueDisplayIDName("id-1", map[string]string{"id-1": " ISSUE-1 "}); got != "ISSUE-1" {
		t.Errorf("issueDisplayIDName() = %q, want trimmed ISSUE-1", got)
	}
}

func TestBoardGraphNodeSVGRendersNonLinkableNode(t *testing.T) {
	node := domain.IssueProjection{
		EffectiveStatus: domain.EffectiveStatusInProgress,
		Issue:           domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-9", Title: "A title"},
	}
	svg := boardGraphNodeSVG(node, 10, 20)
	if !strings.Contains(svg, "<rect") || !strings.Contains(svg, "ISSUE-9") || !strings.Contains(svg, "A title") {
		t.Fatalf("boardGraphNodeSVG() = %q, want a rect containing the display ID and title", svg)
	}
	if strings.Contains(svg, "<a href") {
		t.Fatalf("boardGraphNodeSVG() = %q, want no link wrapper (boardGraphNodeSVG is never linkable)", svg)
	}
}
