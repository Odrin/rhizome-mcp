package application

import (
	"context"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// ActivityService reads validated issue activity pages.
type ActivityService struct {
	repository ports.ActivityRepository
}

// NewActivityService composes the activity read use case.
func NewActivityService(repository ports.ActivityRepository) (*ActivityService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "activity repository is required", false)
	}
	return &ActivityService{repository: repository}, nil
}

// GetIssueActivity validates the request, delegates the read, and clones the
// result so repository-owned mutable data cannot escape the application layer.
func (service *ActivityService) GetIssueActivity(ctx context.Context, input domain.GetIssueActivityInput) (domain.IssueActivity, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.IssueActivity{}, err
	}
	result, err := service.repository.GetIssueActivity(ctx, ports.GetIssueActivityCommand{Input: normalized})
	if err != nil {
		return domain.IssueActivity{}, err
	}
	return domain.CloneIssueActivity(result), nil
}
