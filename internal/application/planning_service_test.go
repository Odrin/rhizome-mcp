package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type recordingPlanningRepository struct {
	validated bool
	applied   bool
	command   ports.ApplyIssuePlanCommand
}

func (repository *recordingPlanningRepository) ValidateIssuePlan(_ context.Context, _ domain.IssuePlan) ([]domain.Detail, error) {
	repository.validated = true
	return nil, nil
}
func (repository *recordingPlanningRepository) LookupAppliedIssuePlan(_ context.Context, _ string, _ []byte) (ports.ApplyIssuePlanResult, bool, error) {
	return ports.ApplyIssuePlanResult{}, false, nil
}
func (repository *recordingPlanningRepository) ApplyIssuePlan(_ context.Context, command ports.ApplyIssuePlanCommand) (ports.ApplyIssuePlanResult, error) {
	repository.applied, repository.command = true, command
	return ports.ApplyIssuePlanResult{}, nil
}

func TestPlanningServiceNormalizesAndRequiresIdempotencyKey(t *testing.T) {
	repository := &recordingPlanningRepository{}
	service, err := application.NewPlanningService(repository, clock.NewFakeClock(time.Unix(0, 0)),
		fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.IssuePlan{Issues: []domain.PlannedIssue{{Ref: "work", Type: domain.TypeTask, Title: "work", Labels: []string{"B", "a"}, CreateMissingLabels: true}}}
	if _, err := service.ApplyIssuePlan(context.Background(), plan, ""); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("missing key error = %v", err)
	}
	if repository.applied {
		t.Fatal("repository called with missing key")
	}
	if _, err := service.ApplyIssuePlan(context.Background(), plan, "key"); err != nil {
		t.Fatal(err)
	}
	if !repository.applied || len(repository.command.RequestHash) != 32 || len(repository.command.LabelIDs) != 1 ||
		repository.command.Plan.Issues[0].Labels[0] != "a" {
		t.Fatalf("command = %#v", repository.command)
	}
}
