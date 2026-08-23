package sqlite_test

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// TestGateEnforcementNoStatusWriteOutsideTheChokePoint is ISSUE-172's
// requested regression tripwire: "a repo-wide test fails on any SET status
// outside the choke point." It scans this package's own (non-test) source
// for every site that can write issues.status and asserts the set of files
// matches exactly what ISSUE-172 wired -- so a new site, wherever it lands,
// forces a conscious update to this test and a look at whether it is gated.
//
// Every current site and what covers it:
//   - attempts.go   FinishAttempt:   enforcementPointForFinish + evaluateGateAgainstAttemptSnapshot
//   - issues.go     CreateIssue:     enforcementPointForCreateStatus + evaluateGateAgainstLivePolicies
//   - issues.go     UpdateIssue:     never sets status to review/done -- domain.ApplyIssuePatch
//     (internal/domain/issue_patch.go) rejects that patch unconditionally before this file runs
//   - planning.go   applyPlan:       enforcementPointForCreateStatus + evaluateGateAgainstLivePolicies, per entry
//   - projects.go   ApplyLogicalProjectImport: exempt by design (docs/02 §17.1, ISSUE-201),
//     but runs domain.CreateIssueInput{}.Validate() on every imported issue
//   - unit_of_work.go ConditionalUpdateIssue: a generic, caller-parameterized helper
//     (ports.UnitOfWork) with a dynamic SET clause. It has zero callers today (see
//     TestConditionalUpdateIssueHasNoUngatedCallers below) -- it is listed here only
//     because its own literal SQL text contains "UPDATE issues SET", not because it
//     itself writes status.
func TestGateEnforcementNoStatusWriteOutsideTheChokePoint(t *testing.T) {
	wantFiles := map[string]bool{
		"attempts.go": true, "issues.go": true, "planning.go": true, "projects.go": true, "unit_of_work.go": true,
	}
	gotFiles := scanPackageSourceFiles(t, func(text string) bool {
		return strings.Contains(text, "INSERT INTO issues(") || strings.Contains(text, "UPDATE issues SET")
	})
	if !reflect.DeepEqual(gotFiles, wantFiles) {
		t.Fatalf("files writing to the issues table = %v, want exactly %v -- a new site must be gated via gate_enforcement.go (or explicitly exempted with a docs/02 §17.1 citation) and added to this test's allowlist", gotFiles, wantFiles)
	}
}

// TestConditionalUpdateIssueHasNoUngatedCallers guards the one generic,
// caller-parameterized loophole in the scan above: ports.UnitOfWork's
// ConditionalUpdateIssue takes a caller-supplied SET clause, so it cannot be
// gated by inspecting its own definition. It has zero callers today (built
// ahead of its consumers, per its own doc comment). The day a first caller
// is added, this test fails, forcing a look at whether that caller's SET
// clause touches status and, if so, whether it is gated.
func TestConditionalUpdateIssueHasNoUngatedCallers(t *testing.T) {
	callers := scanPackageSourceFiles(t, func(text string) bool {
		return strings.Contains(text, ".ConditionalUpdateIssue(")
	})
	if len(callers) != 0 {
		t.Fatalf("ConditionalUpdateIssue call sites = %v, want none; if this is a new, intentional caller, confirm its SET clause does not touch status without going through gate_enforcement.go, then update this test", callers)
	}
}

// scanPackageSourceFiles returns the set of this package's own (non-test)
// .go file base names whose full text satisfies match.
func scanPackageSourceFiles(t *testing.T, match func(text string) bool) map[string]bool {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if match(string(content)) {
			found[filepath.Base(path)] = true
		}
	}
	return found
}

func createWorkflowPolicy(t *testing.T, fixture *attemptTestFixture, selector domain.PolicySelectorInput, requirements []domain.PolicyRequirementInput) domain.WorkflowPolicy {
	t.Helper()
	repository, err := sqlite.NewWorkflowPolicyRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := repository.CreatePolicy(fixture.ctx, ports.CreateWorkflowPolicyCommand{
		ID:        fixture.newID(t),
		Input:     domain.WorkflowPolicyInput{Selector: selector, Requirements: requirements},
		CreatedAt: fixture.clock.Now(),
	})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	return policy
}

func allTasksSelector() domain.PolicySelectorInput {
	return domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeTask}}
}

func assertWorkflowGateUnsatisfied(t *testing.T, err error) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeWorkflowGateUnsatisfied {
		t.Fatalf("error = %v, want CodeWorkflowGateUnsatisfied", err)
	}
	if len(domainErr.Details) == 0 {
		t.Fatalf("details = %v, want at least one unmet-requirement detail", domainErr.Details)
	}
}

func TestClaimWorkRejectsUnmetIssueFieldRequirement(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-claim-unmet")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	})
	issue := createAttemptIssue(t, fixture, "blank ac", domain.StatusReady)

	_, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	assertWorkflowGateUnsatisfied(t, err)

	var attemptCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM work_attempts WHERE issue_id = ?`, issue.ID).Scan(&attemptCount)
	}); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 {
		t.Fatalf("work_attempts rows after rejected claim = %d, want 0", attemptCount)
	}
}

func TestClaimWorkSatisfiedFreezesAttemptSnapshot(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-claim-satisfied")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	})
	ac := "clear criteria"
	issue, err := fixture.issues.CreateIssue(fixture.ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "has ac", Status: domain.StatusReady, AcceptanceCriteria: &ac,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	policyRepository, err := sqlite.NewWorkflowPolicyRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := policyRepository.GetAttemptGateSnapshot(fixture.ctx, ports.GetAttemptGateSnapshotCommand{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatalf("GetAttemptGateSnapshot() error = %v", err)
	}
	if len(snapshot.Requirements) != 1 || snapshot.Requirements[0].Key != "ac" || snapshot.IssueVersion != issue.Issue.Version {
		t.Fatalf("snapshot = %+v, want the one matched requirement at issue version %d", snapshot, issue.Issue.Version)
	}
}

// TestCompleteWorkToReviewUsesFrozenSnapshotNotLivePolicy proves ISSUE-172
// AC4/docs/02 §17.6: a policy created after claim does not retroactively
// apply to that attempt's completion, because completion re-evaluates the
// frozen claim-time snapshot, never live policy state.
func TestCompleteWorkToReviewUsesFrozenSnapshotNotLivePolicy(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-finish-frozen")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "no policy at claim", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	// Created only after the claim: must not affect this already-frozen
	// (empty) snapshot.
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	})

	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Outcome: domain.AttemptOutcomeCompleted,
		ResultSummary: "done", TargetIssueStatus: statusPointer(domain.StatusReview),
	}); err != nil {
		t.Fatalf("FinishAttempt() to review error = %v, want success against the frozen (empty) snapshot", err)
	}
}

func TestCompleteWorkToDoneRejectsMissingAttemptEvidenceAndLeavesAttemptActive(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-finish-evidence")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	issue := createAttemptIssue(t, fixture, "needs evidence", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	_, err = fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Outcome: domain.AttemptOutcomeCompleted,
		ResultSummary: "done", TargetIssueStatus: statusPointer(domain.StatusDone),
	})
	assertWorkflowGateUnsatisfied(t, err)

	var attemptStatus string
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&attemptStatus)
	}); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != "active" {
		t.Fatalf("attempt status after rejected finish = %q, want active", attemptStatus)
	}

	// Submitting the missing evidence and retrying succeeds.
	if _, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "implemented",
	}); err != nil {
		t.Fatalf("SubmitGateEvidence() error = %v", err)
	}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Outcome: domain.AttemptOutcomeCompleted,
		ResultSummary: "done", TargetIssueStatus: statusPointer(domain.StatusDone),
	}); err != nil {
		t.Fatalf("FinishAttempt() after evidence error = %v", err)
	}
}

// TestApproveReviewRejectsUnsatisfiableReviewApproval documents ISSUE-172's
// interim behavior for review_approval (see evaluateGateAgainstAttemptSnapshot's
// doc comment): with no approval-recording mechanism until ISSUE-173 lands,
// a policy requiring review_approval can never be satisfied, so it always
// blocks approve_review while active -- fail rather than silently bypass.
func TestApproveReviewRejectsUnsatisfiableReviewApproval(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-approve-review")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "security", Kind: domain.RequirementKindReviewApproval, Purpose: "security"},
	})
	issue := createAttemptIssue(t, fixture, "needs approval", domain.StatusReady)
	workClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}
	// review_approval does not apply at complete_work_to_review (docs/02
	// §17.4), so finishing to review is unaffected by the policy above.
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: workClaim.Attempt.ID, LeaseToken: workClaim.LeaseToken, Outcome: domain.AttemptOutcomeCompleted,
		ResultSummary: "sent for review", TargetIssueStatus: statusPointer(domain.StatusReview),
	}); err != nil {
		t.Fatalf("FinishAttempt() to review error = %v", err)
	}

	reviewClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() for review error = %v", err)
	}
	if reviewClaim.Attempt.Kind != domain.AttemptKindReview {
		t.Fatalf("claimed attempt kind = %q, want review", reviewClaim.Attempt.Kind)
	}
	approved := domain.ReviewOutcomeApproved
	_, err = fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: reviewClaim.Attempt.ID, LeaseToken: reviewClaim.LeaseToken, Outcome: domain.AttemptOutcomeCompleted,
		ResultSummary: "approved", ReviewOutcome: &approved,
	})
	assertWorkflowGateUnsatisfied(t, err)
}

func TestCreateIssueGatesReviewAndDoneButNotCancelled(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-create-issue")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	})

	for _, status := range []domain.Status{domain.StatusReview, domain.StatusDone} {
		t.Run(string(status), func(t *testing.T) {
			_, err := fixture.issues.CreateIssue(fixture.ctx, domain.CreateIssueInput{
				Type: domain.TypeTask, Title: "blocked create " + string(status), Status: status,
			})
			assertWorkflowGateUnsatisfied(t, err)
		})
	}

	// cancelled is never gated (ISSUE-201): it must succeed even though the
	// same blocking policy is active.
	cancelled, err := fixture.issues.CreateIssue(fixture.ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "cancelled create", Status: domain.StatusCancelled,
	})
	if err != nil {
		t.Fatalf("CreateIssue(status=cancelled) error = %v, want success (ungated)", err)
	}
	if cancelled.Issue.Status != domain.StatusCancelled {
		t.Fatalf("created issue status = %q, want cancelled", cancelled.Issue.Status)
	}

	// Satisfying the requirement lets create_issue{status:done} through.
	ac := "criteria"
	done, err := fixture.issues.CreateIssue(fixture.ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "satisfied create", Status: domain.StatusDone, AcceptanceCriteria: &ac,
	})
	if err != nil {
		t.Fatalf("CreateIssue(status=done, with acceptance_criteria) error = %v", err)
	}
	if done.Issue.Status != domain.StatusDone {
		t.Fatalf("created issue status = %q, want done", done.Issue.Status)
	}
}

func TestApplyIssuePlanRejectsGatedEntryAndRollsBackWholeBatch(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-apply-plan")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	})
	planningRepository, err := sqlite.NewPlanningRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	planningService, err := application.NewPlanningService(planningRepository, fixture.clock, fixture.generator)
	if err != nil {
		t.Fatal(err)
	}

	plan := domain.IssuePlan{Issues: []domain.PlannedIssue{
		{Ref: "ok", Type: domain.TypeTask, Title: "fine entry", Status: domain.StatusOpen},
		{Ref: "blocked", Type: domain.TypeTask, Title: "blank ac done entry", Status: domain.StatusDone},
	}}
	_, err = planningService.ApplyIssuePlan(fixture.ctx, plan, "plan-gate-1")
	assertWorkflowGateUnsatisfied(t, err)

	var issueCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issues WHERE title IN ('fine entry', 'blank ac done entry')`).Scan(&issueCount)
	}); err != nil {
		t.Fatal(err)
	}
	if issueCount != 0 {
		t.Fatalf("issues created despite a gate failure on one plan entry = %d, want 0 (whole batch rolled back)", issueCount)
	}
}

// TestApplyImportExemptFromGatesButValidatesFields covers ISSUE-201/172 AC6:
// apply_import never evaluates workflow gates (a blocking policy does not
// stop it from restoring a terminal-status issue), but it does run the same
// CreateIssueInput.Validate() every other creation path runs.
func TestApplyImportExemptFromGatesButValidatesFields(t *testing.T) {
	db, _ := openProjectDatabase(t, "Imported", "")
	ctx := context.Background()

	policyRepository, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(clock.NewFakeClock(time.Date(2026, 7, 17, 18, 24, 6, 0, time.UTC)), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatal(err)
	}
	policyID, err := generator.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policyRepository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
		ID: policyID,
		Input: domain.WorkflowPolicyInput{
			Selector:     domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeTask}},
			Requirements: []domain.PolicyRequirementInput{{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"}},
		},
		CreatedAt: time.Date(2026, 7, 17, 18, 24, 6, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	issueID, err := generator.New()
	if err != nil {
		t.Fatal(err)
	}
	document := domain.LogicalProjectDocument{
		Format: "rhizome-logical-project", Version: 1, ExportedAt: "2026-07-17T18:24:06Z",
		Project: domain.LogicalProjectProject{
			ID: sqliteTestProjectID, CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
		},
		Issues: []domain.LogicalIssue{{
			ID: issueID, Type: "task", Title: "historical done issue", Status: "done", Priority: "medium",
			CreatedAt: "2026-07-17T18:24:07Z", UpdatedAt: "2026-07-17T18:24:07Z",
		}},
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatal(err)
	}
	plan = assignImportDestinationIDs(t, plan)

	projectRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectRepository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v, want success -- import is exempt from gate evaluation even though a blocking policy is active", err)
	}

	// Now prove domain.CreateIssueInput.Validate() itself runs, not just the
	// interchange parser's own structural checks (which already cover
	// required fields, blocked_reason consistency, and epic-parent rules):
	// an over-long title exceeds domain.MaxTitleRunes, a bound the parser
	// does not enforce.
	invalidIssueID, err := generator.New()
	if err != nil {
		t.Fatal(err)
	}
	overlongTitle := strings.Repeat("x", domain.MaxTitleRunes+1)
	invalidDocument := domain.LogicalProjectDocument{
		Format: "rhizome-logical-project", Version: 1, ExportedAt: "2026-07-17T18:24:06Z",
		Project: domain.LogicalProjectProject{
			ID: sqliteTestProjectID, CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
		},
		Issues: []domain.LogicalIssue{{
			ID: invalidIssueID, Type: "task", Title: overlongTitle, Status: "open", Priority: "medium",
			CreatedAt: "2026-07-17T18:24:07Z", UpdatedAt: "2026-07-17T18:24:07Z",
		}},
	}
	invalidData, err := domain.MarshalLogicalProjectDocument(invalidDocument)
	if err != nil {
		t.Fatal(err)
	}
	invalidPlan, err := domain.ParseLogicalProjectImportPlan(invalidData)
	if err != nil {
		t.Fatal(err)
	}
	invalidPlan = assignImportDestinationIDs(t, invalidPlan)
	db2, _ := openProjectDatabase(t, "Imported2", "")
	projectRepository2, err := sqlite.NewProjectRepository(db2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = projectRepository2.ApplyLogicalProjectImport(ctx, invalidPlan)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeLimitExceeded {
		t.Fatalf("import with an over-long title error = %v, want CodeLimitExceeded (from domain.CreateIssueInput.Validate, not the interchange parser)", err)
	}
}

// TestClaimWorkRaceAgainstConcurrentPolicyEdit is the note's requested race
// coverage: claim_work loads active policies inside the same BEGIN
// IMMEDIATE transaction that creates the attempt and its snapshot, so a
// concurrent policy edit can never produce a torn read -- every claim's
// frozen snapshot deterministically reflects either the policy's state
// before or after the edit, never a mix, and every claim itself either
// succeeds or fails cleanly.
func TestClaimWorkRaceAgainstConcurrentPolicyEdit(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-claim-race")
	defer fixture.close()
	policy := createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	})
	policyRepository, err := sqlite.NewWorkflowPolicyRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}

	const attemptCount = 8
	issueIDs := make([]string, attemptCount)
	for i := range issueIDs {
		ac := "criteria"
		issue, err := fixture.issues.CreateIssue(fixture.ctx, domain.CreateIssueInput{
			Type: domain.TypeTask, Title: "race issue", Status: domain.StatusReady, AcceptanceCriteria: &ac,
		})
		if err != nil {
			t.Fatal(err)
		}
		issueIDs[i] = issue.ID
	}

	var group sync.WaitGroup
	claimErrs := make([]error, attemptCount)
	for i, issueID := range issueIDs {
		group.Add(1)
		go func(index int, issueID string) {
			defer group.Done()
			_, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issueID})
			claimErrs[index] = err
		}(i, issueID)
	}
	group.Add(1)
	go func() {
		defer group.Done()
		_, err := policyRepository.ArchivePolicy(fixture.ctx, ports.ArchiveWorkflowPolicyCommand{
			PolicyID: policy.ID, ExpectedVersion: policy.Version, ArchivedAt: fixture.clock.Now(),
		})
		if err != nil {
			t.Errorf("ArchivePolicy() error = %v", err)
		}
	}()
	group.Wait()

	for index, err := range claimErrs {
		if err != nil {
			t.Fatalf("claim %d error = %v, want success (acceptance_criteria was satisfied regardless of when the archive lands)", index, err)
		}
	}

	// Whatever each claim actually matched, its frozen snapshot must be
	// internally consistent: either the one requirement, or none at all --
	// never a partial/torn state.
	for i, issueID := range issueIDs {
		var attemptID string
		if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
			return query.QueryRowContext(ctx, `SELECT id FROM work_attempts WHERE issue_id = ?`, issueID).Scan(&attemptID)
		}); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		snapshot, err := policyRepository.GetAttemptGateSnapshot(fixture.ctx, ports.GetAttemptGateSnapshotCommand{AttemptID: attemptID})
		if err != nil {
			t.Fatalf("issue %d: GetAttemptGateSnapshot() error = %v", i, err)
		}
		if len(snapshot.Requirements) != 0 && (len(snapshot.Requirements) != 1 || snapshot.Requirements[0].Key != "ac") {
			t.Fatalf("issue %d: snapshot requirements = %+v, want either none or exactly the one full requirement", i, snapshot.Requirements)
		}
	}
}
