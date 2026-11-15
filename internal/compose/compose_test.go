package compose_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/compose"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/runtime"
)

func TestNewServicesWiresSnapshotLoadersForBoardAndIssueDetail(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)
	ctx := context.Background()

	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "compose.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	defer func() {
		if err := db.Close(context.Background()); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	}()
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatalf("migrations.Migrate() error = %v", err)
	}
	const projectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		ts := sqlite.FormatStorageTime(now)
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`, projectID, ts, ts)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	services, err := compose.NewServices(&runtime.Project{ProjectID: projectID, Database: db}, fakeClock)
	if err != nil {
		t.Fatalf("compose.NewServices() error = %v", err)
	}
	bundle := services.Bundle()

	if _, err := bundle.WorkflowPolicyService.CreatePolicy(ctx, domain.WorkflowPolicyInput{
		Selector: domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeTask}},
		Requirements: []domain.PolicyRequirementInput{
			{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		},
	}, nil, ""); err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}

	acceptance := "clear criteria"
	issue, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Composed issue", AcceptanceCriteria: &acceptance, Status: domain.StatusReady, Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	leaseSeconds := 60
	if _, err := bundle.AttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID: issue.ID, LeaseSeconds: &leaseSeconds,
	}); err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	board, err := bundle.BoardService.GetBoard(ctx)
	if err != nil {
		t.Fatalf("BoardService.GetBoard() error = %v", err)
	}
	if !board.GeneratedAt.Equal(now) || len(board.ActiveAttempts) != 1 || len(board.AttemptGates) != 1 {
		t.Fatalf("BoardService.GetBoard() = %#v, want GeneratedAt=%v and 1 active attempt gate", board, now)
	}

	detail, err := bundle.IssueDetailService.GetIssueDetail(ctx, issue.DisplayID)
	if err != nil {
		t.Fatalf("IssueDetailService.GetIssueDetail() error = %v", err)
	}
	if detail.Issue.ID != issue.ID || detail.LatestAttempt == nil || detail.RootIssueProjection == nil {
		t.Fatalf("IssueDetailService.GetIssueDetail() = %#v, want populated detail for %s", detail, issue.DisplayID)
	}
	if detail.Gates.Point != domain.EnforcementPointCompleteWorkToDone {
		t.Fatalf("Gates.Point = %q, want %q while attempt lease is active", detail.Gates.Point, domain.EnforcementPointCompleteWorkToDone)
	}

	// Advancing fakeClock past LeaseSeconds must carry through to the wired
	// IssueDetailService snapshot loader clock.
	fakeClock.Advance(120 * time.Second)
	expiredDetail, err := bundle.IssueDetailService.GetIssueDetail(ctx, issue.DisplayID)
	if err != nil {
		t.Fatalf("IssueDetailService.GetIssueDetail(after expiry) error = %v", err)
	}
	if expiredDetail.Gates.Point != domain.EnforcementPointClaimWork {
		t.Fatalf("Gates.Point after expiry = %q, want %q", expiredDetail.Gates.Point, domain.EnforcementPointClaimWork)
	}
}
