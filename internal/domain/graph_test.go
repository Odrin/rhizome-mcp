package domain

import (
	"reflect"
	"testing"
)

func TestGetIssueGraphInputValidationDefaultsAndBounds(t *testing.T) {
	root := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	input, err := (GetIssueGraphInput{RootIssueID: root}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	if *input.Depth != 2 || *input.MaxNodes != 100 || input.Direction != GraphDirectionBoth ||
		!*input.IncludeHierarchy || !*input.IncludeTerminal || input.View != "compact" ||
		!reflect.DeepEqual(input.RelationTypes, []RelationType{RelationTypeBlocks, RelationTypeRelatedTo, RelationTypeDuplicates}) {
		t.Fatalf("defaults = %#v", input)
	}
	for _, input := range []GetIssueGraphInput{
		{RootIssueID: root, Depth: graphTestInt(6)},
		{RootIssueID: root, MaxNodes: graphTestInt(0)},
		{RootIssueID: root, RelationTypes: []RelationType{RelationTypeBlocks, RelationTypeBlocks}},
		{RootIssueID: root, View: "full"},
	} {
		if _, err := input.Validate(); err == nil {
			t.Fatalf("Validate(%#v) succeeded", input)
		}
	}
	depth := 0
	input, err = (GetIssueGraphInput{RootIssueID: root, Depth: &depth}).Validate()
	if err != nil || *input.Depth != 0 {
		t.Fatalf("explicit zero depth = %#v, %v", input, err)
	}
}

func TestBuildGraphBFSOrderingDirectionsCyclesAndNodeLimit(t *testing.T) {
	snapshot := GraphSnapshot{
		RootIssueID: graphTestString("a"),
		Nodes: []IssueProjection{
			graphTestNode("a", 1, StatusReady), graphTestNode("b", 2, StatusReady),
			graphTestNode("c", 3, StatusReady), graphTestNode("d", 4, StatusReady),
		},
		Edges: []GraphEdge{
			{SourceIssueID: "a", TargetIssueID: "c", Type: "blocks"},
			{SourceIssueID: "a", TargetIssueID: "b", Type: "blocks"},
			{SourceIssueID: "b", TargetIssueID: "a", Type: "related_to"},
			{SourceIssueID: "c", TargetIssueID: "d", Type: "blocks"},
		},
	}
	result := BuildGraph(snapshot, GraphTraversal{
		RootIssueIDs: []string{"a"}, ExplicitRootID: "a", Depth: 2, MaxNodes: 4,
		Direction: GraphDirectionOutgoing, RelationTypes: []RelationType{RelationTypeBlocks, RelationTypeRelatedTo},
		IncludeTerminal: true,
	})
	if got := graphTestIDs(result.Nodes); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("BFS nodes = %v", got)
	}
	if len(result.Edges) != 4 || result.Truncated {
		t.Fatalf("graph = %#v", result)
	}
	incoming := BuildGraph(snapshot, GraphTraversal{
		RootIssueIDs: []string{"c"}, ExplicitRootID: "c", Depth: 1, MaxNodes: 4,
		Direction: GraphDirectionIncoming, RelationTypes: []RelationType{RelationTypeBlocks}, IncludeTerminal: true,
	})
	if got := graphTestIDs(incoming.Nodes); !reflect.DeepEqual(got, []string{"c", "a"}) {
		t.Fatalf("incoming nodes = %v", got)
	}
	limited := BuildGraph(snapshot, GraphTraversal{
		RootIssueIDs: []string{"a"}, ExplicitRootID: "a", Depth: 2, MaxNodes: 2,
		Direction: GraphDirectionBoth, RelationTypes: []RelationType{RelationTypeBlocks, RelationTypeRelatedTo}, IncludeTerminal: true,
	})
	if got := graphTestIDs(limited.Nodes); !reflect.DeepEqual(got, []string{"a", "b"}) ||
		!limited.Truncated || limited.TruncationReason == nil || *limited.TruncationReason != "node_limit" ||
		len(limited.Edges) != 2 {
		t.Fatalf("limited graph = %#v", limited)
	}
}

func TestBuildGraphFiltersTerminalExceptRootAndHierarchy(t *testing.T) {
	snapshot := GraphSnapshot{
		RootIssueID: graphTestString("done"),
		Nodes: []IssueProjection{
			graphTestNode("done", 1, StatusDone), graphTestNode("child", 2, StatusReady),
			graphTestNode("terminal", 3, StatusCancelled),
		},
		Edges: []GraphEdge{
			{SourceIssueID: "done", TargetIssueID: "child", Type: "contains"},
			{SourceIssueID: "child", TargetIssueID: "terminal", Type: "blocks"},
		},
	}
	result := BuildGraph(snapshot, GraphTraversal{
		RootIssueIDs: []string{"done"}, ExplicitRootID: "done", Depth: 2, MaxNodes: 10,
		Direction: GraphDirectionOutgoing, RelationTypes: []RelationType{RelationTypeBlocks},
		IncludeHierarchy: true, IncludeTerminal: false,
	})
	if got := graphTestIDs(result.Nodes); !reflect.DeepEqual(got, []string{"done"}) ||
		len(result.Edges) != 0 || result.Truncated {
		t.Fatalf("terminal hierarchy graph = %#v", result)
	}
}

func TestBuildGraphFiltersReviewRootAndNonRootWithoutTraversing(t *testing.T) {
	snapshot := GraphSnapshot{
		RootIssueID: graphTestString("review"),
		Nodes: []IssueProjection{
			graphTestNode("review", 1, StatusReview), graphTestNode("neighbor", 2, StatusReady),
			graphTestNode("nested-review", 3, StatusReview), graphTestNode("nested-child", 4, StatusReady),
		},
		Edges: []GraphEdge{
			{SourceIssueID: "review", TargetIssueID: "neighbor", Type: "blocks"},
			{SourceIssueID: "neighbor", TargetIssueID: "nested-review", Type: "blocks"},
			{SourceIssueID: "nested-review", TargetIssueID: "nested-child", Type: "blocks"},
		},
	}
	root := BuildGraph(snapshot, GraphTraversal{
		RootIssueIDs: []string{"review"}, ExplicitRootID: "review", Depth: 3, MaxNodes: 10,
		Direction: GraphDirectionOutgoing, RelationTypes: []RelationType{RelationTypeBlocks},
		ExcludeReview: true,
	})
	if got := graphTestIDs(root.Nodes); !reflect.DeepEqual(got, []string{"review"}) ||
		len(root.Edges) != 0 || root.Truncated {
		t.Fatalf("review root graph = %#v", root)
	}

	nonRoot := BuildGraph(snapshot, GraphTraversal{
		RootIssueIDs: []string{"neighbor"}, ExplicitRootID: "neighbor", Depth: 3, MaxNodes: 10,
		Direction: GraphDirectionOutgoing, RelationTypes: []RelationType{RelationTypeBlocks},
		ExcludeReview: true,
	})
	if got := graphTestIDs(nonRoot.Nodes); !reflect.DeepEqual(got, []string{"neighbor"}) ||
		len(nonRoot.Edges) != 0 || nonRoot.Truncated {
		t.Fatalf("non-root review graph = %#v", nonRoot)
	}
}

func graphTestNode(id string, sequence int64, status Status) IssueProjection {
	return IssueProjection{Issue: Issue{ID: id, SequenceNo: sequence, Status: status, Type: TypeTask}, IsClaimable: status == StatusReady}
}

func graphTestIDs(nodes []IssueProjection) []string {
	ids := make([]string, len(nodes))
	for index, node := range nodes {
		ids[index] = node.ID
	}
	return ids
}

func graphTestInt(value int) *int          { return &value }
func graphTestString(value string) *string { return &value }
