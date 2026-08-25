package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestLoadGateDiagnosticWithLivePolicy(t *testing.T) {
	issueService, db, now := openIssueService(t)
	repository, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository() error = %v", err)
	}
	ctx := context.Background()

	// Create a policy.
	policy, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
		ID:        workflowPolicyTestID,
		Input:     validPolicyInput(),
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}

	// Create an issue using the issue service.
	issueResult, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:                domain.TypeTask,
		Title:               "Test Issue",
		AcceptanceCriteria:  stringPtr("Some acceptance criteria"),
		CreateMissingLabels: false,
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	// Load the gate diagnostic without a snapshot (use live policies).
	diagnostic, err := repository.LoadGateDiagnostic(ctx, ports.GateDiagnosticCommand{
		Identifier: internalIssueIdentifier(issueResult.ID),
		Point:      domain.EnforcementPointClaimWork,
	})
	if err != nil {
		t.Fatalf("LoadGateDiagnostic() error = %v", err)
	}

	if diagnostic.SnapshotFound {
		t.Fatalf("SnapshotFound = true, want false (should use live policies)")
	}
	if len(diagnostic.Requirements) == 0 {
		t.Fatalf("Requirements are empty, want requirements from live policy")
	}
	if len(diagnostic.SourcePolicies) == 0 {
		t.Fatalf("SourcePolicies are empty, want at least one source policy")
	}
	if diagnostic.SourcePolicies[0].PolicyID != policy.ID {
		t.Fatalf("SourcePolicies[0].PolicyID = %q, want %q", diagnostic.SourcePolicies[0].PolicyID, policy.ID)
	}
}

func TestLoadGateDiagnosticWithMissingSnapshotFallsBack(t *testing.T) {
	issueService, db, now := openIssueService(t)
	repository, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository() error = %v", err)
	}
	ctx := context.Background()

	// Create a policy.
	_, _ = repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
		ID:        workflowPolicyTestID,
		Input:     validPolicyInput(),
		CreatedAt: now,
	})

	// Create an issue using the issue service.
	issueResult, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:                domain.TypeTask,
		Title:               "Test Issue",
		AcceptanceCriteria:  stringPtr("Some acceptance criteria"),
		CreateMissingLabels: false,
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	// Try to load a diagnostic with a non-existent attempt ID
	// (no snapshot will be found, so it falls back to live policies).
	attemptID := "nonexistent-attempt"
	diagnostic, err := repository.LoadGateDiagnostic(ctx, ports.GateDiagnosticCommand{
		Identifier: internalIssueIdentifier(issueResult.ID),
		Point:      domain.EnforcementPointClaimWork,
		AttemptID:  &attemptID,
	})
	if err != nil {
		t.Fatalf("LoadGateDiagnostic() error = %v", err)
	}

	if diagnostic.SnapshotFound {
		t.Fatalf("SnapshotFound = true, want false (snapshot doesn't exist)")
	}
	// Should have fallen back to live policy.
	if len(diagnostic.Requirements) == 0 {
		t.Fatalf("Requirements are empty; expected fallback to live policy")
	}
	if len(diagnostic.SourcePolicies) == 0 {
		t.Fatalf("SourcePolicies are empty; expected fallback to live policy")
	}
}

func TestLoadGateDiagnosticRejectsNonexistentIssue(t *testing.T) {
	repository, _, _ := openWorkflowPolicyRepository(t)
	ctx := context.Background()

	// Try to load a diagnostic for a non-existent issue.
	_, err := repository.LoadGateDiagnostic(ctx, ports.GateDiagnosticCommand{
		Identifier: internalIssueIdentifier("nonexistent-issue"),
		Point:      domain.EnforcementPointClaimWork,
	})

	if err == nil {
		t.Fatalf("LoadGateDiagnostic() succeeded, want an error for non-existent issue")
	}

	// Check for the expected error code.
	if !errorsIsVersionConflict(err) {
		// The error should be a domain error with CodeIssueNotFound
		var domainErr *domain.Error
		if err != nil {
			domainErr, _ = err.(*domain.Error)
			if domainErr == nil || domainErr.Code != domain.CodeIssueNotFound {
				t.Fatalf("LoadGateDiagnostic() error = %v, want CodeIssueNotFound", err)
			}
		}
	}
}

// TestLoadGateDiagnosticPrefersFrozenSnapshotOverLivePolicies is the docs/02
// §17.6 guarantee, and the reason the diagnostic exists at all: an attempt is
// judged against the requirements frozen when it was claimed, never against
// whatever policies happen to be active now. The policy is archived after the
// claim, so live matching would yield zero requirements — if the diagnostic
// ever fell back to live policies here it would report a pass where the
// mutation path reports a failure, which is exactly the drift acceptance
// criterion 3 forbids.
func TestLoadGateDiagnosticPrefersFrozenSnapshotOverLivePolicies(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-diagnostic-snapshot")
	defer fixture.close()

	policy := createGateEvidencePolicy(t, fixture, []domain.PolicyRequirementInput{
		{Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	issueID, claimed := claimReadyIssueForEvidence(t, fixture, "snapshot beats live policy")

	repository, err := sqlite.NewWorkflowPolicyRepository(fixture.db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository() error = %v", err)
	}

	// Archive the policy the snapshot was frozen from. Live matching now
	// returns nothing; the snapshot must be unaffected.
	if _, err := repository.ArchivePolicy(fixture.ctx, ports.ArchiveWorkflowPolicyCommand{
		PolicyID: policy.ID, ExpectedVersion: policy.Version, ArchivedAt: fixture.clock.Now(),
	}); err != nil {
		t.Fatalf("ArchivePolicy() error = %v", err)
	}

	live, err := repository.LoadGateDiagnostic(fixture.ctx, ports.GateDiagnosticCommand{
		Identifier: internalIssueIdentifier(issueID), Point: domain.EnforcementPointCompleteWorkToDone,
	})
	if err != nil {
		t.Fatalf("LoadGateDiagnostic(live) error = %v", err)
	}
	if len(live.Requirements) != 0 {
		t.Fatalf("live requirements = %#v, want none after archiving the only policy", live.Requirements)
	}

	frozen, err := repository.LoadGateDiagnostic(fixture.ctx, ports.GateDiagnosticCommand{
		Identifier: internalIssueIdentifier(issueID), Point: domain.EnforcementPointCompleteWorkToDone, AttemptID: &claimed.Attempt.ID,
	})
	if err != nil {
		t.Fatalf("LoadGateDiagnostic(snapshot) error = %v", err)
	}
	if !frozen.SnapshotFound {
		t.Fatal("SnapshotFound = false, want true: the claim froze a snapshot for this attempt")
	}
	if len(frozen.Requirements) != 1 || frozen.Requirements[0].Key != "impl" {
		t.Fatalf("frozen requirements = %#v, want the single \"impl\" requirement frozen at claim time", frozen.Requirements)
	}
}

// internalIssueIdentifier builds the ULID-form identifier these tests use when
// the identifier form is not what is under test.
func internalIssueIdentifier(value string) domain.IssueIdentifier {
	return domain.IssueIdentifier{Kind: domain.IssueIdentifierInternalID, Value: value}
}

// TestLoadGateDiagnosticResolvesDisplayIdentifier is the ISSUE-174 review's
// falsifying case: evaluate_gates advertises "ULID or ISSUE-N", but the
// diagnostic queried `WHERE id = ?` with whatever string arrived, so an
// ISSUE-N returned ISSUE_NOT_FOUND while the ULID for the same issue
// succeeded. Both forms must now produce an identical diagnostic.
func TestLoadGateDiagnosticResolvesDisplayIdentifier(t *testing.T) {
	issueService, db, now := openIssueService(t)
	repository, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository() error = %v", err)
	}
	ctx := context.Background()

	if _, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
		ID:        workflowPolicyTestID,
		Input:     validPolicyInput(),
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	issueResult, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:               domain.TypeTask,
		Title:              "Display identifier issue",
		AcceptanceCriteria: stringPtr("Some acceptance criteria"),
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	displayIdentifier, err := domain.ParseIssueIdentifier(issueResult.DisplayID)
	if err != nil {
		t.Fatalf("ParseIssueIdentifier(%q) error = %v", issueResult.DisplayID, err)
	}
	byDisplayID, err := repository.LoadGateDiagnostic(ctx, ports.GateDiagnosticCommand{
		Identifier: displayIdentifier,
		Point:      domain.EnforcementPointClaimWork,
	})
	if err != nil {
		t.Fatalf("LoadGateDiagnostic(%s) error = %v, want the same result as the ULID form", issueResult.DisplayID, err)
	}
	byInternalID, err := repository.LoadGateDiagnostic(ctx, ports.GateDiagnosticCommand{
		Identifier: internalIssueIdentifier(issueResult.ID),
		Point:      domain.EnforcementPointClaimWork,
	})
	if err != nil {
		t.Fatalf("LoadGateDiagnostic(ULID) error = %v", err)
	}
	if !reflect.DeepEqual(byDisplayID, byInternalID) {
		t.Fatalf("diagnostic by display ID = %#v, want the same as by internal ID %#v", byDisplayID, byInternalID)
	}
	if len(byDisplayID.Requirements) == 0 {
		t.Fatal("requirements are empty; the fixture policy should have matched, so this test would pass vacuously")
	}
}

// TestLoadGateDiagnosticRejectsUnknownDisplayIdentifier pins that resolving
// both forms did not turn a well-formed identifier that matches nothing into
// anything other than ISSUE_NOT_FOUND.
func TestLoadGateDiagnosticRejectsUnknownDisplayIdentifier(t *testing.T) {
	repository, _, _ := openWorkflowPolicyRepository(t)

	unknown, err := domain.ParseIssueIdentifier("ISSUE-999999")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.LoadGateDiagnostic(context.Background(), ports.GateDiagnosticCommand{
		Identifier: unknown,
		Point:      domain.EnforcementPointClaimWork,
	})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeIssueNotFound {
		t.Fatalf("error = %v, want CodeIssueNotFound", err)
	}
}
