package sqlite_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

// TestActivityRepositoryIncludesGateEvidence proves ISSUE-171 AC5's "visible
// in issue activity" claim end-to-end: submitted evidence appears in the
// unfiltered get_issue_activity feed under the gate_evidence category, and
// filtering to just that category returns exactly the evidence record.
func TestActivityRepositoryIncludesGateEvidence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.FixedZone("test", 2*60*60)).UTC()
	db := openTestDB(t, filepath.Join(t.TempDir(), "activity-gate-evidence.db"), true)
	fakeClock := clock.NewFakeClock(now)
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			activityTestProjectID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatalf("NewIssueRepository() error = %v", err)
	}
	issues, err := application.NewIssueService(issueRepository, fakeClock, generator)
	if err != nil {
		t.Fatalf("NewIssueService() error = %v", err)
	}
	attemptRepository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatalf("NewAttemptRepository() error = %v", err)
	}
	attempts, err := application.NewAttemptService(attemptRepository, fakeClock, generator)
	if err != nil {
		t.Fatalf("NewAttemptService() error = %v", err)
	}
	activityRepository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatalf("NewActivityRepository() error = %v", err)
	}

	// A matching workflow policy must exist before claim, so the real
	// claim_work gate evaluation (ISSUE-172) freezes this requirement into
	// the new attempt's snapshot itself -- attempt_gate_snapshots is
	// immutable, so nothing can seed one after the fact.
	policyRepository, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository() error = %v", err)
	}
	policyID, err := generator.New()
	if err != nil {
		t.Fatalf("generator.New() error = %v", err)
	}
	if _, err := policyRepository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
		ID: policyID,
		Input: domain.WorkflowPolicyInput{
			Selector: domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeTask}},
			Requirements: []domain.PolicyRequirementInput{
				{Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
			},
		},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}

	issue, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "gate evidence activity", Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	claimed, err := attempts.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	submitted, err := attempts.SubmitGateEvidence(ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "implemented and tested",
	})
	if err != nil {
		t.Fatalf("SubmitGateEvidence() error = %v", err)
	}

	unfiltered, err := activityRepository.GetIssueActivity(ctx, ports.GetIssueActivityCommand{
		Input: domain.GetIssueActivityInput{IssueID: issue.ID, Limit: 20},
	})
	if err != nil {
		t.Fatalf("GetIssueActivity() error = %v", err)
	}
	found := false
	for _, item := range unfiltered.Items {
		if item.EntityType == domain.ActivityEntityTypeGateEvidence {
			found = true
			if item.GateEvidence == nil || item.GateEvidence.ID != submitted.Evidence.ID {
				t.Fatalf("gate_evidence activity item = %#v, want it to wrap the submitted evidence", item)
			}
		}
	}
	if !found {
		t.Fatalf("unfiltered activity feed = %#v, want a gate_evidence entry included by default", unfiltered.Items)
	}

	filtered, err := activityRepository.GetIssueActivity(ctx, ports.GetIssueActivityCommand{
		Input: domain.GetIssueActivityInput{IssueID: issue.ID, Types: []domain.ActivityCategory{domain.ActivityCategoryGateEvidence}, Limit: 20},
	})
	if err != nil {
		t.Fatalf("GetIssueActivity() filtered error = %v", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].EntityType != domain.ActivityEntityTypeGateEvidence {
		t.Fatalf("filtered activity feed = %#v, want exactly the one gate_evidence entry", filtered.Items)
	}
}
