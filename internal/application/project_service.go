package application

import (
	"context"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// ProjectService queries metadata for the current project.
type ProjectService struct {
	repository ports.ProjectRepository
}

// NewProjectService composes the project metadata use case from its repository.
func NewProjectService(repository ports.ProjectRepository) (*ProjectService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "project repository is required", false)
	}
	return &ProjectService{repository: repository}, nil
}

// GetProject returns the current project's persisted metadata.
func (service *ProjectService) GetProject(ctx context.Context) (domain.Project, error) {
	return service.repository.GetProject(ctx)
}

// ExportLogicalProject renders the current project's logical interchange document as JSON bytes.
func (service *ProjectService) ExportLogicalProject(ctx context.Context) ([]byte, error) {
	document, err := service.repository.ExportLogicalProject(ctx)
	if err != nil {
		return nil, err
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		return nil, domain.WrapError(err, domain.CodeStorageFailure, "logical project export could not be encoded", false)
	}
	return data, nil
}

// ValidateLogicalProjectImport validates a logical project document and reports a deterministic dry run.
func (service *ProjectService) ValidateLogicalProjectImport(ctx context.Context, document []byte) (domain.LogicalProjectImportDryRun, error) {
	plan, err := domain.ParseLogicalProjectImportPlan(document)
	if err != nil {
		return domain.LogicalProjectImportDryRun{}, err
	}
	hasContent, err := service.repository.HasLogicalProjectImportDestinationContent(ctx)
	if err != nil {
		return domain.LogicalProjectImportDryRun{}, err
	}
	if hasContent {
		domain.AddDestinationConflicts(&plan, "empty_destination_required", "destination project must be empty for this import", "$.destination")
	}
	return plan.DryRun, nil
}

// ApplyLogicalProjectImport applies a validated logical project document into an empty destination.
func (service *ProjectService) ApplyLogicalProjectImport(ctx context.Context, document []byte) (domain.LogicalProjectImportApplyResult, error) {
	plan, err := domain.ParseLogicalProjectImportPlan(document)
	if err != nil {
		return domain.LogicalProjectImportApplyResult{}, err
	}
	return service.repository.ApplyLogicalProjectImport(ctx, plan)
}
