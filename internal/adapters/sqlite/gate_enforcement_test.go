package sqlite_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// issueWriteSite is one source location whose SQL text can write the issues
// table, resolved to the function that encloses it.
type issueWriteSite struct {
	File     string
	Function string
	Line     int
}

func (site issueWriteSite) key() string { return site.File + ":" + site.Function }

// allowedIssueWriteSites is the complete set of functions permitted to write
// the issues table, each with what makes it legitimate. Adding an entry here
// is the conscious act TestGateEnforcementNoStatusWriteOutsideTheChokePoint
// exists to force.
var allowedIssueWriteSites = map[string]string{
	"attempts.go:FinishAttempt":              "gated: enforcementPointForFinish + evaluateGateAgainstAttemptSnapshot",
	"issues.go:CreateIssue":                  "gated: enforcementPointForCreateStatus + evaluateGateAgainstLivePolicies",
	"planning.go:applyPlan":                  "gated per entry: enforcementPointForCreateStatus + evaluateGateAgainstLivePolicies",
	"issues.go:UpdateIssue":                  "ungated by design: domain.ApplyIssuePatch (internal/domain/issue_patch.go) rejects a status patch to review/done unconditionally, before this file runs, so no gated status can reach here (docs/02 §17.1)",
	"projects.go:ApplyLogicalProjectImport":  "ungated by design: import is exempt from gate evaluation (docs/02 §17.1, ISSUE-201); it still runs domain.CreateIssueInput{}.Validate() on every imported issue",
	"unit_of_work.go:ConditionalUpdateIssue": "generic caller-parameterized helper (ports.UnitOfWork) with a dynamic SET clause; it does not itself write status, and TestConditionalUpdateIssueHasNoUngatedCallers guards its callers",
}

// TestGateEnforcementNoStatusWriteOutsideTheChokePoint is ISSUE-172's
// requested regression tripwire: "a repo-wide test fails on any SET status
// outside the choke point."
//
// Granularity, stated honestly (ISSUE-221). The scan resolves every line of
// this package's non-test source whose text contains "UPDATE issues SET" or
// "INSERT INTO issues(" to its enclosing function, and compares the resulting
// (file, function) set against allowedIssueWriteSites. A new write added
// inside an already-listed *file* therefore fails, as long as it lives in a
// function that is not itself listed.
//
// What it still cannot catch, and what covers that instead:
//   - a new write added inside an already-allowlisted *function* -- reviewed
//     by hand; the allowlist entry records why that function is legitimate;
//   - SQL assembled from a variable, a helper, or fragments that never spell
//     the marker literally -- ConditionalUpdateIssue is the one such helper
//     that exists, and TestConditionalUpdateIssueHasNoUngatedCallers guards it;
//   - writes from outside this package -- the issues table is only reachable
//     through this package's repositories.
//
// TestGateEnforcementScanReportsUngatedSiteInsideAnAllowlistedFile proves the
// first of the guarantees above against a fixture rather than asserting it.
func TestGateEnforcementNoStatusWriteOutsideTheChokePoint(t *testing.T) {
	seen := map[string]bool{}
	for _, site := range scanIssueWriteSites(t, ".") {
		if _, allowed := allowedIssueWriteSites[site.key()]; !allowed {
			t.Errorf("%s:%d: %s writes the issues table but is not an allowlisted site -- gate it through gate_enforcement.go (evaluateGateAgainstLivePolicies or evaluateGateAgainstAttemptSnapshot), or, if it is exempt, add %q to allowedIssueWriteSites with the docs/02 §17.1 justification",
				site.File, site.Line, site.Function, site.key())
			continue
		}
		seen[site.key()] = true
	}
	for key := range allowedIssueWriteSites {
		if !seen[key] {
			t.Errorf("allowlisted site %s no longer writes the issues table -- remove its allowedIssueWriteSites entry so the allowlist keeps meaning something", key)
		}
	}
}

// TestGateEnforcementScanReportsUngatedSiteInsideAnAllowlistedFile is the
// falsifying case for the scan above (ISSUE-221 AC1): testdata/ungated_fixture
// holds an issues.go with an allowlisted function plus a second, ungated
// status write in the same file. A file-granularity scan sees one file it
// already trusts; this scan must report the new function by name and line.
func TestGateEnforcementScanReportsUngatedSiteInsideAnAllowlistedFile(t *testing.T) {
	sites := scanIssueWriteSites(t, filepath.Join("testdata", "ungated_fixture"))
	var unallowed []issueWriteSite
	for _, site := range sites {
		if _, allowed := allowedIssueWriteSites[site.key()]; !allowed {
			unallowed = append(unallowed, site)
		}
	}
	if len(unallowed) != 1 {
		t.Fatalf("unallowlisted sites in the fixture = %+v, want exactly one", unallowed)
	}
	got := unallowed[0]
	if got.File != "issues.go" || got.Function != "sneakUngatedStatusWrite" {
		t.Fatalf("reported site = %s:%s, want issues.go:sneakUngatedStatusWrite", got.File, got.Function)
	}
	wantLine := fixtureUpdateLine(t)
	if got.Line != wantLine {
		t.Fatalf("reported line = %d, want %d (the fixture's UPDATE line)", got.Line, wantLine)
	}
	// The allowlisted twin in the same file must not be reported: proving the
	// scan discriminates by function, not by file.
	if len(sites) != 2 {
		t.Fatalf("all fixture sites = %+v, want two (the allowlisted CreateIssue and the ungated one)", sites)
	}
}

// fixtureUpdateLine reports which line of the fixture holds its ungated
// UPDATE, so the expectation above tracks the fixture instead of a constant
// that goes stale the moment a comment line is added to it.
func fixtureUpdateLine(t *testing.T) int {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "ungated_fixture", "issues.go"))
	if err != nil {
		t.Fatal(err)
	}
	for index, line := range strings.Split(string(content), "\n") {
		if strings.Contains(line, "UPDATE issues SET") {
			return index + 1
		}
	}
	t.Fatal("fixture no longer contains an UPDATE issues SET line")
	return 0
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

// scanIssueWriteSites returns every line in dir's non-test .go files whose
// text contains an issues-table write marker, resolved to the function that
// encloses it and sorted by file then line. A marker outside any function
// body is reported with the function name "(package level)", which no
// allowlist entry names, so it fails loudly rather than passing unseen.
func scanIssueWriteSites(t *testing.T, dir string) []issueWriteSite {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	var sites []issueWriteSite
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fileSet, path, content, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, line := range issueWriteMarkerLines(string(content)) {
			sites = append(sites, issueWriteSite{
				File:     filepath.Base(path),
				Function: enclosingFunctionName(fileSet, file, line),
				Line:     line,
			})
		}
	}
	slices.SortFunc(sites, func(left, right issueWriteSite) int {
		if left.File != right.File {
			return strings.Compare(left.File, right.File)
		}
		return left.Line - right.Line
	})
	return sites
}

// issueWriteMarkerLines returns the 1-based line numbers whose text writes the
// issues table. Both markers on one line count once: they cannot both start a
// statement.
func issueWriteMarkerLines(content string) []int {
	var lines []int
	for index, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "INSERT INTO issues(") || strings.Contains(line, "UPDATE issues SET") {
			lines = append(lines, index+1)
		}
	}
	return lines
}

// enclosingFunctionName returns the name of the function or method whose body
// spans line, or "(package level)" when the line sits outside every function.
func enclosingFunctionName(fileSet *token.FileSet, file *ast.File, line int) string {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		start := fileSet.Position(function.Body.Lbrace).Line
		end := fileSet.Position(function.Body.Rbrace).Line
		if line >= start && line <= end {
			return function.Name.Name
		}
	}
	return "(package level)"
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

// TestApproveReviewRejectsUnsatisfiableReviewApproval covers approve_review
// with no review request ever bound to the attempt ("review is optional"
// backward compat, ISSUE-173): with no request, there is no review target to
// freeze or read a snapshot from, so this falls back to re-evaluating the
// reviewing attempt's own claim-time snapshot (evaluateGateAgainstAttemptSnapshot),
// exactly like ISSUE-172's original interim behavior. ReviewApprovalPurposes
// stays empty on that path -- nothing was ever granted -- so an active
// review_approval requirement can never be satisfied this way; fail rather
// than silently bypass. A request that IS bound instead evaluates against
// its own review target's frozen snapshot, using the purposes the request
// itself covers as evidence (see review_purpose_test.go).
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

// TestWorkflowGateUnsatisfiedDetailShapeMatchesDocumentedContract pins the
// wire shape docs/02 §17.7 and docs/03 §13 lock (ISSUE-220): the error
// message text, and one project-standard {field, code, message} detail per
// unmet requirement -- requirement_key structured in Field, and policy_id,
// enforcement_point, and reason packed into Message. Two policies are used
// so the documented policy_id-then-key ordering is exercised too, and so
// the packed policy_id is the only thing distinguishing two details that
// are otherwise identical. Requirement Key ("ac") deliberately differs from
// the gated Field ("acceptance_criteria") so the test would catch the two
// being confused.
func TestWorkflowGateUnsatisfiedDetailShapeMatchesDocumentedContract(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-detail-shape")
	defer fixture.close()
	requirements := []domain.PolicyRequirementInput{
		{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	}
	firstPolicy := createWorkflowPolicy(t, fixture, allTasksSelector(), requirements)
	secondPolicy := createWorkflowPolicy(t, fixture, allTasksSelector(), requirements)
	issue := createAttemptIssue(t, fixture, "blank ac", domain.StatusReady)

	_, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	if domainErr.Code != domain.CodeWorkflowGateUnsatisfied {
		t.Fatalf("code = %q, want %q", domainErr.Code, domain.CodeWorkflowGateUnsatisfied)
	}
	if domainErr.Message != "workflow gate requirements are not satisfied" {
		t.Fatalf("message = %q, want the docs/02 §17.7 text", domainErr.Message)
	}

	orderedPolicyIDs := []string{firstPolicy.ID, secondPolicy.ID}
	slices.Sort(orderedPolicyIDs)
	want := make([]domain.Detail, len(orderedPolicyIDs))
	for index, policyID := range orderedPolicyIDs {
		want[index] = domain.Detail{
			Field: "ac",
			Code:  domain.CodeWorkflowGateUnsatisfied,
			Message: "policy_id=" + policyID +
				" enforcement_point=claim_work: issue field 'acceptance_criteria' is blank",
		}
	}
	if !reflect.DeepEqual(domainErr.Details, want) {
		t.Fatalf("details = %#v, want %#v", domainErr.Details, want)
	}
}
