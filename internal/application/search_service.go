package application

import (
	"context"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type SearchService struct {
	repository ports.SearchRepository
}

func NewSearchService(repository ports.SearchRepository) (*SearchService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "search repository is required", false)
	}
	return &SearchService{repository: repository}, nil
}

func (service *SearchService) Search(ctx context.Context, input domain.SearchInput) (domain.SearchPage, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.SearchPage{}, err
	}
	result, err := service.repository.Search(ctx, ports.SearchCommand{Input: normalized})
	if err != nil {
		return domain.SearchPage{}, err
	}
	return domain.CloneSearchPage(result), nil
}

func (service *SearchService) GetChanges(ctx context.Context, input domain.GetChangesInput) (domain.ChangesPage, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.ChangesPage{}, err
	}
	result, err := service.repository.GetChanges(ctx, ports.GetChangesCommand{Input: normalized})
	if err != nil {
		return domain.ChangesPage{}, err
	}
	return domain.CloneChangesPage(result), nil
}
