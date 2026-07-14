package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

type recordingProjectRepository struct {
	project domain.Project
	err     error
	called  bool
}

func (repository *recordingProjectRepository) GetProject(context.Context) (domain.Project, error) {
	repository.called = true
	return repository.project, repository.err
}

func TestProjectServiceGetsProjectMetadata(t *testing.T) {
	repository := &recordingProjectRepository{
		project: domain.Project{
			ID:            "01ARZ3NDEKTSV4RRFFQ69G5FAV",
			SchemaVersion: 1,
			LatestEventID: 7,
			CreatedAt:     time.Unix(1, 0).UTC(),
			UpdatedAt:     time.Unix(2, 0).UTC(),
		},
	}
	service, err := application.NewProjectService(repository)
	if err != nil {
		t.Fatalf("NewProjectService() error = %v", err)
	}

	got, err := service.GetProject(context.Background())
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if !repository.called || got != repository.project {
		t.Fatalf("called=%v, project=%#v, want %#v", repository.called, got, repository.project)
	}
}

func TestProjectServicePropagatesRepositoryError(t *testing.T) {
	want := errors.New("repository failed")
	service, err := application.NewProjectService(&recordingProjectRepository{err: want})
	if err != nil {
		t.Fatalf("NewProjectService() error = %v", err)
	}
	if _, err := service.GetProject(context.Background()); !errors.Is(err, want) {
		t.Fatalf("GetProject() error = %v, want %v", err, want)
	}
}

func TestNewProjectServiceRequiresRepository(t *testing.T) {
	if _, err := application.NewProjectService(nil); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("NewProjectService(nil) error = %v, want invalid argument", err)
	}
}
