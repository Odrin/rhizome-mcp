package application

import (
	"context"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestRelationServicePreservesIdempotentAddResult(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	repository := &relationRepositoryStub{
		result: ports.ManageIssueRelationResult{
			Relation: domain.IssueRelation{
				ID:            "01ARZ3NDEKTSV4RRFFQ69G5FAY",
				SourceIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
				TargetIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
				Type:          domain.RelationTypeRelatedTo,
				CreatedAt:     now,
			},
			AffectedIssues: []domain.IssueProjection{
				{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
				{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}},
			},
			Changed: false,
		},
	}
	service, err := NewRelationService(repository, clock.NewFakeClock(now), fixedRelationIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FAX"))
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ManageIssueRelation(context.Background(), domain.ManageIssueRelationInput{
		Action:        domain.RelationActionAdd,
		SourceIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		TargetIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		RelationType:  domain.RelationTypeRelatedTo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Relation.ID != repository.result.Relation.ID ||
		len(result.AffectedIssues) != 2 || repository.command.RelationID == "" {
		t.Fatalf("idempotent result = %#v, command = %#v", result, repository.command)
	}
}

type relationRepositoryStub struct {
	command ports.ManageIssueRelationCommand
	result  ports.ManageIssueRelationResult
}

func (stub *relationRepositoryStub) ManageIssueRelation(_ context.Context, command ports.ManageIssueRelationCommand) (ports.ManageIssueRelationResult, error) {
	stub.command = command
	return stub.result, nil
}

func (stub *relationRepositoryStub) LookupManageIssueRelation(context.Context, string, []byte) (ports.ManageIssueRelationResult, bool, error) {
	return ports.ManageIssueRelationResult{}, false, nil
}

type fixedRelationIDGenerator string

func (generator fixedRelationIDGenerator) New() (string, error) {
	return string(generator), nil
}
