package sqlite

import (
	"context"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// LoadIssueGateSummary reads one issue's compact gate summary from a single
// snapshot. It resolves either identifier form like every other issue read,
// then delegates to buildWorkContextGateSummary -- the exact builder
// get_work_context uses -- so the board and issue-detail surfaces report the
// same summary an agent sees in context (ISSUE-175 AC2), evaluated with the
// same helpers enforcement uses.
func (repository *WorkflowPolicyRepository) LoadIssueGateSummary(ctx context.Context, command ports.IssueGateSummaryCommand) (domain.WorkContextGateSummary, error) {
	if command.Now.IsZero() {
		return domain.WorkContextGateSummary{}, domain.NewError(domain.CodeInvalidArgument, "gate summary command timestamp is required", false)
	}
	var summary domain.WorkContextGateSummary
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		issue, err := loadIssueForGateDiagnostic(ctx, query, command.Identifier)
		if err != nil {
			return err
		}
		// buildWorkContextGateSummary reads only ID, Type and
		// AcceptanceCriteria from the issue; labels it loads itself.
		summary, err = buildWorkContextGateSummary(ctx, query, issue.ID, domain.Issue{
			ID:                 issue.ID,
			Type:               issue.Type,
			AcceptanceCriteria: issue.AcceptanceCriteria,
		}, command.Now.UTC())
		return err
	})
	if err != nil {
		return domain.WorkContextGateSummary{}, err
	}
	return summary, nil
}
