package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

type recordingProjectRepository struct {
	project    domain.Project
	export     domain.LogicalProjectDocument
	err        error
	called     bool
	hasContent bool
}

func (repository *recordingProjectRepository) GetProject(context.Context) (domain.Project, error) {
	repository.called = true
	return repository.project, repository.err
}

func (repository *recordingProjectRepository) ExportLogicalProject(context.Context) (domain.LogicalProjectDocument, error) {
	repository.called = true
	return repository.export, repository.err
}

func (repository *recordingProjectRepository) HasLogicalProjectImportDestinationContent(context.Context) (bool, error) {
	repository.called = true
	return repository.hasContent, repository.err
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

func TestProjectServiceExportsLogicalProjectDocument(t *testing.T) {
	repository := &recordingProjectRepository{export: domain.LogicalProjectDocument{Format: "rhizome-logical-project", Version: 1}}
	service, err := application.NewProjectService(repository)
	if err != nil {
		t.Fatalf("NewProjectService() error = %v", err)
	}
	got, err := service.ExportLogicalProject(context.Background())
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	if !repository.called {
		t.Fatal("repository was not called")
	}
	if !strings.Contains(string(got), "\"format\": \"rhizome-logical-project\"") ||
		!strings.Contains(string(got), "\"version\": 1") ||
		!strings.Contains(string(got), "\"issues\": []") ||
		!strings.Contains(string(got), "\"project\": {") {
		t.Fatalf("export bytes = %s", string(got))
	}
}

func TestNewProjectServiceRequiresRepository(t *testing.T) {
	if _, err := application.NewProjectService(nil); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("NewProjectService(nil) error = %v, want invalid argument", err)
	}
}

func TestProjectServiceValidatesLogicalProjectImportAndReportsDestinationConflict(t *testing.T) {
	repository := &recordingProjectRepository{hasContent: true}
	service, err := application.NewProjectService(repository)
	if err != nil {
		t.Fatalf("NewProjectService() error = %v", err)
	}
	result, err := service.ValidateLogicalProjectImport(context.Background(), []byte(`{
		"format": "rhizome-logical-project",
		"version": 1,
		"exported_at": "2026-07-17T18:24:06Z",
		"project": {
			"id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
			"name": null,
			"instructions": null,
			"created_at": "2026-07-17T18:24:06Z",
			"updated_at": "2026-07-17T18:24:06Z"
		},
		"issues": [],
		"labels": [],
		"issue_labels": [],
		"relations": [],
		"comments": [],
		"decisions": [],
		"attempts": [],
		"attempt_notes": [],
		"artifacts": [],
		"events": []
	}`))
	if err != nil {
		t.Fatalf("ValidateLogicalProjectImport() error = %v", err)
	}
	if result.Counts.Project != 1 || result.Counts.Issues != 0 || result.Writes.Count != 0 {
		t.Fatalf("dry run counts = %#v, writes = %#v", result.Counts, result.Writes)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Code != "empty_destination_required" || result.Conflicts[0].Field != "$.destination" {
		t.Fatalf("conflicts = %#v", result.Conflicts)
	}
}

func TestProjectServiceRejectsMalformedLogicalProjectImport(t *testing.T) {
	repository := &recordingProjectRepository{}
	service, err := application.NewProjectService(repository)
	if err != nil {
		t.Fatalf("NewProjectService() error = %v", err)
	}
	if _, err := service.ValidateLogicalProjectImport(context.Background(), []byte(`{"format":"rhizome-logical-project"}`)); err == nil {
		t.Fatal("ValidateLogicalProjectImport() succeeded for malformed document")
	}
}
