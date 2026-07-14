package application

import (
	"context"
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestGraphServiceDelegatesAndPropagatesRepositoryErrors(t *testing.T) {
	repository := &graphRepositoryStub{err: errors.New("snapshot failed")}
	service, err := NewGraphService(repository)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetIssueGraph(context.Background(), domain.GetIssueGraphInput{
		RootIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
	})
	if !errors.Is(err, repository.err) {
		t.Fatalf("GetIssueGraph() error = %v", err)
	}
	if repository.command.RootIdentifier == nil {
		t.Fatal("root identifier was not delegated")
	}
	if _, err := NewGraphService(nil); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("NewGraphService(nil) error = %v", err)
	}
}

type graphRepositoryStub struct {
	command ports.LoadGraphCommand
	err     error
}

func (stub *graphRepositoryStub) LoadGraph(_ context.Context, command ports.LoadGraphCommand) (domain.GraphSnapshot, error) {
	stub.command = command
	return domain.GraphSnapshot{}, stub.err
}
