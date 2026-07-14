package sqlite_test

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"testing"
	"time"

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
			VALUES (?, ?, ?, 'blocks', NULL, ?)`, "01BX5ZZKBKACTAV9WEVGEMMVRZ", a.ID, b.ID, now.Format(time.RFC3339Nano))
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
