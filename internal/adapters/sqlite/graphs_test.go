package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
)

func TestGraphRepositorySnapshotHierarchyArchiveAndPlanningCap(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	repository, err := sqlite.NewGraphRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	graphs, err := application.NewGraphService(repository, clock.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	epic, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeEpic, Title: "Epic"})
	if err != nil {
		t.Fatal(err)
	}
	parent := epic.ID
	child, err := issues.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Child", Status: domain.StatusReady, ParentID: &parent,
		Description: stringPointer("private detail"), Labels: []string{"graph"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Blocker", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: blocker.ID, TargetIssueID: child.ID, RelationType: domain.RelationTypeBlocks,
	}); err != nil {
		t.Fatal(err)
	}

	direction := domain.GraphDirectionIncoming
	depth, cap := 1, 10
	graph, err := graphs.GetIssueGraph(ctx, domain.GetIssueGraphInput{
		RootIssueID: child.DisplayID, Depth: &depth, MaxNodes: &cap, Direction: direction,
		RelationTypes: []domain.RelationType{domain.RelationTypeBlocks}, IncludeHierarchy: graphBoolPointer(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := graphIDs(graph.Nodes); !reflect.DeepEqual(got, []string{child.ID, blocker.ID, epic.ID}) ||
		len(graph.Edges) != 2 || graph.Edges[0].Type != "blocks" || graph.Edges[1].Type != "contains" ||
		graph.Nodes[0].UnresolvedBlockerCount != 1 || !graph.Nodes[0].IsBlocked ||
		graph.Nodes[0].Description != nil || graph.Nodes[0].AcceptanceCriteria != nil ||
		len(graph.Nodes[0].Labels) != 1 || graph.Nodes[0].Labels[0].Name != "graph" {
		t.Fatalf("graph projection = %#v", graph)
	}

	var eventsBefore, eventsAfter int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&eventsBefore)
	}); err != nil {
		t.Fatal(err)
	}
	planningDepth, planningCap := 3, 2
	planning, err := graphs.GetPlanningGraph(ctx, domain.GetPlanningGraphInput{Depth: &planningDepth, MaxNodes: &planningCap})
	if err != nil {
		t.Fatal(err)
	}
	if got := graphIDs(planning.Nodes); !reflect.DeepEqual(got, []string{epic.ID, child.ID}) ||
		!planning.Truncated || planning.TruncationReason == nil || *planning.TruncationReason != "node_limit" {
		t.Fatalf("planning cap = %#v", planning)
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&eventsAfter)
	}); err != nil {
		t.Fatal(err)
	}
	if eventsBefore != eventsAfter {
		t.Fatalf("graph read mutated events: before=%d after=%d", eventsBefore, eventsAfter)
	}

	if _, err := issues.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: blocker.ID, ExpectedVersion: blocker.Issue.Version}); err != nil {
		t.Fatal(err)
	}
	graph, err = graphs.GetIssueGraph(ctx, domain.GetIssueGraphInput{
		RootIssueID: child.ID, Depth: &depth, MaxNodes: &cap, Direction: direction,
		RelationTypes: []domain.RelationType{domain.RelationTypeBlocks}, IncludeHierarchy: graphBoolPointer(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := graphIDs(graph.Nodes); !reflect.DeepEqual(got, []string{child.ID}) || len(graph.Edges) != 0 {
		t.Fatalf("archived node was exposed: %#v", graph)
	}
	_, err = graphs.GetIssueGraph(ctx, domain.GetIssueGraphInput{RootIssueID: blocker.ID})
	if !errors.Is(err, &domain.Error{Code: domain.CodeIssueArchived}) {
		t.Fatalf("archived root error = %v", err)
	}
}

func graphIDs(nodes []domain.IssueProjection) []string {
	result := make([]string, len(nodes))
	for index, node := range nodes {
		result[index] = node.ID
	}
	return result
}

func graphBoolPointer(value bool) *bool { return &value }
