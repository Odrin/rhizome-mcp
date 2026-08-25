package domain_test

import (
	"fmt"
	"testing"

	"rhizome-mcp/internal/domain"
)

// planningNode builds one snapshot node. Claimable is derived the way the
// planning projection derives it: only non-terminal work is ever claimable.
func planningNode(id string, status domain.Status, claimable bool) domain.IssueProjection {
	return domain.IssueProjection{
		Issue:       domain.Issue{ID: id, DisplayID: id, Title: id, Status: status},
		IsClaimable: claimable,
	}
}

func planningTraversal(snapshot domain.GraphSnapshot, maxNodes int, includeTerminal bool) domain.GraphTraversal {
	roots := make([]string, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		roots = append(roots, node.ID)
	}
	return domain.GraphTraversal{
		RootIssueIDs:      roots,
		Depth:             3,
		MaxNodes:          maxNodes,
		Direction:         domain.GraphDirectionBoth,
		RelationTypes:     []domain.RelationType{domain.RelationTypeBlocks},
		IncludeHierarchy:  true,
		IncludeTerminal:   includeTerminal,
		PreferNonTerminal: true,
	}
}

func nodeIDs(result domain.GraphResult) []string {
	ids := make([]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestBuildGraphPlanningPrefersNonTerminalNodes is the ISSUE-219 regression:
// with 150 done and 10 ready issues and the default 100-node budget, the old
// order-of-discovery selection filled the budget with finished work and
// reported zero entry points.
func TestBuildGraphPlanningPrefersNonTerminalNodes(t *testing.T) {
	var nodes []domain.IssueProjection
	for index := 0; index < 150; index++ {
		nodes = append(nodes, planningNode(fmt.Sprintf("done-%03d", index), domain.StatusDone, false))
	}
	for index := 0; index < 10; index++ {
		nodes = append(nodes, planningNode(fmt.Sprintf("ready-%03d", index), domain.StatusReady, true))
	}
	snapshot := domain.GraphSnapshot{Nodes: nodes}

	result := domain.BuildGraph(snapshot, planningTraversal(snapshot, 100, true))

	if len(result.EntryPoints) != 10 {
		t.Fatalf("entry_points = %d, want all 10 ready issues (got %v)", len(result.EntryPoints), result.EntryPoints)
	}
	for index := 0; index < 10; index++ {
		want := fmt.Sprintf("ready-%03d", index)
		if !containsID(result.EntryPoints, want) {
			t.Fatalf("entry_points missing %q: %v", want, result.EntryPoints)
		}
	}
	if result.Summary.EntryPointCount != 10 {
		t.Fatalf("summary.entry_point_count = %d, want 10", result.Summary.EntryPointCount)
	}

	// Every ready issue must be retained: non-terminal work claims the budget
	// before any terminal node is considered.
	retained := nodeIDs(result)
	for index := 0; index < 10; index++ {
		want := fmt.Sprintf("ready-%03d", index)
		if !containsID(retained, want) {
			t.Fatalf("ready issue %q was dropped from nodes", want)
		}
	}
	if len(result.Nodes) > 100 {
		t.Fatalf("node count = %d, want at most the 100-node cap", len(result.Nodes))
	}
	if !result.Truncated {
		t.Fatal("truncated = false, want true: 160 candidates do not fit a 100-node cap")
	}
	if result.TruncationReason == nil || *result.TruncationReason != "node_limit" {
		t.Fatalf("truncation_reason = %v, want node_limit", result.TruncationReason)
	}
}

// TestBuildGraphPlanningKeepsTerminalNodesOnlyAsEndpoints proves the second
// pass: a done issue is retained when an edge attaches it to retained work, and
// dropped when it is isolated, even though budget remains.
func TestBuildGraphPlanningKeepsTerminalNodesOnlyAsEndpoints(t *testing.T) {
	snapshot := domain.GraphSnapshot{
		Nodes: []domain.IssueProjection{
			planningNode("ready-1", domain.StatusReady, true),
			planningNode("done-attached", domain.StatusDone, false),
			planningNode("done-isolated", domain.StatusDone, false),
		},
		Edges: []domain.GraphEdge{
			{SourceIssueID: "done-attached", TargetIssueID: "ready-1", Type: string(domain.RelationTypeBlocks)},
		},
	}

	result := domain.BuildGraph(snapshot, planningTraversal(snapshot, 100, true))

	retained := nodeIDs(result)
	if !containsID(retained, "ready-1") {
		t.Fatalf("nodes = %v, want ready-1 retained", retained)
	}
	if !containsID(retained, "done-attached") {
		t.Fatalf("nodes = %v, want done-attached retained as a relation endpoint", retained)
	}
	if containsID(retained, "done-isolated") {
		t.Fatalf("nodes = %v, want done-isolated dropped: no edge attaches it to retained work", retained)
	}
	if !result.Truncated {
		t.Fatal("truncated = false, want true: an allowed node was dropped")
	}
}

// TestBuildGraphPlanningExcludeTerminalDropsEveryTerminalNode covers the board's
// configuration, where include_terminal is false and the second pass admits
// nothing at all.
func TestBuildGraphPlanningExcludeTerminalDropsEveryTerminalNode(t *testing.T) {
	snapshot := domain.GraphSnapshot{
		Nodes: []domain.IssueProjection{
			planningNode("ready-1", domain.StatusReady, true),
			planningNode("done-attached", domain.StatusDone, false),
			planningNode("cancelled-1", domain.StatusCancelled, false),
		},
		Edges: []domain.GraphEdge{
			{SourceIssueID: "done-attached", TargetIssueID: "ready-1", Type: string(domain.RelationTypeBlocks)},
		},
	}

	result := domain.BuildGraph(snapshot, planningTraversal(snapshot, 100, false))

	retained := nodeIDs(result)
	if len(retained) != 1 || retained[0] != "ready-1" {
		t.Fatalf("nodes = %v, want only ready-1", retained)
	}
	if len(result.EntryPoints) != 1 || result.EntryPoints[0] != "ready-1" {
		t.Fatalf("entry_points = %v, want [ready-1]", result.EntryPoints)
	}
	if result.Truncated {
		t.Fatal("truncated = true, want false: excluded nodes are not truncation")
	}
}

// TestBuildGraphIssueGraphSelectionIsUnchanged pins the gating decision: the
// rooted issue graph does not opt into non-terminal-first selection, so a
// terminal node still occupies the budget in discovery order there.
func TestBuildGraphIssueGraphSelectionIsUnchanged(t *testing.T) {
	root := "done-root"
	snapshot := domain.GraphSnapshot{
		RootIssueID: &root,
		Nodes: []domain.IssueProjection{
			planningNode("done-root", domain.StatusDone, false),
			planningNode("ready-child", domain.StatusReady, true),
		},
		Edges: []domain.GraphEdge{
			{SourceIssueID: "done-root", TargetIssueID: "ready-child", Type: "contains"},
		},
	}

	result := domain.BuildGraph(snapshot, domain.GraphTraversal{
		RootIssueIDs:     []string{root},
		ExplicitRootID:   root,
		Depth:            2,
		MaxNodes:         100,
		Direction:        domain.GraphDirectionBoth,
		IncludeHierarchy: true,
		IncludeTerminal:  true,
	})

	retained := nodeIDs(result)
	if !containsID(retained, "done-root") {
		t.Fatalf("nodes = %v, want the requested terminal root retained", retained)
	}
	if !containsID(retained, "ready-child") {
		t.Fatalf("nodes = %v, want ready-child retained", retained)
	}
	// Without PreferNonTerminal, entry points stay scoped to retained nodes.
	if len(result.EntryPoints) != 1 || result.EntryPoints[0] != "ready-child" {
		t.Fatalf("entry_points = %v, want [ready-child]", result.EntryPoints)
	}
}

// TestBuildGraphPlanningTraversesThroughTerminalParents is the review
// regression for ISSUE-219: a deferred terminal node must still be traversed
// through, so claimable work reachable only via a finished parent stays in the
// graph instead of appearing as an entry point that resolves to nothing.
func TestBuildGraphPlanningTraversesThroughTerminalParents(t *testing.T) {
	snapshot := domain.GraphSnapshot{
		Nodes: []domain.IssueProjection{
			planningNode("done-epic", domain.StatusDone, false),
			planningNode("ready-child", domain.StatusReady, true),
		},
		Edges: []domain.GraphEdge{
			{SourceIssueID: "done-epic", TargetIssueID: "ready-child", Type: "contains"},
		},
	}
	traversal := domain.GraphTraversal{
		// Only the epic is top-level, mirroring GetPlanningGraph's root seeding.
		RootIssueIDs:      []string{"done-epic"},
		Depth:             3,
		MaxNodes:          100,
		Direction:         domain.GraphDirectionBoth,
		RelationTypes:     []domain.RelationType{domain.RelationTypeBlocks},
		IncludeHierarchy:  true,
		IncludeTerminal:   true,
		PreferNonTerminal: true,
	}

	result := domain.BuildGraph(snapshot, traversal)

	retained := nodeIDs(result)
	if !containsID(retained, "ready-child") {
		t.Fatalf("nodes = %v, want ready-child discovered through its done parent", retained)
	}
	if !containsID(retained, "done-epic") {
		t.Fatalf("nodes = %v, want done-epic retained as a relation endpoint", retained)
	}
	if len(result.Edges) != 1 {
		t.Fatalf("edges = %v, want the contains edge between epic and child", result.Edges)
	}
	if result.Truncated {
		t.Fatal("truncated = true, want false: everything fits the budget")
	}

	// The board's configuration excludes terminal nodes entirely; the child
	// must still be discoverable, without the epic and without truncation.
	traversal.IncludeTerminal = false
	result = domain.BuildGraph(snapshot, traversal)
	retained = nodeIDs(result)
	if len(retained) != 1 || retained[0] != "ready-child" {
		t.Fatalf("nodes = %v, want only ready-child when terminal nodes are excluded", retained)
	}
	if len(result.EntryPoints) != 1 || result.EntryPoints[0] != "ready-child" {
		t.Fatalf("entry_points = %v, want [ready-child]", result.EntryPoints)
	}
	if result.Truncated {
		t.Fatal("truncated = true, want false: excluded nodes are not truncation")
	}
}

// TestBuildGraphPlanningEntryPointsHonorExcludeReview pins that the
// snapshot-wide entry-point scan respects the caller's include_review=false:
// a claimable review issue must not be resurrected as an entry point after its
// node was filtered out.
func TestBuildGraphPlanningEntryPointsHonorExcludeReview(t *testing.T) {
	snapshot := domain.GraphSnapshot{
		Nodes: []domain.IssueProjection{
			planningNode("ready-1", domain.StatusReady, true),
			planningNode("review-1", domain.StatusReview, true),
		},
	}
	traversal := planningTraversal(snapshot, 100, true)
	traversal.ExcludeReview = true

	result := domain.BuildGraph(snapshot, traversal)

	if containsID(nodeIDs(result), "review-1") {
		t.Fatalf("nodes = %v, want review-1 excluded", nodeIDs(result))
	}
	if len(result.EntryPoints) != 1 || result.EntryPoints[0] != "ready-1" {
		t.Fatalf("entry_points = %v, want [ready-1]: excluded review work must not be an entry point", result.EntryPoints)
	}
}

// TestBuildGraphPlanningSecondPassIsOrderIndependent pins that terminal
// admission is judged against the pass-1 retained set: a terminal node whose
// only edges reach other terminal nodes is dropped no matter where it sits in
// snapshot order.
func TestBuildGraphPlanningSecondPassIsOrderIndependent(t *testing.T) {
	build := func(nodes []domain.IssueProjection) domain.GraphResult {
		snapshot := domain.GraphSnapshot{
			Nodes: nodes,
			Edges: []domain.GraphEdge{
				{SourceIssueID: "done-epic", TargetIssueID: "done-task", Type: "contains"},
				{SourceIssueID: "done-task", TargetIssueID: "ready-1", Type: string(domain.RelationTypeBlocks)},
			},
		}
		return domain.BuildGraph(snapshot, planningTraversal(snapshot, 100, true))
	}
	forward := build([]domain.IssueProjection{
		planningNode("done-epic", domain.StatusDone, false),
		planningNode("done-task", domain.StatusDone, false),
		planningNode("ready-1", domain.StatusReady, true),
	})
	backward := build([]domain.IssueProjection{
		planningNode("ready-1", domain.StatusReady, true),
		planningNode("done-task", domain.StatusDone, false),
		planningNode("done-epic", domain.StatusDone, false),
	})

	for name, result := range map[string]domain.GraphResult{"forward": forward, "backward": backward} {
		retained := nodeIDs(result)
		if !containsID(retained, "ready-1") || !containsID(retained, "done-task") {
			t.Fatalf("%s: nodes = %v, want ready-1 and its terminal endpoint done-task", name, retained)
		}
		if containsID(retained, "done-epic") {
			t.Fatalf("%s: nodes = %v, want done-epic dropped: it only attaches to another terminal node", name, retained)
		}
		if !result.Truncated {
			t.Fatalf("%s: truncated = false, want true: an includable node was dropped", name)
		}
	}
}
