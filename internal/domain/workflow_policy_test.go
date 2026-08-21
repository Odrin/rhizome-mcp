package domain

import (
	"errors"
	"testing"
)

func TestPolicyStatusValid(t *testing.T) {
	tests := []struct {
		status PolicyStatus
		want   bool
	}{
		{PolicyStatusActive, true},
		{PolicyStatusArchived, true},
		{PolicyStatus("deleted"), false},
		{PolicyStatus(""), false},
	}
	for _, test := range tests {
		if got := test.status.Valid(); got != test.want {
			t.Errorf("PolicyStatus(%q).Valid() = %v, want %v", test.status, got, test.want)
		}
	}
}

func TestCloneWorkflowPolicyDetachesSlices(t *testing.T) {
	policy := WorkflowPolicy{
		ID:       "01AAAAAAAAAAAAAAAAAAAAAAAA",
		Selector: PolicySelector{IssueTypes: []Type{TypeTask}, LabelsAll: []string{"database"}},
		Requirements: []PolicyRequirement{
			{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "ac", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		},
	}
	cloned := CloneWorkflowPolicy(policy)
	cloned.Selector.IssueTypes[0] = TypeBug
	cloned.Selector.LabelsAll[0] = "mutated"
	cloned.Requirements[0].Key = "mutated"
	if policy.Selector.IssueTypes[0] != TypeTask || policy.Selector.LabelsAll[0] != "database" || policy.Requirements[0].Key != "ac" {
		t.Fatal("CloneWorkflowPolicy did not detach its slices from the original")
	}
}

func TestPolicySelectorInputValidateRejectsEpicAndDuplicates(t *testing.T) {
	tests := []struct {
		name    string
		input   PolicySelectorInput
		wantErr string
	}{
		{name: "empty selector is valid", input: PolicySelectorInput{}},
		{name: "task and bug are valid", input: PolicySelectorInput{IssueTypes: []Type{TypeTask, TypeBug}}},
		{name: "epic is rejected", input: PolicySelectorInput{IssueTypes: []Type{TypeEpic}}, wantErr: "UNSUPPORTED_TYPE"},
		{name: "invalid type is rejected", input: PolicySelectorInput{IssueTypes: []Type{Type("story")}}, wantErr: "UNSUPPORTED_TYPE"},
		{name: "duplicate type is rejected", input: PolicySelectorInput{IssueTypes: []Type{TypeTask, TypeTask}}, wantErr: "DUPLICATE"},
		{name: "duplicate label (case-insensitive) is rejected", input: PolicySelectorInput{LabelsAll: []string{"Database", "database"}}, wantErr: "DUPLICATE"},
		{name: "too many labels is rejected", input: PolicySelectorInput{LabelsAll: manyLabels(MaxPolicyLabelsAll + 1)}, wantErr: "MAX_ITEMS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.input.Validate()
			assertDetailCode(t, err, test.wantErr)
		})
	}
}

func manyLabels(count int) []string {
	labels := make([]string, count)
	for i := range labels {
		labels[i] = "label-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	return labels
}

func TestPolicySelectorMatches(t *testing.T) {
	selector := PolicySelector{IssueTypes: []Type{TypeTask}, LabelsAll: []string{"Database", "Security"}}
	tests := []struct {
		name   string
		typ    Type
		labels []string
		want   bool
	}{
		{name: "matching type and labels", typ: TypeTask, labels: []string{"database", "SECURITY", "extra"}, want: true},
		{name: "wrong type", typ: TypeBug, labels: []string{"database", "security"}, want: false},
		{name: "missing one label", typ: TypeTask, labels: []string{"database"}, want: false},
		{name: "no labels at all", typ: TypeTask, labels: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := selector.Matches(test.typ, test.labels); got != test.want {
				t.Errorf("Matches() = %v, want %v", got, test.want)
			}
		})
	}

	empty := PolicySelector{}
	if !empty.Matches(TypeBug, nil) {
		t.Error("empty selector must match every executable type with no labels")
	}
}

func TestWorkflowPolicyInputValidateRequirementKinds(t *testing.T) {
	tests := []struct {
		name    string
		input   PolicyRequirementInput
		wantErr string
	}{
		{name: "valid issue_field_nonblank", input: PolicyRequirementInput{Key: "acceptance_criteria", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"}},
		{name: "issue_field_nonblank rejects unsupported field", input: PolicyRequirementInput{Key: "k", Kind: RequirementKindIssueFieldNonblank, Field: "title"}, wantErr: "UNSUPPORTED_FIELD"},
		{name: "issue_field_nonblank rejects cross-kind evidence_key", input: PolicyRequirementInput{Key: "k", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria", EvidenceKey: "x"}, wantErr: "UNEXPECTED_FIELD"},
		{name: "valid attempt_evidence", input: PolicyRequirementInput{Key: "implementation_evidence", Kind: RequirementKindAttemptEvidence, EvidenceKey: "implementation"}},
		{name: "attempt_evidence requires evidence_key", input: PolicyRequirementInput{Key: "k", Kind: RequirementKindAttemptEvidence}, wantErr: "REQUIRED"},
		{name: "attempt_evidence rejects cross-kind purpose", input: PolicyRequirementInput{Key: "k", Kind: RequirementKindAttemptEvidence, EvidenceKey: "x", Purpose: "security"}, wantErr: "UNEXPECTED_FIELD"},
		{name: "valid review_approval", input: PolicyRequirementInput{Key: "security_review", Kind: RequirementKindReviewApproval, Purpose: "security"}},
		{name: "review_approval requires purpose", input: PolicyRequirementInput{Key: "k", Kind: RequirementKindReviewApproval}, wantErr: "REQUIRED"},
		{name: "invalid kind is rejected", input: PolicyRequirementInput{Key: "k", Kind: RequirementKind("bogus")}, wantErr: "INVALID_ENUM"},
		{name: "blank key is rejected", input: PolicyRequirementInput{Key: "  ", Kind: RequirementKindReviewApproval, Purpose: "security"}, wantErr: "REQUIRED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := WorkflowPolicyInput{Requirements: []PolicyRequirementInput{test.input}}
			_, err := policy.Validate()
			assertDetailCode(t, err, test.wantErr)
		})
	}
}

func TestWorkflowPolicyInputValidateLimitsAndDuplicateKeys(t *testing.T) {
	tooMany := make([]PolicyRequirementInput, MaxPolicyRequirements+1)
	for i := range tooMany {
		tooMany[i] = PolicyRequirementInput{Key: "k" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Kind: RequirementKindReviewApproval, Purpose: "security"}
	}
	_, err := WorkflowPolicyInput{Requirements: tooMany}.Validate()
	assertDetailCode(t, err, "MAX_ITEMS")

	duplicateKeys := []PolicyRequirementInput{
		{Key: "same", Kind: RequirementKindReviewApproval, Purpose: "security"},
		{Key: "same", Kind: RequirementKindAttemptEvidence, EvidenceKey: "tests"},
	}
	_, err = WorkflowPolicyInput{Requirements: duplicateKeys}.Validate()
	assertDetailCode(t, err, "DUPLICATE_KEY")

	longKey := ""
	for i := 0; i < MaxPolicyKeyRunes+1; i++ {
		longKey += "a"
	}
	_, err = WorkflowPolicyInput{Requirements: []PolicyRequirementInput{{Key: longKey, Kind: RequirementKindReviewApproval, Purpose: "security"}}}.Validate()
	assertDetailCode(t, err, "MAX_RUNES")
}

func TestWorkflowPolicyInputValidateNormalizesSuccessfully(t *testing.T) {
	input := WorkflowPolicyInput{
		Selector: PolicySelectorInput{IssueTypes: []Type{TypeTask}, LabelsAll: []string{"  Database  "}},
		Requirements: []PolicyRequirementInput{
			{Key: "  acceptance_criteria  ", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		},
	}
	validated, err := input.Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(validated.Selector.LabelsAll) != 1 || validated.Selector.LabelsAll[0] != "Database" {
		t.Fatalf("selector labels = %#v, want trimmed 'Database'", validated.Selector.LabelsAll)
	}
	if len(validated.Requirements) != 1 || validated.Requirements[0].Key != "acceptance_criteria" {
		t.Fatalf("requirements = %#v, want trimmed key", validated.Requirements)
	}
}

func TestMatchWorkflowPoliciesComposesDeterministicallyRegardlessOfInputOrder(t *testing.T) {
	policyA := WorkflowPolicy{
		ID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Status: PolicyStatusActive,
		Selector: PolicySelector{IssueTypes: []Type{TypeTask}},
		Requirements: []PolicyRequirement{
			{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "z_key", Kind: RequirementKindReviewApproval, Purpose: "security"},
			{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "a_key", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		},
	}
	policyB := WorkflowPolicy{
		ID: "01BBBBBBBBBBBBBBBBBBBBBBBB", Status: PolicyStatusActive,
		Selector: PolicySelector{},
		Requirements: []PolicyRequirement{
			{PolicyID: "01BBBBBBBBBBBBBBBBBBBBBBBB", Key: "m_key", Kind: RequirementKindAttemptEvidence, EvidenceKey: "tests"},
		},
	}
	archived := WorkflowPolicy{
		ID: "01CCCCCCCCCCCCCCCCCCCCCCCC", Status: PolicyStatusArchived,
		Selector: PolicySelector{},
		Requirements: []PolicyRequirement{
			{PolicyID: "01CCCCCCCCCCCCCCCCCCCCCCCC", Key: "n_key", Kind: RequirementKindAttemptEvidence, EvidenceKey: "manual_qa"},
		},
	}
	nonMatching := WorkflowPolicy{
		ID: "01DDDDDDDDDDDDDDDDDDDDDDDD", Status: PolicyStatusActive,
		Selector: PolicySelector{IssueTypes: []Type{TypeBug}},
		Requirements: []PolicyRequirement{
			{PolicyID: "01DDDDDDDDDDDDDDDDDDDDDDDD", Key: "o_key", Kind: RequirementKindAttemptEvidence, EvidenceKey: "irrelevant"},
		},
	}

	forward := MatchWorkflowPolicies([]WorkflowPolicy{policyA, policyB, archived, nonMatching}, TypeTask, nil)
	backward := MatchWorkflowPolicies([]WorkflowPolicy{nonMatching, archived, policyB, policyA}, TypeTask, nil)

	want := []PolicyRequirement{
		{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "a_key", Kind: RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "z_key", Kind: RequirementKindReviewApproval, Purpose: "security"},
		{PolicyID: "01BBBBBBBBBBBBBBBBBBBBBBBB", Key: "m_key", Kind: RequirementKindAttemptEvidence, EvidenceKey: "tests"},
	}
	assertRequirementsEqual(t, forward, want)
	assertRequirementsEqual(t, backward, want)
}

func TestMatchWorkflowPoliciesDedupesOnlyIdenticalPolicyIDAndKey(t *testing.T) {
	policy := WorkflowPolicy{
		ID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Status: PolicyStatusActive,
		Selector: PolicySelector{},
		Requirements: []PolicyRequirement{
			{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "dup", Kind: RequirementKindReviewApproval, Purpose: "security"},
			{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "dup", Kind: RequirementKindReviewApproval, Purpose: "security"},
		},
	}
	otherPolicySameKey := WorkflowPolicy{
		ID: "01BBBBBBBBBBBBBBBBBBBBBBBB", Status: PolicyStatusActive,
		Selector: PolicySelector{},
		Requirements: []PolicyRequirement{
			{PolicyID: "01BBBBBBBBBBBBBBBBBBBBBBBB", Key: "dup", Kind: RequirementKindReviewApproval, Purpose: "security"},
		},
	}
	got := MatchWorkflowPolicies([]WorkflowPolicy{policy, otherPolicySameKey}, TypeTask, nil)
	if len(got) != 2 {
		t.Fatalf("got %d requirements, want 2 (same-policy duplicate collapsed, cross-policy same key kept independent): %#v", len(got), got)
	}
}

func TestMatchWorkflowPoliciesNeverMatchesEpic(t *testing.T) {
	policy := WorkflowPolicy{
		ID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Status: PolicyStatusActive,
		Selector: PolicySelector{}, // empty selector matches every executable type
		Requirements: []PolicyRequirement{
			{PolicyID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Key: "k", Kind: RequirementKindReviewApproval, Purpose: "security"},
		},
	}
	got := MatchWorkflowPolicies([]WorkflowPolicy{policy}, TypeEpic, nil)
	if len(got) != 0 {
		t.Fatalf("epic matched %d requirements, want 0: %#v", len(got), got)
	}
}

func assertRequirementsEqual(t *testing.T, got, want []PolicyRequirement) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d requirements, want %d: got=%#v want=%#v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("requirement[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func assertDetailCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if wantCode == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want a *domain.Error with detail code %q", err, wantCode)
	}
	for _, detail := range domainErr.Details {
		if detail.Code == wantCode {
			return
		}
	}
	t.Fatalf("error details = %#v, want a detail with code %q", domainErr.Details, wantCode)
}
