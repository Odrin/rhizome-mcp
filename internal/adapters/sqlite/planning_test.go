package sqlite_test

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
)

func TestApplyIssuePlanAtomicReplayAndConflict(t *testing.T) {
	_, db, now := openIssueService(t)
	source := clock.NewFakeClock(now)
	repository, err := sqlite.NewPlanningRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewPlanningService(repository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.IssuePlan{
		Issues: []domain.PlannedIssue{
			{Ref: "epic", Type: domain.TypeEpic, Title: "Epic"},
			{Ref: "task", Type: domain.TypeTask, Title: "Task", ParentRef: stringPointer("epic")},
		},
		Relations: []domain.PlannedRelation{{SourceRef: "epic", TargetRef: "task", Type: domain.RelationTypeBlocks}},
		Decisions: []domain.PlannedDecision{{IssueRef: stringPointer("task"), Title: "Decision", Summary: "summary", Content: "content"}},
	}
	first, err := service.ApplyIssuePlan(context.Background(), plan, "plan-key")
	if err != nil {
		t.Fatalf("ApplyIssuePlan() error = %v", err)
	}
	if len(first.CreatedIssues) != 2 || first.CreatedIssues[1].Issue.ParentID == nil ||
		len(first.CreatedRelations) != 1 || len(first.CreatedDecisions) != 1 || first.LatestEventID != 5 {
		t.Fatalf("first result = %#v", first)
	}
	second, err := service.ApplyIssuePlan(context.Background(), plan, "plan-key")
	if err != nil {
		t.Fatalf("replay error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay = %#v, want %#v", second, first)
	}
	changed := plan
	changed.Issues = append([]domain.PlannedIssue(nil), plan.Issues...)
	changed.Issues[1].Title = "Changed"
	_, err = service.ApplyIssuePlan(context.Background(), changed, "plan-key")
	if !errors.Is(err, &domain.Error{Code: domain.CodeIdempotencyConflict}) {
		t.Fatalf("conflict error = %v", err)
	}
	var issues, relations, decisions, events, records, nextNumber int
	var decisionEventType string
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations").Scan(&relations); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&decisions); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM idempotency_records").Scan(&records); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT event_type FROM issue_events WHERE id = ?", first.LatestEventID).Scan(&decisionEventType); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&nextNumber)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != 2 || relations != 1 || decisions != 1 || events != 5 || records != 1 || nextNumber != 3 {
		t.Fatalf("persisted counts = issues=%d relations=%d decisions=%d events=%d records=%d next=%d", issues, relations, decisions, events, records, nextNumber)
	}
	if decisionEventType != "decision_recorded" {
		t.Fatalf("decision event type = %q, want %q", decisionEventType, "decision_recorded")
	}
}

func TestApplyIssuePlanUsesExistingLabelsWithoutGeneratedIDs(t *testing.T) {
	_, db, now := openIssueService(t)
	source := clock.NewFakeClock(now)
	repository, err := sqlite.NewPlanningRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewPlanningService(repository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	create := domain.IssuePlan{Issues: []domain.PlannedIssue{{
		Ref: "create", Type: domain.TypeTask, Title: "Create label",
		Labels: []string{"existing"}, CreateMissingLabels: true,
	}}}
	created, err := service.ApplyIssuePlan(ctx, create, "create-label")
	if err != nil {
		t.Fatalf("create label: %v", err)
	}
	if len(created.CreatedIssues) != 1 || len(created.CreatedIssues[0].Issue.Labels) != 1 {
		t.Fatalf("create result = %#v", created)
	}

	missing := domain.IssuePlan{Issues: []domain.PlannedIssue{{
		Ref: "missing", Type: domain.TypeTask, Title: "Missing label",
		Labels: []string{"missing"},
	}}}
	if _, err := service.ApplyIssuePlan(ctx, missing, "missing-label"); !errors.Is(err, &domain.Error{Code: domain.CodeLabelNotFound}) {
		t.Fatalf("missing label apply error = %v", err)
	}

	reuse := domain.IssuePlan{Issues: []domain.PlannedIssue{{
		Ref: "reuse", Type: domain.TypeTask, Title: "Reuse label",
		Labels: []string{"existing"},
	}}}
	first, err := service.ApplyIssuePlan(ctx, reuse, "reuse-label")
	if err != nil {
		t.Fatalf("reuse label: %v", err)
	}
	second, err := service.ApplyIssuePlan(ctx, reuse, "reuse-label")
	if err != nil {
		t.Fatalf("replay reuse label: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay = %#v, want %#v", second, first)
	}
	if len(first.CreatedIssues) != 1 || !reflect.DeepEqual(first.CreatedIssues[0].Issue.Labels, created.CreatedIssues[0].Issue.Labels) {
		t.Fatalf("reuse result = %#v, created labels = %#v", first, created.CreatedIssues[0].Issue.Labels)
	}

	reuseWithCreation := domain.IssuePlan{Issues: []domain.PlannedIssue{{
		Ref: "reuse-create", Type: domain.TypeTask, Title: "Reuse label with creation enabled",
		Labels: []string{"existing"}, CreateMissingLabels: true,
	}}}
	reusedWithCreation, err := service.ApplyIssuePlan(ctx, reuseWithCreation, "reuse-label-create")
	if err != nil {
		t.Fatalf("reuse label with creation enabled: %v", err)
	}
	if len(reusedWithCreation.CreatedIssues) != 1 ||
		!reflect.DeepEqual(reusedWithCreation.CreatedIssues[0].Issue.Labels, created.CreatedIssues[0].Issue.Labels) {
		t.Fatalf("reuse with creation result = %#v, created labels = %#v", reusedWithCreation, created.CreatedIssues[0].Issue.Labels)
	}

	var issues, records int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM idempotency_records").Scan(&records)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != 3 || records != 3 {
		t.Fatalf("rollback/replay state = issues=%d records=%d", issues, records)
	}
}

func TestApplyIssuePlanRejectsCycleWithoutSequenceAllocation(t *testing.T) {
	_, db, now := openIssueService(t)
	source := clock.NewFakeClock(now)
	repository, _ := sqlite.NewPlanningRepository(db)
	generator, _ := ids.NewGenerator(source, rand.Reader)
	service, _ := application.NewPlanningService(repository, source, generator)
	plan := domain.IssuePlan{
		Issues:    []domain.PlannedIssue{{Ref: "a", Type: domain.TypeTask, Title: "A"}, {Ref: "b", Type: domain.TypeTask, Title: "B"}},
		Relations: []domain.PlannedRelation{{SourceRef: "a", TargetRef: "b", Type: domain.RelationTypeBlocks}, {SourceRef: "b", TargetRef: "a", Type: domain.RelationTypeBlocks}},
	}

	_, err := service.ApplyIssuePlan(context.Background(), plan, "cycle")
	if !errors.Is(err, &domain.Error{Code: domain.CodeValidationError}) {
		t.Fatalf("cycle error = %v", err)
	}
	var issues, events, records, next int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM idempotency_records").Scan(&records); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT next_issue_number FROM projects").Scan(&next)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != 0 || events != 0 || records != 0 || next != 1 {
		t.Fatalf("rollback state = issues=%d events=%d records=%d next=%d", issues, events, records, next)
	}
}

func TestApplyIssuePlanRejectsCycleAcrossExistingAndBatchEdges(t *testing.T) {
	issues, db, now := openIssueService(t)
	ctx := context.Background()
	a, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_by_session_id, created_at)
			VALUES (?, ?, ?, 'blocks', NULL, ?)`, "01BX5ZZKBKACTAV9WEVGEMMVRZ", a.ID, b.ID, sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	source := clock.NewFakeClock(now)
	repository, _ := sqlite.NewPlanningRepository(db)
	generator, _ := ids.NewGenerator(source, rand.Reader)
	service, _ := application.NewPlanningService(repository, source, generator)
	plan := domain.IssuePlan{
		Issues: []domain.PlannedIssue{{Ref: "c", Type: domain.TypeTask, Title: "C"}},
		Relations: []domain.PlannedRelation{
			{SourceRef: b.ID, TargetRef: "c", Type: domain.RelationTypeBlocks},
			{SourceRef: "c", TargetRef: a.DisplayID, Type: domain.RelationTypeBlocks},
		},
	}
	validation, err := service.ValidateIssuePlan(ctx, plan)
	if err != nil || validation.Valid {
		t.Fatalf("validation = %#v, error = %v", validation, err)
	}
	if len(validation.Errors) != 1 || validation.Errors[0].Code != domain.CodeBlocksCycle {
		t.Fatalf("errors = %#v", validation.Errors)
	}
}

// TestBlocksPathExistsAgainstDomainFunction is a regression check for
// planPathExists, which now delegates its BFS to domain.BlocksPathExists: it
// exercises that delegation against a small blocks-relation fixture graph.
// The SQL-adapter-vs-domain-helper agreement ISSUE-186 AC4 asks for is
// covered separately by TestBlocksPathExistsAgreesWithDomainHelper in
// relations_test.go, which goes through blocksPathExists's SQL recursive CTE.
func TestBlocksPathExistsAgainstDomainFunction(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()

	// Create a fixture: A -> B -> C (A blocks B, B blocks C)
	//                  A -> D (A blocks D)
	// There should be a path from A to C (through B), but not from C to A
	a, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "B"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "C"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "D"})
	if err != nil {
		t.Fatal(err)
	}

	// Create relation service
	relationRepository, err := sqlite.NewRelationRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	source := clock.NewFakeClock(now)
	relationGenerator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relationService, err := application.NewRelationService(relationRepository, source, relationGenerator)
	if err != nil {
		t.Fatal(err)
	}

	// Add blocks relations: A -> B, B -> C, A -> D
	for _, rel := range []struct {
		source string
		target string
	}{
		{a.ID, b.ID},
		{b.ID, c.ID},
		{a.ID, d.ID},
	} {
		_, err := relationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
			Action:        domain.RelationActionAdd,
			SourceIssueID: rel.source,
			TargetIssueID: rel.target,
			RelationType:  domain.RelationTypeBlocks,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Test cases: (start, sought, wantPath)
	testCases := []struct {
		name     string
		start    string
		sought   string
		wantPath bool
	}{
		{name: "direct edge A->B", start: a.ID, sought: b.ID, wantPath: true},
		{name: "transitive A->B->C", start: a.ID, sought: c.ID, wantPath: true},
		{name: "direct edge A->D", start: a.ID, sought: d.ID, wantPath: true},
		{name: "no path C->A (backward)", start: c.ID, sought: a.ID, wantPath: false},
		{name: "no path D->B (sideways)", start: d.ID, sought: b.ID, wantPath: false},
		{name: "self loop check A->A", start: a.ID, sought: a.ID, wantPath: false},
		{name: "direct edge B->C", start: b.ID, sought: c.ID, wantPath: true},
		{name: "no path B->A (backward)", start: b.ID, sought: a.ID, wantPath: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call domain.BlocksPathExists with an adjacency function built from the fixture
			edges := []struct{ source, target string }{
				{a.ID, b.ID},
				{b.ID, c.ID},
				{a.ID, d.ID},
			}
			adjacency := make(map[string][]string)
			for _, edge := range edges {
				adjacency[edge.source] = append(adjacency[edge.source], edge.target)
			}

			got := domain.BlocksPathExists(tc.start, tc.sought, func(node string) []string {
				return adjacency[node]
			})

			if got != tc.wantPath {
				t.Fatalf("BlocksPathExists(%q, %q) = %v, want %v",
					tc.start, tc.sought, got, tc.wantPath)
			}
		})
	}
}
