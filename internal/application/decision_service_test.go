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

type recordingDecisionRepository struct {
	command ports.RecordDecisionCommand
	called  bool
}

func (repository *recordingDecisionRepository) RecordDecision(_ context.Context, command ports.RecordDecisionCommand) (domain.RecordDecisionResult, error) {
	repository.called = true
	repository.command = command
	return domain.RecordDecisionResult{Decision: domain.Decision{ID: command.ID, Status: command.Input.Status}}, nil
}

func (repository *recordingDecisionRepository) ListDecisions(_ context.Context, _ ports.ListDecisionsCommand) (domain.DecisionList, error) {
	return domain.DecisionList{}, nil
}

func TestDecisionServiceValidatesAndBuildsCommand(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 9, 10, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingDecisionRepository{}
	service, err := application.NewDecisionService(repository, clock.NewFakeClock(now), fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RecordDecision(context.Background(), domain.RecordDecisionInput{Title: "Title", Summary: "Summary"})
	if err != nil {
		t.Fatal(err)
	}
	if !repository.called || repository.command.ID != result.Decision.ID ||
		repository.command.Input.Status != domain.DecisionStatusActive || !repository.command.OccurredAt.Equal(now.UTC()) {
		t.Fatalf("command = %#v", repository.command)
	}
}

func TestDecisionServiceDoesNotAllocateForInvalidInput(t *testing.T) {
	generator := &countingDecisionGenerator{}
	service, err := application.NewDecisionService(&recordingDecisionRepository{}, clock.NewFakeClock(time.Unix(1, 0)), generator)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.RecordDecision(context.Background(), domain.RecordDecisionInput{Title: " ", Summary: "summary"})
	if err == nil || generator.calls != 0 {
		t.Fatalf("error=%v generator calls=%d", err, generator.calls)
	}
}

func TestDecisionServiceMapsIDGenerationFailure(t *testing.T) {
	service, err := application.NewDecisionService(&recordingDecisionRepository{}, clock.NewFakeClock(time.Unix(1, 0)),
		fixedIDGenerator{err: errors.New("entropy failed")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.RecordDecision(context.Background(), domain.RecordDecisionInput{Title: "Title", Summary: "Summary"})
	if !errors.Is(err, &domain.Error{Code: domain.CodeIDGeneration}) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewDecisionServiceRequiresDependencies(t *testing.T) {
	generator := fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	source := clock.NewFakeClock(time.Unix(1, 0))
	for _, test := range []struct {
		repository ports.DecisionRepository
		source     clock.Clock
		generator  application.IDGenerator
	}{
		{nil, source, generator}, {&recordingDecisionRepository{}, nil, generator}, {&recordingDecisionRepository{}, source, nil},
	} {
		if _, err := application.NewDecisionService(test.repository, test.source, test.generator); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
			t.Fatalf("constructor error = %v", err)
		}
	}
}

type countingDecisionGenerator struct{ calls int }

func (generator *countingDecisionGenerator) New() (string, error) {
	generator.calls++
	return "01ARZ3NDEKTSV4RRFFQ69G5FAV", nil
}
