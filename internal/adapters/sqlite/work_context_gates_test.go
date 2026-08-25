package sqlite_test

import (
	"strings"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// loadWorkContextGates reads one issue's default work context and returns just
// its always-present gate summary.
func loadWorkContextGates(t *testing.T, fixture *attemptTestFixture, issueID string) domain.WorkContextGateSummary {
	t.Helper()
	repository, err := sqlite.NewWorkContextRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	context, err := repository.GetWorkContext(fixture.ctx, ports.GetWorkContextCommand{
		Input: domain.GetWorkContextInput{IssueID: issueID},
		Now:   fixture.clock.Now(),
	})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	return context.Gates
}

// TestWorkContextGatesWithNoPoliciesIsEmptyAndPassing pins the no-policy
// compatibility case: a project that never configured a gate still gets the
// summary, reporting nothing required rather than nothing known.
func TestWorkContextGatesWithNoPoliciesIsEmptyAndPassing(t *testing.T) {
	fixture := newAttemptTestFixture(t, "wc-gates-none")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "no policies", domain.StatusReady)

	gates := loadWorkContextGates(t, fixture, issue.ID)
	if gates.Point != domain.EnforcementPointClaimWork {
		t.Fatalf("point = %q, want claim_work for an unclaimed issue", gates.Point)
	}
	if gates.RequirementCount != 0 || len(gates.Unmet) != 0 {
		t.Fatalf("gates = %+v, want no requirements and nothing unmet", gates)
	}
	if gates.SnapshotFingerprint != nil {
		t.Fatalf("snapshot fingerprint = %v, want nil with no attempt", *gates.SnapshotFingerprint)
	}
}

// TestWorkContextGatesReportsUnmetLivePolicyForUnclaimedIssue is AC1's core
// case: the agent learns which requirement key is missing, and what to do
// about it, from the default response alone.
func TestWorkContextGatesReportsUnmetLivePolicyForUnclaimedIssue(t *testing.T) {
	fixture := newAttemptTestFixture(t, "wc-gates-live")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	})
	issue := createAttemptIssue(t, fixture, "blank ac", domain.StatusReady)

	gates := loadWorkContextGates(t, fixture, issue.ID)
	if gates.Point != domain.EnforcementPointClaimWork {
		t.Fatalf("point = %q, want claim_work", gates.Point)
	}
	if gates.RequirementCount != 1 || gates.SatisfiedCount != 0 {
		t.Fatalf("counts = %d required / %d satisfied, want 1/0", gates.RequirementCount, gates.SatisfiedCount)
	}
	if len(gates.Unmet) != 1 || gates.Unmet[0].RequirementKey != "ac" {
		t.Fatalf("unmet = %+v, want exactly the 'ac' requirement", gates.Unmet)
	}
	if gates.Unmet[0].Reason == "" || gates.Unmet[0].PolicyID == "" {
		t.Fatalf("unmet entry = %+v, want a policy id and a reason", gates.Unmet[0])
	}
	if len(gates.NextActions) != 1 || !strings.Contains(gates.NextActions[0], "acceptance_criteria") {
		t.Fatalf("next actions = %v, want one naming the blank field", gates.NextActions)
	}
}

// TestWorkContextGatesUsesActiveAttemptSnapshot proves the enforcement point
// follows the issue's state: once claimed, the summary reports what finishing
// requires, sourced from the attempt's frozen snapshot rather than live
// policies -- and clears as soon as the evidence is submitted.
func TestWorkContextGatesUsesActiveAttemptSnapshot(t *testing.T) {
	fixture := newAttemptTestFixture(t, "wc-gates-snapshot")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	issue := createAttemptIssue(t, fixture, "needs evidence", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	gates := loadWorkContextGates(t, fixture, issue.ID)
	if gates.Point != domain.EnforcementPointCompleteWorkToDone {
		t.Fatalf("point = %q, want complete_work_to_done while an attempt is active", gates.Point)
	}
	if gates.SnapshotFingerprint == nil || *gates.SnapshotFingerprint == "" {
		t.Fatal("snapshot fingerprint = nil, want the frozen snapshot's fingerprint")
	}
	if len(gates.Unmet) != 1 || gates.Unmet[0].RequirementKey != "impl" {
		t.Fatalf("unmet = %+v, want the 'impl' evidence requirement", gates.Unmet)
	}
	if len(gates.NextActions) != 1 || !strings.Contains(gates.NextActions[0], "submit_gate_evidence") {
		t.Fatalf("next actions = %v, want one naming submit_gate_evidence", gates.NextActions)
	}

	if _, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "implemented",
	}); err != nil {
		t.Fatalf("SubmitGateEvidence() error = %v", err)
	}

	cleared := loadWorkContextGates(t, fixture, issue.ID)
	if len(cleared.Unmet) != 0 || cleared.SatisfiedCount != 1 {
		t.Fatalf("gates after evidence = %+v, want nothing unmet and one satisfied", cleared)
	}
	if len(cleared.NextActions) != 0 {
		t.Fatalf("next actions after evidence = %v, want none", cleared.NextActions)
	}
}

// TestWorkContextGatesMatchEvaluateGatesDiagnostic pins that the context
// summary and the evaluate_gates diagnostic agree on the same issue: two
// surfaces reporting different requirement sets would be worse than one.
func TestWorkContextGatesMatchEvaluateGatesDiagnostic(t *testing.T) {
	fixture := newAttemptTestFixture(t, "wc-gates-agree")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	})
	issue := createAttemptIssue(t, fixture, "blank ac", domain.StatusReady)

	gates := loadWorkContextGates(t, fixture, issue.ID)

	policyRepository, err := sqlite.NewWorkflowPolicyRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := policyRepository.LoadGateDiagnostic(fixture.ctx, ports.GateDiagnosticCommand{
		Identifier: internalIssueIdentifier(issue.ID),
		Point:      domain.EnforcementPointClaimWork,
	})
	if err != nil {
		t.Fatalf("LoadGateDiagnostic() error = %v", err)
	}
	evaluation, err := domain.EvaluateGate(domain.EnforcementPointClaimWork, diagnostic.Requirements, diagnostic.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if int(gates.RequirementCount) != len(diagnostic.Requirements) {
		t.Fatalf("context requirement count = %d, diagnostic = %d", gates.RequirementCount, len(diagnostic.Requirements))
	}
	if len(gates.Unmet) != len(evaluation.Unmet) {
		t.Fatalf("context unmet = %d, diagnostic unmet = %d", len(gates.Unmet), len(evaluation.Unmet))
	}
	for index, unmet := range evaluation.Unmet {
		if gates.Unmet[index].RequirementKey != unmet.RequirementKey || gates.Unmet[index].Reason != unmet.Reason {
			t.Fatalf("context unmet[%d] = %+v, diagnostic = %+v", index, gates.Unmet[index], unmet)
		}
	}
}
