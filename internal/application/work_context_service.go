package application

import (
	"context"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// WorkContextService reads validated compact issue work contexts.
type WorkContextService struct {
	repository ports.WorkContextRepository
}

// NewWorkContextService composes the work-context read use case.
func NewWorkContextService(repository ports.WorkContextRepository) (*WorkContextService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "work context repository is required", false)
	}
	return &WorkContextService{repository: repository}, nil
}

// GetWorkContext validates the request, delegates the read, and clones the
// result so repository-owned mutable data cannot escape the application layer.
func (service *WorkContextService) GetWorkContext(ctx context.Context, input domain.GetWorkContextInput) (domain.WorkContext, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.WorkContext{}, err
	}
	result, err := service.repository.GetWorkContext(ctx, ports.GetWorkContextCommand{Input: normalized})
	if err != nil {
		return domain.WorkContext{}, err
	}
	return domain.CloneWorkContext(result), nil
}
