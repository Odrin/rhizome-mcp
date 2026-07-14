package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// GraphService composes compact issue and planning graph projections through one
// shared domain traversal engine.
type GraphService struct {
	repository ports.GraphRepository
	clock      clock.Clock
}

// NewGraphService composes graph queries from their snapshot repository.
func NewGraphService(repository ports.GraphRepository, source clock.Clock) (*GraphService, error) {
	if repository == nil || source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "graph dependencies are required", false)
	}
	return &GraphService{repository: repository, clock: source}, nil
}

// GetIssueGraph returns the requested graph rooted at one current issue.
func (service *GraphService) GetIssueGraph(ctx context.Context, input domain.GetIssueGraphInput) (domain.GraphResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.GraphResult{}, err
	}
	identifier, err := domain.ParseIssueIdentifier(normalized.RootIssueID)
	if err != nil {
		return domain.GraphResult{}, err
	}
	snapshot, err := service.repository.LoadGraph(ctx, ports.LoadGraphCommand{RootIdentifier: &identifier, Now: service.clock.Now().UTC()})
	if err != nil {
		return domain.GraphResult{}, err
	}
	return domain.BuildGraph(snapshot, domain.GraphTraversal{
		RootIssueIDs: normalizedRootIDs(snapshot), ExplicitRootID: dereference(snapshot.RootIssueID),
		Depth: *normalized.Depth, MaxNodes: *normalized.MaxNodes, Direction: normalized.Direction,
		RelationTypes: normalized.RelationTypes, IncludeHierarchy: *normalized.IncludeHierarchy,
		IncludeTerminal: *normalized.IncludeTerminal,
	}), nil
}

// GetPlanningGraph returns the planning projection through the same traversal
// engine used by GetIssueGraph.
func (service *GraphService) GetPlanningGraph(ctx context.Context, input domain.GetPlanningGraphInput) (domain.GraphResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.GraphResult{}, err
	}
	command := ports.LoadGraphCommand{Now: service.clock.Now().UTC()}
	if normalized.RootIssueID != nil {
		identifier, err := domain.ParseIssueIdentifier(*normalized.RootIssueID)
		if err != nil {
			return domain.GraphResult{}, err
		}
		command.RootIdentifier = &identifier
	}
	snapshot, err := service.repository.LoadGraph(ctx, command)
	if err != nil {
		return domain.GraphResult{}, err
	}
	roots := snapshot.TopLevelIssueIDs
	if snapshot.RootIssueID != nil {
		roots = normalizedRootIDs(snapshot)
	}
	relationTypes := []domain.RelationType{domain.RelationTypeBlocks}
	if *normalized.IncludeRelated {
		relationTypes = append(relationTypes, domain.RelationTypeRelatedTo)
	}
	return domain.BuildGraph(snapshot, domain.GraphTraversal{
		RootIssueIDs: roots, ExplicitRootID: dereference(snapshot.RootIssueID),
		Depth: *normalized.Depth, MaxNodes: *normalized.MaxNodes, Direction: domain.GraphDirectionBoth,
		RelationTypes: relationTypes, IncludeHierarchy: true, IncludeTerminal: true,
		ExcludeReview: !*normalized.IncludeReview,
	}), nil
}

func normalizedRootIDs(snapshot domain.GraphSnapshot) []string {
	if snapshot.RootIssueID == nil {
		return nil
	}
	return []string{*snapshot.RootIssueID}
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
