package domain_test

import (
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
)

var fixedFuzzTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// requirementKindAppliesAt mirrors the fixed applicability matrix in
// docs/02 §17.4 (also implemented, unexported, in workflow_policy.go) so
// this externally-package-scoped fuzz test can assert the partition
// invariant without reaching into domain's unexported method.
func requirementKindAppliesAt(kind domain.RequirementKind, point domain.EnforcementPoint) bool {
	switch kind {
	case domain.RequirementKindIssueFieldNonblank:
		return point == domain.EnforcementPointClaimWork || point == domain.EnforcementPointCompleteWorkToReview || point == domain.EnforcementPointCompleteWorkToDone
	case domain.RequirementKindAttemptEvidence:
		return point == domain.EnforcementPointCompleteWorkToReview || point == domain.EnforcementPointCompleteWorkToDone
	case domain.RequirementKindReviewApproval:
		return point == domain.EnforcementPointCompleteWorkToDone || point == domain.EnforcementPointApproveReview
	default:
		return false
	}
}

// FuzzWorkflowPolicyInputValidateNoPanic proves Validate never panics for
// arbitrary requirement input and, whenever it does accept a requirement,
// re-validating its own normalized output succeeds identically -- Validate
// has no second fixed point.
func FuzzWorkflowPolicyInputValidateNoPanic(f *testing.F) {
	for _, seed := range []string{
		"acceptance_criteria", "implementation", "security", "", "  spaced  ",
		"Acceptance_Criteria", "a/b", "with\x00nul", "unicode-Ключ",
	} {
		f.Add(seed, uint8(0))
		f.Add(seed, uint8(1))
		f.Add(seed, uint8(2))
		f.Add(seed, uint8(3))
	}
	f.Fuzz(func(t *testing.T, text string, kindSelector uint8) {
		kind := []domain.RequirementKind{
			domain.RequirementKindIssueFieldNonblank,
			domain.RequirementKindAttemptEvidence,
			domain.RequirementKindReviewApproval,
			domain.RequirementKind("invalid"),
		}[int(kindSelector)%4]

		input := domain.PolicyRequirementInput{Key: text, Kind: kind}
		switch kind {
		case domain.RequirementKindIssueFieldNonblank:
			input.Field = text
		case domain.RequirementKindAttemptEvidence:
			input.EvidenceKey = text
		case domain.RequirementKindReviewApproval:
			input.Purpose = text
		}

		policy := domain.WorkflowPolicyInput{Requirements: []domain.PolicyRequirementInput{input}}
		validated, err := policy.Validate()
		if err != nil {
			return // invalid input; nothing to prove about a second pass
		}
		if len(validated.Requirements) != 1 {
			t.Fatalf("Validate() accepted 1 requirement but returned %d", len(validated.Requirements))
		}
		again := domain.WorkflowPolicyInput{Requirements: validated.Requirements}
		reValidated, err := again.Validate()
		if err != nil {
			t.Fatalf("re-validating Validate()'s own normalized output failed: %v", err)
		}
		if reValidated.Requirements[0] != validated.Requirements[0] {
			t.Fatalf("Validate() not idempotent on its own output: %#v -> %#v", validated.Requirements[0], reValidated.Requirements[0])
		}
	})
}

// FuzzGateSnapshotFingerprintOrderIndependent proves NewGateSnapshot's
// fingerprint depends only on the logical content of its source policy list,
// never on the order the caller supplies it in.
func FuzzGateSnapshotFingerprintOrderIndependent(f *testing.F) {
	f.Add("01AAAAAAAAAAAAAAAAAAAAAAAA", int64(1), "01BBBBBBBBBBBBBBBBBBBBBBBB", int64(2), int64(7))
	f.Add("", int64(0), "", int64(0), int64(0))
	f.Fuzz(func(t *testing.T, policyIDA string, versionA int64, policyIDB string, versionB int64, issueVersion int64) {
		requirements := []domain.PolicyRequirement{
			{PolicyID: policyIDA, Key: "k1", Kind: domain.RequirementKindReviewApproval, Purpose: "security"},
		}
		forward := []domain.SourcePolicyRef{{PolicyID: policyIDA, Version: versionA}, {PolicyID: policyIDB, Version: versionB}}
		backward := []domain.SourcePolicyRef{{PolicyID: policyIDB, Version: versionB}, {PolicyID: policyIDA, Version: versionA}}

		first, err := domain.NewGateSnapshot(requirements, forward, issueVersion, fixedFuzzTime)
		if err != nil {
			t.Fatalf("NewGateSnapshot(forward) error = %v", err)
		}
		second, err := domain.NewGateSnapshot(requirements, backward, issueVersion, fixedFuzzTime)
		if err != nil {
			t.Fatalf("NewGateSnapshot(backward) error = %v", err)
		}
		if policyIDA == policyIDB {
			return // ambiguous ordering when the two source policy IDs collide; nothing to prove
		}
		if first.Fingerprint != second.Fingerprint {
			t.Fatalf("fingerprint depends on source-policy order: forward=%q backward=%q", first.Fingerprint, second.Fingerprint)
		}
	})
}

// FuzzEvaluateGateNoPanicAndPartitionsRequirements proves EvaluateGate never
// panics for arbitrary requirement/evidence combinations and that every
// applicable requirement appears in exactly one of Satisfied or Unmet, never
// both and never neither.
func FuzzEvaluateGateNoPanicAndPartitionsRequirements(f *testing.F) {
	f.Add("k", uint8(0), uint8(0), true, true, true)
	f.Add("", uint8(3), uint8(1), false, false, false)
	f.Fuzz(func(t *testing.T, key string, kindSelector, pointSelector uint8, acBlank, evidencePresent, approvalPresent bool) {
		kind := []domain.RequirementKind{
			domain.RequirementKindIssueFieldNonblank,
			domain.RequirementKindAttemptEvidence,
			domain.RequirementKindReviewApproval,
		}[int(kindSelector)%3]
		point := []domain.EnforcementPoint{
			domain.EnforcementPointClaimWork,
			domain.EnforcementPointCompleteWorkToReview,
			domain.EnforcementPointCompleteWorkToDone,
			domain.EnforcementPointApproveReview,
		}[int(pointSelector)%4]

		requirement := domain.PolicyRequirement{PolicyID: "p", Key: key, Kind: kind}
		switch kind {
		case domain.RequirementKindIssueFieldNonblank:
			requirement.Field = "acceptance_criteria"
		case domain.RequirementKindAttemptEvidence:
			requirement.EvidenceKey = "e"
		case domain.RequirementKindReviewApproval:
			requirement.Purpose = "p"
		}

		evidence := domain.GateEvidence{AcceptanceCriteriaBlank: acBlank}
		if evidencePresent {
			evidence.AttemptEvidenceKeys = map[string]bool{"e": true}
		}
		if approvalPresent {
			evidence.ReviewApprovalPurposes = map[string]bool{"p": true}
		}

		evaluation, err := domain.EvaluateGate(point, []domain.PolicyRequirement{requirement}, evidence)
		if err != nil {
			t.Fatalf("EvaluateGate() error = %v", err)
		}
		total := len(evaluation.Satisfied) + len(evaluation.Unmet)
		if requirementKindAppliesAt(requirement.Kind, point) {
			if total != 1 {
				t.Fatalf("applicable requirement appears %d times across Satisfied+Unmet, want exactly 1", total)
			}
		} else if total != 0 {
			t.Fatalf("inapplicable requirement appears %d times across Satisfied+Unmet, want 0", total)
		}
	})
}
