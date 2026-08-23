package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"rhizome-mcp/internal/domain"
)

// This file is ISSUE-172's single choke point for workflow-gate evaluation:
// every write path that can set or change an issue's status to review or
// done -- claim_issue, finish_attempt, create_issue, apply_issue_plan --
// calls into evaluateGateAgainstLivePolicies or
// evaluateGateAgainstAttemptSnapshot before performing its own write, inside
// the same transaction. update_issue's direct-transition path needs neither:
// it rejects a review/done target unconditionally at the domain layer
// (domain.ApplyIssuePatch), per docs/02 §17.1. apply_import is exempt from
// gate evaluation entirely (docs/02 §17.1, ISSUE-201).

// evaluateGateAgainstLivePolicies is the choke point for enforcement points
// with no prior attempt or review context to freeze against: claim_work
// (before an attempt exists), and the create-time paths ISSUE-201 routes
// through complete_work_to_review/complete_work_to_done (create_issue,
// apply_issue_plan). It loads every active policy inside tx, matches it
// against (issueType, issueLabels) per docs/02 §17.3, and evaluates evidence
// at point. On success it also returns the matched requirements and their
// source policies so claim_work can freeze them into an
// attempt_gate_snapshot; create/plan callers have no attempt to snapshot
// and ignore those two return values.
func evaluateGateAgainstLivePolicies(
	ctx context.Context, tx Executor, point domain.EnforcementPoint,
	issueType domain.Type, issueLabels []string, evidence domain.GateEvidence,
) ([]domain.PolicyRequirement, []domain.SourcePolicyRef, error) {
	policies, err := loadActiveWorkflowPolicies(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	requirements := domain.MatchWorkflowPolicies(policies, issueType, issueLabels)
	sourcePolicies := matchingSourcePolicies(policies, issueType, issueLabels)
	if err := evaluateGate(point, requirements, evidence); err != nil {
		return nil, nil, err
	}
	return requirements, sourcePolicies, nil
}

// evaluateGateAgainstAttemptSnapshot is the choke point for finish_attempt's
// three completion paths (complete_work_to_review, complete_work_to_done,
// approve_review): it re-evaluates the attempt's own frozen claim-time
// requirement snapshot against live evidence, per docs/02 §17.6 -- never
// against whatever policies are active at completion time. A missing
// snapshot (an attempt claimed before gates were wired into claim_work) is
// treated as zero requirements, not an error: gates were not yet active
// when that attempt started, so there is nothing to re-check.
//
// approve_review interim note: docs/02 describes a review-target snapshot,
// frozen at review-request creation, for this path. That snapshot table
// (review_target_gate_snapshots) already exists, but nothing writes it yet
// -- creating one is ISSUE-173's job, alongside purpose-scoped review
// requests. Until ISSUE-173 lands, a review attempt's OWN claim-time
// snapshot (written by claim_work, exactly like a work attempt's) is the
// best available frozen source and is used here instead; ISSUE-173 replaces
// this call for approve_review with a review-target snapshot read.
func evaluateGateAgainstAttemptSnapshot(
	ctx context.Context, tx Executor, point domain.EnforcementPoint, attemptID string, evidence domain.GateEvidence,
) error {
	snapshot, err := loadGateSnapshot(ctx, tx, "attempt_gate_snapshots", "attempt_id", attemptID)
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Code == domain.CodeGateSnapshotNotFound {
			return nil
		}
		return err
	}
	return evaluateGate(point, snapshot.Requirements, evidence)
}

// evaluateGateAgainstReviewTargetSnapshot is approve_review's choke point
// once a review request is actually bound (ISSUE-173, replacing the
// ISSUE-172 interim behavior of reusing the reviewing attempt's own
// claim-time snapshot -- decision 01M0Q4ZVHAAR8ZVGYX33X0V46M): it
// re-evaluates the review target's own frozen review_approval requirement
// snapshot (docs/02 §17.6), never the resolving attempt's claim-time
// snapshot and never live policy state. A missing snapshot is treated as
// zero requirements, matching evaluateGateAgainstAttemptSnapshot's identical
// defensive handling -- every target created through ensureTarget always
// has one, so this only guards a target that somehow predates that code
// path.
func evaluateGateAgainstReviewTargetSnapshot(ctx context.Context, tx Executor, targetID string, evidence domain.GateEvidence) error {
	snapshot, err := loadGateSnapshot(ctx, tx, "review_target_gate_snapshots", "target_id", targetID)
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Code == domain.CodeGateSnapshotNotFound {
			return evaluateGate(domain.EnforcementPointApproveReview, nil, evidence)
		}
		return err
	}
	return evaluateGate(domain.EnforcementPointApproveReview, snapshot.Requirements, evidence)
}

func evaluateGate(point domain.EnforcementPoint, requirements []domain.PolicyRequirement, evidence domain.GateEvidence) error {
	evaluation, err := domain.EvaluateGate(point, requirements, evidence)
	if err != nil {
		return err
	}
	if evaluation.Passed() {
		return nil
	}
	return workflowGateUnsatisfiedError(evaluation)
}

// workflowGateUnsatisfiedError builds the WORKFLOW_GATE_UNSATISFIED error,
// one Detail per unmet requirement, in the shape docs/02 §17.7 and
// docs/03 §13 lock: Field is the requirement_key (the natural "what
// failed" identifier, consistent with every other validation error in this
// codebase); Message packs policy_id, enforcement_point, and reason into a
// readable string, following the same pack-identifying-dimensions-into-one-
// detail pattern migrations.go uses for FOREIGN_KEY_VIOLATION. The message
// text and the detail shape are both asserted by
// TestWorkflowGateUnsatisfiedDetailShapeMatchesDocumentedContract so they
// cannot drift from the docs silently (ISSUE-220).
func workflowGateUnsatisfiedError(evaluation domain.GateEvaluation) error {
	details := make([]domain.Detail, len(evaluation.Unmet))
	for index, unmet := range evaluation.Unmet {
		details[index] = domain.Detail{
			Field:   unmet.RequirementKey,
			Code:    domain.CodeWorkflowGateUnsatisfied,
			Message: fmt.Sprintf("policy_id=%s enforcement_point=%s: %s", unmet.PolicyID, unmet.EnforcementPoint, unmet.Reason),
		}
	}
	return domain.NewError(domain.CodeWorkflowGateUnsatisfied, "workflow gate requirements are not satisfied", false, details...)
}

func loadActiveWorkflowPolicies(ctx context.Context, query Queryer) ([]domain.WorkflowPolicy, error) {
	rows, err := query.QueryContext(ctx, `SELECT id, selector_json, requirements_json, status, version, created_at, updated_at
		FROM workflow_policies WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var policies []domain.WorkflowPolicy
	for rows.Next() {
		policy, err := scanWorkflowPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return policies, nil
}

// matchingSourcePolicies mirrors MatchWorkflowPolicies' own active+selector
// filter (domain/workflow_policy.go) to report every policy that
// contributed -- including one that matched but happens to own zero
// requirements, which would otherwise leave no trace in the requirement
// list MatchWorkflowPolicies returns.
func matchingSourcePolicies(policies []domain.WorkflowPolicy, issueType domain.Type, issueLabels []string) []domain.SourcePolicyRef {
	var refs []domain.SourcePolicyRef
	for _, policy := range policies {
		if policy.Status != domain.PolicyStatusActive {
			continue
		}
		if !policy.Selector.Matches(issueType, issueLabels) {
			continue
		}
		refs = append(refs, domain.SourcePolicyRef{PolicyID: policy.ID, Version: policy.Version})
	}
	return refs
}

// acceptanceCriteriaBlank reports whether text is blank once trimmed -- the
// issue_field_nonblank evaluation always uses the current live value
// (docs/02 §17.5), whether from a loaded issue or from a create/plan input
// that has not been persisted yet.
func acceptanceCriteriaBlank(text *string) bool {
	return text == nil || strings.TrimSpace(*text) == ""
}

// loadAttemptEvidenceKeys returns the set of gate_evidence keys recorded on
// attemptID, regardless of result (satisfied or not_applicable): submission
// already validated a not_applicable result against the matching
// requirement's allow_not_applicable flag (ISSUE-171, gate_evidence.go), so
// any recorded key is acceptable evidence by the time a gate re-checks it.
func loadAttemptEvidenceKeys(ctx context.Context, tx Executor, attemptID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key FROM gate_evidence WHERE attempt_id = ?`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// enforcementPointForCreateStatus maps a create-time status to the
// enforcement point it is gated as (ISSUE-201): review and done route
// through the matching completion point, evaluated as a transition from a
// virtual open; every other status (including cancelled) is ungated.
func enforcementPointForCreateStatus(status domain.Status) (domain.EnforcementPoint, bool) {
	switch status {
	case domain.StatusReview:
		return domain.EnforcementPointCompleteWorkToReview, true
	case domain.StatusDone:
		return domain.EnforcementPointCompleteWorkToDone, true
	default:
		return "", false
	}
}

// enforcementPointForFinish maps a resolved finish_attempt outcome to the
// enforcement point it is gated as (docs/02 §17.1): a work attempt
// completing to review or done, or a review attempt whose review_outcome is
// approved. Every other completion (work-to-ready, work-to-blocked,
// changes_requested, blocked) is ungated.
func enforcementPointForFinish(kind domain.AttemptKind, target domain.Status, reviewOutcome *domain.ReviewOutcome) (domain.EnforcementPoint, bool) {
	if kind == domain.AttemptKindWork {
		switch target {
		case domain.StatusReview:
			return domain.EnforcementPointCompleteWorkToReview, true
		case domain.StatusDone:
			return domain.EnforcementPointCompleteWorkToDone, true
		default:
			return "", false
		}
	}
	if kind == domain.AttemptKindReview && reviewOutcome != nil && *reviewOutcome == domain.ReviewOutcomeApproved {
		return domain.EnforcementPointApproveReview, true
	}
	return "", false
}
