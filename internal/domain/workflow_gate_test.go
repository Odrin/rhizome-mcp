package domain

import (
	"testing"
	"time"
)

func TestEvaluateGateAppliesRequirementKindsOnlyAtTheirFixedPoints(t *testing.T) {
	requirements := []PolicyRequirement{
		{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "ac", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "impl", Kind: RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
		{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "sec", Kind: RequirementKindReviewApproval, Purpose: "security"},
	}
	// Evidence satisfies nothing, so every applicable requirement is unmet;
	// this isolates the applicability matrix from the satisfaction logic.
	blank := GateEvidence{AcceptanceCriteriaBlank: true}

	tests := []struct {
		point           EnforcementPoint
		wantUnmetKeys   []string
		wantIgnoredKeys []string
	}{
		{point: EnforcementPointClaimWork, wantUnmetKeys: []string{"ac"}, wantIgnoredKeys: []string{"impl", "sec"}},
		{point: EnforcementPointCompleteWorkToReview, wantUnmetKeys: []string{"ac", "impl"}, wantIgnoredKeys: []string{"sec"}},
		{point: EnforcementPointCompleteWorkToDone, wantUnmetKeys: []string{"ac", "impl", "sec"}},
		{point: EnforcementPointApproveReview, wantUnmetKeys: []string{"sec"}, wantIgnoredKeys: []string{"ac", "impl"}},
	}
	for _, test := range tests {
		t.Run(string(test.point), func(t *testing.T) {
			evaluation, err := EvaluateGate(test.point, requirements, blank)
			if err != nil {
				t.Fatalf("EvaluateGate() error = %v", err)
			}
			if len(evaluation.Satisfied) != 0 {
				t.Fatalf("Satisfied = %#v, want none (blank evidence)", evaluation.Satisfied)
			}
			gotKeys := make(map[string]bool, len(evaluation.Unmet))
			for _, unmet := range evaluation.Unmet {
				gotKeys[unmet.RequirementKey] = true
				if unmet.EnforcementPoint != test.point {
					t.Errorf("unmet[%s].EnforcementPoint = %q, want %q", unmet.RequirementKey, unmet.EnforcementPoint, test.point)
				}
				if unmet.PolicyID != "01AAAAAAAAAAAAAAAAAAAAAAAA" {
					t.Errorf("unmet[%s].PolicyID = %q, want the requirement's policy id", unmet.RequirementKey, unmet.PolicyID)
				}
			}
			for _, want := range test.wantUnmetKeys {
				if !gotKeys[want] {
					t.Errorf("Unmet missing key %q: %#v", want, evaluation.Unmet)
				}
			}
			for _, ignored := range test.wantIgnoredKeys {
				if gotKeys[ignored] {
					t.Errorf("Unmet unexpectedly contains inapplicable key %q: %#v", ignored, evaluation.Unmet)
				}
			}
			if len(evaluation.Unmet) != len(test.wantUnmetKeys) {
				t.Errorf("Unmet length = %d, want %d: %#v", len(evaluation.Unmet), len(test.wantUnmetKeys), evaluation.Unmet)
			}
		})
	}
}

func TestEvaluateGateSatisfactionReasonsMatchDocumentedWording(t *testing.T) {
	requirements := []PolicyRequirement{
		{PolicyID: "01J1POLICYAAAAAAAAAAAAAAAA", Key: "acceptance_criteria", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	}
	evaluation, err := EvaluateGate(EnforcementPointClaimWork, requirements, GateEvidence{AcceptanceCriteriaBlank: true})
	if err != nil {
		t.Fatalf("EvaluateGate() error = %v", err)
	}
	if len(evaluation.Unmet) != 1 || evaluation.Unmet[0].Reason != "issue field 'acceptance_criteria' is blank" {
		t.Fatalf("Unmet = %#v, want the docs/02 §17.8 worked-example wording", evaluation.Unmet)
	}

	implementation := []PolicyRequirement{{PolicyID: "p", Key: "implementation_evidence", Kind: RequirementKindAttemptEvidence, EvidenceKey: "implementation"}}
	evaluation, err = EvaluateGate(EnforcementPointCompleteWorkToReview, implementation, GateEvidence{})
	if err != nil {
		t.Fatalf("EvaluateGate() error = %v", err)
	}
	if len(evaluation.Unmet) != 1 || evaluation.Unmet[0].Reason != "no attempt_evidence recorded for key 'implementation'" {
		t.Fatalf("Unmet = %#v, want the docs/02 §17.8 worked-example wording", evaluation.Unmet)
	}

	security := []PolicyRequirement{{PolicyID: "p", Key: "security_review", Kind: RequirementKindReviewApproval, Purpose: "security"}}
	evaluation, err = EvaluateGate(EnforcementPointApproveReview, security, GateEvidence{})
	if err != nil {
		t.Fatalf("EvaluateGate() error = %v", err)
	}
	if len(evaluation.Unmet) != 1 || evaluation.Unmet[0].Reason != "no review_approval recorded for purpose 'security'" {
		t.Fatalf("Unmet = %#v, want the docs/02 §17.8 worked-example wording", evaluation.Unmet)
	}
}

func TestEvaluateGateSatisfiesWhenEvidencePresent(t *testing.T) {
	requirements := []PolicyRequirement{
		{PolicyID: "p", Key: "ac", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		{PolicyID: "p", Key: "impl", Kind: RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
		{PolicyID: "p", Key: "sec", Kind: RequirementKindReviewApproval, Purpose: "security"},
	}
	evidence := GateEvidence{
		AcceptanceCriteriaBlank: false,
		AttemptEvidenceKeys:     map[string]bool{"implementation": true},
		ReviewApprovalPurposes:  map[string]bool{"security": true},
	}
	evaluation, err := EvaluateGate(EnforcementPointCompleteWorkToDone, requirements, evidence)
	if err != nil {
		t.Fatalf("EvaluateGate() error = %v", err)
	}
	if !evaluation.Passed() {
		t.Fatalf("Passed() = false, Unmet = %#v", evaluation.Unmet)
	}
	if len(evaluation.Satisfied) != 3 {
		t.Fatalf("Satisfied = %#v, want all 3 requirements", evaluation.Satisfied)
	}
}

func TestEvaluateGateRejectsInvalidEnforcementPointAndUnknownKind(t *testing.T) {
	if _, err := EvaluateGate(EnforcementPoint("bogus"), nil, GateEvidence{}); err == nil {
		t.Fatal("EvaluateGate() with an invalid point: want an error, got nil")
	}
	requirements := []PolicyRequirement{{PolicyID: "p", Key: "k", Kind: RequirementKind("bogus")}}
	if _, err := EvaluateGate(EnforcementPointClaimWork, requirements, GateEvidence{}); err == nil {
		t.Fatal("EvaluateGate() with an unrecognized requirement kind: want an error (never silently ignored), got nil")
	}
}

func TestEvaluateGateNeverMutatesCallerOwnedRequirements(t *testing.T) {
	requirements := []PolicyRequirement{
		{PolicyID: "p", Key: "ac", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	}
	original := append([]PolicyRequirement(nil), requirements...)
	evaluation, err := EvaluateGate(EnforcementPointClaimWork, requirements, GateEvidence{AcceptanceCriteriaBlank: false})
	if err != nil {
		t.Fatalf("EvaluateGate() error = %v", err)
	}
	evaluation.Satisfied[0].Key = "mutated"
	if requirements[0] != original[0] {
		t.Fatal("mutating the returned Satisfied slice mutated the caller's input requirements")
	}
}

func TestNewGateSnapshotFingerprintIsStableAndDiscriminating(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	requirements := []PolicyRequirement{
		{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "ac", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	}
	sources := []SourcePolicyRef{{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Version: 1}}

	first, err := NewGateSnapshot(requirements, sources, 3, now)
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	if first.Fingerprint == "" {
		t.Fatal("Fingerprint is empty")
	}

	// Equivalent input (different CreatedAt, same requirements/sources/issue
	// version) must produce the identical fingerprint: CreatedAt is metadata,
	// not part of the semantic snapshot content.
	later := now.Add(time.Hour)
	second, err := NewGateSnapshot(requirements, sources, 3, later)
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	if second.Fingerprint != first.Fingerprint {
		t.Fatalf("fingerprint changed across equivalent inputs: %q vs %q", first.Fingerprint, second.Fingerprint)
	}

	// Source policy order must not matter: sorted internally by PolicyID.
	reorderedSources := []SourcePolicyRef{{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Version: 1}}
	third, err := NewGateSnapshot(requirements, reorderedSources, 3, now)
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	if third.Fingerprint != first.Fingerprint {
		t.Fatalf("fingerprint differs for a reordered-but-equivalent source list: %q vs %q", first.Fingerprint, third.Fingerprint)
	}

	// A semantic change (different issue version) must change the fingerprint.
	differentIssueVersion, err := NewGateSnapshot(requirements, sources, 4, now)
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	if differentIssueVersion.Fingerprint == first.Fingerprint {
		t.Fatal("fingerprint did not change for a different issue version")
	}

	// A semantic change (different source policy version) must change the fingerprint.
	differentSourceVersion, err := NewGateSnapshot(requirements, []SourcePolicyRef{{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Version: 2}}, 3, now)
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	if differentSourceVersion.Fingerprint == first.Fingerprint {
		t.Fatal("fingerprint did not change for a different source policy version")
	}

	// A semantic change (different requirement content) must change the fingerprint.
	differentRequirements := []PolicyRequirement{
		{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "ac", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "extra", Kind: RequirementKindReviewApproval, Purpose: "security"},
	}
	fourth, err := NewGateSnapshot(differentRequirements, sources, 3, now)
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	if fourth.Fingerprint == first.Fingerprint {
		t.Fatal("fingerprint did not change for a different requirement set")
	}
}

func TestNewGateSnapshotNeverMutatesCallerOwnedSlices(t *testing.T) {
	requirements := []PolicyRequirement{{PolicyID: "p", Key: "ac", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"}}
	sources := []SourcePolicyRef{{PolicyID: "p", Version: 1}}
	snapshot, err := NewGateSnapshot(requirements, sources, 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	snapshot.Requirements[0].Key = "mutated"
	snapshot.SourcePolicies[0].Version = 999
	if requirements[0].Key != "ac" || sources[0].Version != 1 {
		t.Fatal("mutating the returned snapshot mutated the caller's input slices")
	}
}

func TestCloneGateSnapshotAndCloneGateEvaluationDetachSlices(t *testing.T) {
	snapshot := GateSnapshot{
		Requirements:   []PolicyRequirement{{PolicyID: "p", Key: "ac"}},
		SourcePolicies: []SourcePolicyRef{{PolicyID: "p", Version: 1}},
	}
	cloned := CloneGateSnapshot(snapshot)
	cloned.Requirements[0].Key = "mutated"
	if snapshot.Requirements[0].Key != "ac" {
		t.Fatal("CloneGateSnapshot did not detach Requirements")
	}

	evaluation := GateEvaluation{
		Satisfied: []PolicyRequirement{{PolicyID: "p", Key: "ac"}},
		Unmet:     []UnmetRequirement{{PolicyID: "p", RequirementKey: "sec"}},
	}
	clonedEval := CloneGateEvaluation(evaluation)
	clonedEval.Satisfied[0].Key = "mutated"
	if evaluation.Satisfied[0].Key != "ac" {
		t.Fatal("CloneGateEvaluation did not detach Satisfied")
	}
}
