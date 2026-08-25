package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// EnforcementPoint identifies one of the four fixed points gates are
// evaluated at (docs/02 §17.1). No other point evaluates gates.
type EnforcementPoint string

const (
	EnforcementPointClaimWork            EnforcementPoint = "claim_work"
	EnforcementPointCompleteWorkToReview EnforcementPoint = "complete_work_to_review"
	EnforcementPointCompleteWorkToDone   EnforcementPoint = "complete_work_to_done"
	EnforcementPointApproveReview        EnforcementPoint = "approve_review"
)

// Valid reports whether point is one of the four fixed enforcement points.
func (point EnforcementPoint) Valid() bool {
	switch point {
	case EnforcementPointClaimWork, EnforcementPointCompleteWorkToReview, EnforcementPointCompleteWorkToDone, EnforcementPointApproveReview:
		return true
	default:
		return false
	}
}

// GateEvidence is the live state EvaluateGate needs to decide requirement
// satisfaction (docs/02 §17.5). It is supplied by the caller; EvaluateGate
// performs no storage reads of its own.
type GateEvidence struct {
	// AcceptanceCriteriaBlank reports whether the issue's current
	// acceptance_criteria field is blank (trimmed, empty). issue_field_nonblank
	// is always evaluated against the issue's current stored value, never a
	// snapshot.
	AcceptanceCriteriaBlank bool
	// AttemptEvidenceKeys is the set of evidence keys recorded on the
	// completing work attempt, evaluated against the attempt's frozen
	// requirement snapshot, not live policy state.
	AttemptEvidenceKeys map[string]bool
	// ReviewApprovalPurposes is the set of purposes with an existing
	// immutable approval record for the issue.
	ReviewApprovalPurposes map[string]bool
}

// UnmetRequirement is one unsatisfied requirement at one enforcement point.
// All four dimensions are structured here in the domain; the
// WORKFLOW_GATE_UNSATISFIED error transport packs them into the project-wide
// {field, code, message} detail shape — requirement_key in field, the rest in
// message (docs/02 §17.7, docs/03 §13).
type UnmetRequirement struct {
	PolicyID         string
	RequirementKey   string
	EnforcementPoint EnforcementPoint
	Reason           string
}

// GateEvaluation is the pure result of evaluating a frozen requirement list
// against live evidence at one enforcement point. Requirement kinds that do
// not apply at Point (docs/02 §17.4) are neither satisfied nor unmet: they
// are simply absent from both slices.
type GateEvaluation struct {
	Point     EnforcementPoint
	Satisfied []PolicyRequirement
	Unmet     []UnmetRequirement
}

// Passed reports whether every applicable requirement was satisfied.
func (evaluation GateEvaluation) Passed() bool {
	return len(evaluation.Unmet) == 0
}

// CloneGateEvaluation returns an evaluation with no shared slice data.
func CloneGateEvaluation(evaluation GateEvaluation) GateEvaluation {
	evaluation.Satisfied = slices.Clone(evaluation.Satisfied)
	evaluation.Unmet = slices.Clone(evaluation.Unmet)
	return evaluation
}

// EvaluateGate evaluates requirements (the frozen effective set for one
// attempt or review target, per docs/02 §17.6) against evidence at point. It
// performs no storage reads and never silently ignores a requirement whose
// kind it does not recognize: an invalid kind fails evaluation outright
// rather than being treated as inapplicable.
func EvaluateGate(point EnforcementPoint, requirements []PolicyRequirement, evidence GateEvidence) (GateEvaluation, error) {
	if !point.Valid() {
		return GateEvaluation{}, invalidEnum("enforcement_point", string(point))
	}
	result := GateEvaluation{Point: point}
	for _, requirement := range requirements {
		if !requirement.Kind.Valid() {
			return GateEvaluation{}, invalidEnum("kind", string(requirement.Kind))
		}
		if !requirement.Kind.appliesAt(point) {
			continue
		}
		satisfied, reason := requirement.isSatisfied(evidence)
		if satisfied {
			result.Satisfied = append(result.Satisfied, requirement)
			continue
		}
		result.Unmet = append(result.Unmet, UnmetRequirement{
			PolicyID: requirement.PolicyID, RequirementKey: requirement.Key,
			EnforcementPoint: point, Reason: reason,
		})
	}
	return result, nil
}

func (requirement PolicyRequirement) isSatisfied(evidence GateEvidence) (bool, string) {
	switch requirement.Kind {
	case RequirementKindIssueFieldNonblank:
		if evidence.AcceptanceCriteriaBlank {
			return false, fmt.Sprintf("issue field '%s' is blank", requirement.Field)
		}
		return true, ""
	case RequirementKindAttemptEvidence:
		if !evidence.AttemptEvidenceKeys[requirement.EvidenceKey] {
			return false, fmt.Sprintf("no attempt_evidence recorded for key '%s'", requirement.EvidenceKey)
		}
		return true, ""
	case RequirementKindReviewApproval:
		if !evidence.ReviewApprovalPurposes[requirement.Purpose] {
			return false, fmt.Sprintf("no review_approval recorded for purpose '%s'", requirement.Purpose)
		}
		return true, ""
	default:
		return false, "unrecognized requirement kind"
	}
}

// SourcePolicyRef identifies one policy that contributed to a GateSnapshot,
// pinned to the version it was read at.
type SourcePolicyRef struct {
	PolicyID string
	Version  int64
}

// GateSnapshot is the immutable, frozen requirement set captured at claim
// time (for a work attempt) or review-request-creation time (for a review
// target), per docs/02 §17.6. An existing snapshot never changes.
type GateSnapshot struct {
	Requirements   []PolicyRequirement
	SourcePolicies []SourcePolicyRef
	Fingerprint    string
	IssueVersion   int64
	CreatedAt      time.Time
}

// CloneGateSnapshot returns a snapshot with no shared slice data.
func CloneGateSnapshot(snapshot GateSnapshot) GateSnapshot {
	snapshot.Requirements = slices.Clone(snapshot.Requirements)
	snapshot.SourcePolicies = slices.Clone(snapshot.SourcePolicies)
	return snapshot
}

// canonicalSnapshotPayload is the exact shape hashed to produce a
// GateSnapshot's fingerprint. Field order is fixed by struct declaration
// order (encoding/json preserves it) and slice order is the caller's
// (already canonical, per MatchWorkflowPolicies' policy-ID-then-key
// ordering), so two semantically equivalent inputs always marshal to
// byte-identical JSON and two semantically different inputs practically
// never collide (SHA-256).
type canonicalSnapshotPayload struct {
	Requirements   []PolicyRequirement `json:"requirements"`
	SourcePolicies []SourcePolicyRef   `json:"source_policies"`
	IssueVersion   int64               `json:"issue_version"`
}

// NewGateSnapshot builds a GateSnapshot from a canonically ordered
// requirement set (as returned by MatchWorkflowPolicies) and its
// contributing source policies, computing a SHA-256 fingerprint that is
// stable across equivalent normalized inputs and changes for every semantic
// requirement change.
func NewGateSnapshot(requirements []PolicyRequirement, sourcePolicies []SourcePolicyRef, issueVersion int64, now time.Time) (GateSnapshot, error) {
	orderedSources := slices.Clone(sourcePolicies)
	slices.SortFunc(orderedSources, func(a, b SourcePolicyRef) int {
		if a.PolicyID != b.PolicyID {
			if a.PolicyID < b.PolicyID {
				return -1
			}
			return 1
		}
		return 0
	})
	payload := canonicalSnapshotPayload{
		Requirements:   slices.Clone(requirements),
		SourcePolicies: orderedSources,
		IssueVersion:   issueVersion,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return GateSnapshot{}, WrapError(err, CodeInvalidArgument, "cannot canonicalize gate snapshot", false)
	}
	sum := sha256.Sum256(encoded)
	return GateSnapshot{
		Requirements:   slices.Clone(requirements),
		SourcePolicies: orderedSources,
		Fingerprint:    hex.EncodeToString(sum[:]),
		IssueVersion:   issueVersion,
		CreatedAt:      now,
	}, nil
}
