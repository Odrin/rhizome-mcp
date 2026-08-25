package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// LoadGateDiagnostic assembles the read-only inputs needed for a gate decision,
// using either a frozen snapshot (if AttemptID or TargetID is set) or live
// policies (if neither is set).
func (repository *WorkflowPolicyRepository) LoadGateDiagnostic(ctx context.Context, command ports.GateDiagnosticCommand) (ports.GateDiagnosticResult, error) {
	var result ports.GateDiagnosticResult
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		// Load the issue to check if it exists and get its type and labels.
		issue, err := loadIssueForGateDiagnostic(ctx, query, command.IssueID)
		if err != nil {
			return err
		}

		// Assemble evidence: acceptance criteria blankness, attempt evidence keys,
		// and review approval purposes.
		evidenceBlank := acceptanceCriteriaBlank(issue.AcceptanceCriteria)
		attemptKeys := make(map[string]bool)
		if command.AttemptID != nil {
			// The query parameter is actually a *sql.Conn which implements Executor.
			executor := query.(Executor)
			attemptKeys, err = loadAttemptEvidenceKeys(ctx, executor, *command.AttemptID)
			if err != nil {
				return err
			}
		}
		// The query parameter is actually a *sql.Conn which implements Executor.
		executor := query.(Executor)
		approvalPurposes, err := loadIssueReviewApprovalPurposes(ctx, executor, command.IssueID)
		if err != nil {
			return err
		}

		result.Evidence = domain.GateEvidence{
			AcceptanceCriteriaBlank: evidenceBlank,
			AttemptEvidenceKeys:     attemptKeys,
			ReviewApprovalPurposes:  approvalPurposes,
		}

		// Load requirements and source policies from either a frozen snapshot or live policies.
		// A frozen snapshot, when one was asked for and exists, is the whole
		// answer: the mutation path re-evaluates an attempt or review target
		// against its own claim-time requirements, never against whatever
		// policies happen to be active now (docs/02 §17.6).
		table, column, key := "", "", ""
		switch {
		case command.AttemptID != nil:
			table, column, key = "attempt_gate_snapshots", "attempt_id", *command.AttemptID
		case command.TargetID != nil:
			table, column, key = "review_target_gate_snapshots", "target_id", *command.TargetID
		}
		if key != "" {
			snapshot, err := loadGateSnapshot(ctx, query, table, column, key)
			switch {
			case err == nil:
				result.Requirements = snapshot.Requirements
				result.SourcePolicies = snapshot.SourcePolicies
				result.SnapshotFound = true
				return nil
			case !gateSnapshotNotFound(err):
				return err
			}
			// A missing snapshot is not an error: it means gates were not yet
			// active when that attempt started. The mutation path treats it as
			// zero requirements, so the diagnostic falls through to live
			// policies rather than reporting a failure the mutation would not.
		}

		// No snapshot (or none was requested): use live policies.
		policies, err := loadActiveWorkflowPolicies(ctx, query)
		if err != nil {
			return err
		}
		result.Requirements = domain.MatchWorkflowPolicies(policies, issue.Type, issue.Labels)
		result.SourcePolicies = matchingSourcePolicies(policies, issue.Type, issue.Labels)
		result.SnapshotFound = false
		return nil
	})
	if err != nil {
		return ports.GateDiagnosticResult{}, err
	}
	return result, nil
}

// loadIssueForGateDiagnostic loads only the fields needed for gate diagnostics:
// type, labels, and acceptance criteria.
func loadIssueForGateDiagnostic(ctx context.Context, query Queryer, issueID string) (struct {
	Type               domain.Type
	Labels             []string
	AcceptanceCriteria *string
}, error) {
	result := struct {
		Type               domain.Type
		Labels             []string
		AcceptanceCriteria *string
	}{}

	row := query.QueryRowContext(ctx, `SELECT type, acceptance_criteria FROM issues WHERE id = ?`, issueID)
	var issueType string
	if err := row.Scan(&issueType, &result.AcceptanceCriteria); err != nil {
		if err == sql.ErrNoRows {
			return result, domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
		}
		return result, err
	}

	result.Type = domain.Type(issueType)
	if !result.Type.Valid() {
		return result, domain.WrapError(nil, domain.CodeStorageCorrupt, "stored issue type is invalid", false)
	}

	// Load labels from the issue_labels junction table.
	labelObjects, err := loadIssueLabels(ctx, query, issueID)
	if err != nil {
		return result, err
	}
	labelNames := make([]string, len(labelObjects))
	for i, label := range labelObjects {
		labelNames[i] = label.Name
	}
	result.Labels = labelNames

	return result, nil
}

// gateSnapshotNotFound reports whether err is the domain's
// "snapshot not found" signal. It uses errors.As, matching
// evaluateGateAgainstAttemptSnapshot exactly: a bare type assertion would stop
// recognising the condition the moment loadGateSnapshot wrapped its error, and
// the diagnostic would then report a failure where the mutation path reports
// none -- precisely the drift this diagnostic exists to rule out.
func gateSnapshotNotFound(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Code == domain.CodeGateSnapshotNotFound
}
