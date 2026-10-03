package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
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

type legacyGraphRepository struct{ base ports.GraphRepository }

func (repository legacyGraphRepository) LoadGraph(ctx context.Context, command ports.LoadGraphCommand) (domain.GraphSnapshot, error) {
	command.Traversal = nil
	return repository.base.LoadGraph(ctx, command)
}

func TestGraphRepositoryTraversalMatchesFullSnapshot(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	ctx := context.Background()
	parentIssue, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeEpic, Title: "Parent"})
	if err != nil {
		t.Fatal(err)
	}
	nodes := []domain.Issue{}
	for index := 0; index < 8; index++ {
		created, err := issues.CreateIssue(ctx, domain.CreateIssueInput{
			Type: domain.TypeTask, Title: fmt.Sprintf("Node %d", index), Status: domain.StatusReady,
			ParentID: &parentIssue.ID, Labels: []string{"graph"}, CreateMissingLabels: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, created.Issue)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		for _, change := range []struct {
			index  int
			status string
		}{{2, "done"}, {4, "review"}, {6, "cancelled"}} {
			var closed any
			if change.status != "review" {
				closed = now.Format(time.RFC3339Nano)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE issues SET status = ?, closed_at = ? WHERE id = ?`, change.status, closed, nodes[change.index].ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []struct {
		source, target int
		kind           domain.RelationType
	}{
		{0, 1, domain.RelationTypeBlocks}, {0, 2, domain.RelationTypeBlocks}, {2, 3, domain.RelationTypeBlocks},
		{0, 4, domain.RelationTypeBlocks}, {4, 5, domain.RelationTypeBlocks}, {1, 6, domain.RelationTypeRelatedTo},
		{3, 7, domain.RelationTypeRelatedTo}, {7, 0, domain.RelationTypeRelatedTo},
	} {
		if _, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
			Action: domain.RelationActionAdd, SourceIssueID: nodes[edge.source].ID, TargetIssueID: nodes[edge.target].ID, RelationType: edge.kind,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 12; index++ {
		if _, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Disconnected", Status: domain.StatusReady}); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := sqlite.NewGraphRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	optimized, err := application.NewGraphService(repository, clock.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := application.NewGraphService(legacyGraphRepository{base: repository}, clock.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{parentIssue.ID, nodes[0].ID, nodes[2].ID, nodes[4].ID} {
		for _, depth := range []int{0, 1, 2, 4} {
			for _, cap := range []int{1, 3, 30} {
				for _, direction := range []domain.GraphDirection{domain.GraphDirectionIncoming, domain.GraphDirectionOutgoing, domain.GraphDirectionBoth} {
					for _, hierarchy := range []bool{false, true} {
						for _, terminal := range []bool{false, true} {
							input := domain.GetIssueGraphInput{RootIssueID: root, Depth: &depth, MaxNodes: &cap, Direction: direction,
								RelationTypes: []domain.RelationType{domain.RelationTypeBlocks, domain.RelationTypeRelatedTo}, IncludeHierarchy: &hierarchy, IncludeTerminal: &terminal}
							want, err := legacy.GetIssueGraph(ctx, input)
							if err != nil {
								t.Fatal(err)
							}
							got, err := optimized.GetIssueGraph(ctx, input)
							if err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(got, want) {
								t.Fatalf("root=%s depth=%d cap=%d direction=%s hierarchy=%t terminal=%t\ngot=%#v\nwant=%#v", root, depth, cap, direction, hierarchy, terminal, got, want)
							}
						}
					}
				}
			}
		}
	}
	for _, root := range []*string{nil, &nodes[0].ID, &nodes[2].ID, &nodes[4].ID} {
		for _, depth := range []int{0, 1, 2, 4} {
			for _, cap := range []int{1, 3, 30} {
				for _, terminal := range []bool{false, true} {
					for _, review := range []bool{false, true} {
						for _, related := range []bool{false, true} {
							input := domain.GetPlanningGraphInput{RootIssueID: root, Depth: &depth, MaxNodes: &cap,
								IncludeTerminal: &terminal, IncludeReview: &review, IncludeRelated: &related}
							want, err := legacy.GetPlanningGraph(ctx, input)
							if err != nil {
								t.Fatal(err)
							}
							got, err := optimized.GetPlanningGraph(ctx, input)
							if err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(got, want) {
								t.Fatalf("planning root=%v depth=%d cap=%d terminal=%t review=%t related=%t\ngot=%#v\nwant=%#v", root, depth, cap, terminal, review, related, got, want)
							}
						}
					}
				}
			}
		}
	}
}

func TestGraphRepositoryRootedReadBudget(t *testing.T) {
	issues, db, now := openIssueService(t)
	ctx := context.Background()
	root, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Root", Status: domain.StatusReady, Labels: []string{"graph"}, CreateMissingLabels: true})
	if err != nil {
		t.Fatal(err)
	}
	base, err := sqlite.NewGraphRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	repository := &measuredGraphRepository{base: base}
	service, err := application.NewGraphService(repository, clock.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}
	depth, cap := 0, 1
	previous := 0
	var first ports.GraphLoadStats
	for _, corpus := range []int{1000, 10000, 100000} {
		if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
			statement := fmt.Sprintf(`WITH RECURSIVE candidates(number) AS (VALUES(%d) UNION ALL SELECT number+1 FROM candidates WHERE number < %d)
			 INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
			 SELECT printf('%%026d', number), number, 'task', 'disconnected', 'ready', 'medium', 1,
			 '2026-10-03T00:00:00.000000000Z', '2026-10-03T00:00:00.000000000Z' FROM candidates`, previous+2, corpus+1)
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO issue_labels(issue_id,label_id)
			 SELECT issues.id,labels.id FROM issues CROSS JOIN labels WHERE issues.sequence_no > ? AND issues.sequence_no <= ? AND labels.name = 'graph'`, previous+1, corpus+1)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		result, err := service.GetIssueGraph(ctx, domain.GetIssueGraphInput{RootIssueID: root.DisplayID, Depth: &depth, MaxNodes: &cap})
		if err != nil {
			t.Fatal(err)
		}
		stats := repository.stats
		if len(result.Nodes) != 1 || len(result.Nodes[0].Labels) != 1 || stats.HydratedNodes != 1 || stats.CandidateNodes != 1 {
			t.Fatalf("corpus=%d graph=%#v stats=%#v", corpus, result, stats)
		}
		if previous == 0 {
			first = stats
		} else if stats.Queries != first.Queries || stats.RowsRead != first.RowsRead {
			t.Fatalf("rooted reads grew: first=%#v corpus=%d stats=%#v", first, corpus, stats)
		}
		if stats.RowsRead != 4 || stats.Queries != 4 {
			t.Fatalf("unexpected rooted budget: %#v", stats)
		}
		previous = corpus
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
