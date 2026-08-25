package sqlite

import (
	"context"
	"fmt"
	"time"

	"rhizome-mcp/internal/domain"
)

// buildWorkContextGateSummary assembles get_work_context's always-populated
// gate summary (ISSUE-175 AC1).
//
// It reuses the same helpers the enforcement and diagnostic paths use --
// acceptanceCriteriaBlank, loadAttemptEvidenceKeys,
// loadIssueReviewApprovalPurposes, loadGateSnapshot, loadActiveWorkflowPolicies
// and domain.EvaluateGate -- rather than re-deriving any of them, so what
// context reports and what a mutation enforces cannot drift apart.
//
// The enforcement point follows the issue's own state: an active attempt is
// heading for complete_work_to_done and will be re-evaluated against its own
// frozen snapshot (docs/02 §17.6), while an unclaimed issue faces claim_work
// against whatever policies are live now.
func buildWorkContextGateSummary(
	ctx context.Context,
	query Queryer,
	issueID string,
	issue domain.Issue,
	now time.Time,
) (domain.WorkContextGateSummary, error) {
	summary := domain.WorkContextGateSummary{
		Point:       domain.EnforcementPointClaimWork,
		Unmet:       []domain.WorkContextUnmetRequirement{},
		NextActions: []string{},
	}

	executor, ok := query.(Executor)
	if !ok {
		return summary, domain.NewError(domain.CodeStorageConfiguration, "snapshot connection does not support statement execution", false)
	}

	activeAttemptID, err := activeAttemptIDForIssue(ctx, query, issueID, now)
	if err != nil {
		return summary, err
	}

	attemptKeys := make(map[string]bool)
	if activeAttemptID != "" {
		summary.Point = domain.EnforcementPointCompleteWorkToDone
		attemptKeys, err = loadAttemptEvidenceKeys(ctx, executor, activeAttemptID)
		if err != nil {
			return summary, err
		}
	}
	approvalPurposes, err := loadIssueReviewApprovalPurposes(ctx, executor, issueID)
	if err != nil {
		return summary, err
	}
	evidence := domain.GateEvidence{
		AcceptanceCriteriaBlank: acceptanceCriteriaBlank(issue.AcceptanceCriteria),
		AttemptEvidenceKeys:     attemptKeys,
		ReviewApprovalPurposes:  approvalPurposes,
	}

	requirements, fingerprint, err := workContextGateRequirements(ctx, query, issueID, issue, activeAttemptID)
	if err != nil {
		return summary, err
	}
	summary.SnapshotFingerprint = fingerprint

	evaluation, err := domain.EvaluateGate(summary.Point, requirements, evidence)
	if err != nil {
		return summary, err
	}
	summary.RequirementCount = int64(len(requirements))
	summary.SatisfiedCount = int64(len(evaluation.Satisfied))

	byKey := make(map[string]domain.PolicyRequirement, len(requirements))
	for _, requirement := range requirements {
		byKey[requirement.PolicyID+"\x00"+requirement.Key] = requirement
	}
	for _, unmet := range evaluation.Unmet {
		summary.Unmet = append(summary.Unmet, domain.WorkContextUnmetRequirement{
			PolicyID:       unmet.PolicyID,
			RequirementKey: unmet.RequirementKey,
			Reason:         unmet.Reason,
		})
		summary.NextActions = append(summary.NextActions, gateNextAction(byKey[unmet.PolicyID+"\x00"+unmet.RequirementKey]))
	}
	return summary, nil
}

// workContextGateRequirements returns the requirements the summary is
// evaluated against, plus the snapshot fingerprint when a frozen snapshot
// supplied them. A missing snapshot falls through to live policies exactly as
// the mutation path does: gates were not active when that attempt started, so
// reporting a failure the mutation would not report is the drift to avoid.
func workContextGateRequirements(
	ctx context.Context,
	query Queryer,
	issueID string,
	issue domain.Issue,
	activeAttemptID string,
) ([]domain.PolicyRequirement, *string, error) {
	if activeAttemptID != "" {
		snapshot, err := loadGateSnapshot(ctx, query, "attempt_gate_snapshots", "attempt_id", activeAttemptID)
		switch {
		case err == nil:
			fingerprint := snapshot.Fingerprint
			return snapshot.Requirements, &fingerprint, nil
		case !gateSnapshotNotFound(err):
			return nil, nil, err
		}
	}

	labelObjects, err := loadIssueLabels(ctx, query, issueID)
	if err != nil {
		return nil, nil, err
	}
	labels := make([]string, len(labelObjects))
	for index, label := range labelObjects {
		labels[index] = label.Name
	}
	policies, err := loadActiveWorkflowPolicies(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	return domain.MatchWorkflowPolicies(policies, issue.Type, labels), nil, nil
}

// gateNextAction states what clears one unmet requirement. It is deliberately
// imperative and names the tool to call: the summary exists so an agent can
// act without loading the policy body it came from.
func gateNextAction(requirement domain.PolicyRequirement) string {
	switch requirement.Kind {
	case domain.RequirementKindIssueFieldNonblank:
		return fmt.Sprintf("set a non-blank %s on the issue with update_issue", requirement.Field)
	case domain.RequirementKindAttemptEvidence:
		return fmt.Sprintf("submit_gate_evidence for key %q on the active attempt", requirement.EvidenceKey)
	case domain.RequirementKindReviewApproval:
		return fmt.Sprintf("obtain an approved review covering purpose %q", requirement.Purpose)
	default:
		return "satisfy the requirement before this enforcement point"
	}
}

// activeAttemptIDForIssue returns the issue's live attempt, or "" when the
// issue is unclaimed or its lease has expired. An expired lease is not an
// active attempt, matching effective-status derivation.
func activeAttemptIDForIssue(ctx context.Context, query Queryer, issueID string, now time.Time) (string, error) {
	var attemptID string
	row := query.QueryRowContext(ctx,
		`SELECT id FROM work_attempts WHERE issue_id = ? AND status = 'active' AND lease_expires_at > ? LIMIT 1`,
		issueID, formatStorageTime(now))
	if err := row.Scan(&attemptID); err != nil {
		if isNoRowsError(err) {
			return "", nil
		}
		return "", err
	}
	return attemptID, nil
}
