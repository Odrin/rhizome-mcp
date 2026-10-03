package domain

import (
	"fmt"
	"testing"
)

func BenchmarkBuildGraph(b *testing.B) {
	for _, size := range []int{100, 250, 500} {
		for _, shape := range []string{"dense", "star"} {
			b.Run(fmt.Sprintf("%s/nodes=%d", shape, size), func(b *testing.B) {
				snapshot := GraphSnapshot{Nodes: make([]IssueProjection, size)}
				for index := range snapshot.Nodes {
					snapshot.Nodes[index] = graphTestNode(fmt.Sprintf("node-%03d", index), int64(index+1), StatusReady)
				}
				for source := 0; source < size; source++ {
					if shape == "star" && source != 0 {
						break
					}
					for target := source + 1; target < size; target++ {
						snapshot.Edges = append(snapshot.Edges, GraphEdge{
							SourceIssueID: snapshot.Nodes[source].ID,
							TargetIssueID: snapshot.Nodes[target].ID,
							Type:          string(RelationTypeRelatedTo),
						})
					}
				}
				traversal := GraphTraversal{
					RootIssueIDs: []string{snapshot.Nodes[0].ID}, ExplicitRootID: snapshot.Nodes[0].ID,
					Depth: 2, MaxNodes: size, Direction: GraphDirectionBoth,
					RelationTypes: []RelationType{RelationTypeRelatedTo}, IncludeTerminal: true,
				}
				var result GraphResult
				b.ReportAllocs()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					result = BuildGraph(snapshot, traversal)
				}
				b.StopTimer()
				wantEdges := size - 1
				if shape == "dense" {
					wantEdges = size * (size - 1) / 2
				}
				if len(result.Nodes) != size || len(result.Edges) != wantEdges || result.Truncated {
					b.Fatalf("graph nodes/edges/truncated = %d/%d/%v, want %d/%d/false", len(result.Nodes), len(result.Edges), result.Truncated, size, wantEdges)
				}
			})
		}
	}
}
