package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// TestLoadIssueGateSummaryResolvesBothIdentifierForms pins the identifier
// contract the board and issue-detail surfaces rely on: either public form
// produces an identical summary. Non-vacuous by construction -- the fixture
// policy must have matched, or the test fails rather than comparing two
// empty summaries.
func TestLoadIssueGateSummaryResolvesBothIdentifierForms(t *testing.T) {
	issueService, db, now := openIssueService(t)
	repository, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository() error = %v", err)
	}
	ctx := context.Background()

	if _, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
		ID: workflowPolicyTestID, Input: validPolicyInput(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	issueResult, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:  domain.TypeTask,
		Title: "Gate summary issue",
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	displayIdentifier, err := domain.ParseIssueIdentifier(issueResult.DisplayID)
	if err != nil {
		t.Fatalf("ParseIssueIdentifier(%q) error = %v", issueResult.DisplayID, err)
	}
	byDisplay, err := repository.LoadIssueGateSummary(ctx, ports.IssueGateSummaryCommand{Identifier: displayIdentifier, Now: now})
	if err != nil {
		t.Fatalf("LoadIssueGateSummary(%s) error = %v", issueResult.DisplayID, err)
	}
	internalIdentifier, err := domain.ParseIssueIdentifier(issueResult.ID)
	if err != nil {
		t.Fatalf("ParseIssueIdentifier(ULID) error = %v", err)
	}
	byInternal, err := repository.LoadIssueGateSummary(ctx, ports.IssueGateSummaryCommand{Identifier: internalIdentifier, Now: now})
	if err != nil {
		t.Fatalf("LoadIssueGateSummary(ULID) error = %v", err)
	}
	if !reflect.DeepEqual(byDisplay, byInternal) {
		t.Fatalf("summary by display ID = %#v, want the same as by internal ID %#v", byDisplay, byInternal)
	}
	if byDisplay.RequirementCount == 0 {
		t.Fatal("requirement count is 0; the fixture policy should have matched, so this test would pass vacuously")
	}
	// The fixture issue has no acceptance criteria, so the nonblank
	// requirement must be reported unmet -- the summary carries actionable
	// state, not just counts.
	if len(byDisplay.Unmet) != 1 || byDisplay.Unmet[0].RequirementKey != "acceptance_criteria" {
		t.Fatalf("unmet = %#v, want the acceptance_criteria requirement", byDisplay.Unmet)
	}
	if byDisplay.Point != domain.EnforcementPointClaimWork {
		t.Fatalf("point = %q, want claim_work for an unclaimed issue", byDisplay.Point)
	}
}

// TestLoadIssueGateSummaryRejectsUnknownIssue pins ISSUE_NOT_FOUND for a
// well-formed identifier matching nothing.
func TestLoadIssueGateSummaryRejectsUnknownIssue(t *testing.T) {
	repository, _, now := openWorkflowPolicyRepository(t)

	unknown, err := domain.ParseIssueIdentifier("ISSUE-999999")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.LoadIssueGateSummary(context.Background(), ports.IssueGateSummaryCommand{Identifier: unknown, Now: now})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeIssueNotFound {
		t.Fatalf("error = %v, want CodeIssueNotFound", err)
	}
}

// TestLoadIssueGateSummaryRequiresTimestamp mirrors GetWorkContext's
// command-timestamp contract: the active-attempt lease check must never
// silently anchor to the zero time.
func TestLoadIssueGateSummaryRequiresTimestamp(t *testing.T) {
	repository, _, _ := openWorkflowPolicyRepository(t)

	identifier, err := domain.ParseIssueIdentifier("ISSUE-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.LoadIssueGateSummary(context.Background(), ports.IssueGateSummaryCommand{Identifier: identifier, Now: time.Time{}})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("error = %v, want CodeInvalidArgument for a zero timestamp", err)
	}
}
