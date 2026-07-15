package ports

import (
	"context"

	"rhizome-mcp/internal/domain"
)

type SearchCommand struct {
	Input domain.SearchInput
}

type GetChangesCommand struct {
	Input domain.GetChangesInput
}

type SearchRepository interface {
	Search(context.Context, SearchCommand) (domain.SearchPage, error)
	GetChanges(context.Context, GetChangesCommand) (domain.ChangesPage, error)
}
