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
		// This also resolves a display identifier to the internal ID every
		// read below is keyed by.
		issue, err := loadIssueForGateDiagnostic(ctx, query, command.Identifier)
		if err != nil {
			return err
		}

		// Assemble evidence: acceptance criteria blankness, attempt evidence keys,
		// and review approval purposes.
		evidenceBlank := acceptanceCriteriaBlank(issue.AcceptanceCriteria)
		// readSnapshot hands over a *sql.Conn, which implements Executor. A
		// checked assertion keeps a future Queryer that does not from
		// panicking inside a read-only diagnostic.
		executor, ok := query.(Executor)
		if !ok {
			return domain.NewError(domain.CodeStorageConfiguration, "snapshot connection does not support statement execution", false)
		}
		attemptKeys := make(map[string]bool)
		if command.AttemptID != nil {
			attemptKeys, err = loadAttemptEvidenceKeys(ctx, executor, *command.AttemptID)
			if err != nil {
				return err
			}
		}
		approvalPurposes, err := loadIssueReviewApprovalPurposes(ctx, executor, issue.ID)
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

// gateDiagnosticIssue is the slice of an issue a gate decision reads. ID is
// the resolved internal identifier, so callers keyed by it work whichever
// form the request used.
type gateDiagnosticIssue struct {
	ID                 string
	Type               domain.Type
	Labels             []string
	AcceptanceCriteria *string
}

// loadIssueForGateDiagnostic loads only the fields needed for gate
// diagnostics: internal ID, type, labels, and acceptance criteria. It accepts
// either identifier form, branching exactly like IssueRepository.GetIssue --
// the diagnostic's input schema advertises both, so resolving only the ULID
// here would reject an ISSUE-N the contract promises to accept.
func loadIssueForGateDiagnostic(ctx context.Context, query Queryer, identifier domain.IssueIdentifier) (gateDiagnosticIssue, error) {
	result := gateDiagnosticIssue{}

	const projection = `SELECT id, type, acceptance_criteria FROM issues`
	var row *sql.Row
	switch identifier.Kind {
	case domain.IssueIdentifierInternalID:
		row = query.QueryRowContext(ctx, projection+` WHERE id = ?`, identifier.Value)
	case domain.IssueIdentifierDisplayID:
		row = query.QueryRowContext(ctx, projection+` WHERE sequence_no = ?`, identifier.SequenceNo)
	default:
		return result, domain.NewError(
			domain.CodeInvalidArgument,
			"issue identifier is invalid",
			false,
			domain.Detail{Field: "issue_id", Code: "INVALID_IDENTIFIER"},
		)
	}

	var issueType string
	if err := row.Scan(&result.ID, &issueType, &result.AcceptanceCriteria); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
		}
		return result, err
	}

	result.Type = domain.Type(issueType)
	if !result.Type.Valid() {
		return result, domain.WrapError(nil, domain.CodeStorageCorrupt, "stored issue type is invalid", false)
	}

	// Load labels from the issue_labels junction table.
	labelObjects, err := loadIssueLabels(ctx, query, result.ID)
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
