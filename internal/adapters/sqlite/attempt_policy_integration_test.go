package sqlite_test

import (
	"context"
	"math/rand"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// TestEvaluateClaimAgainstIsClaimableProjection verifies that the is_claimable
// projection query result matches the behavior of domain.EvaluateClaim for each
// combination of issue type, status, archived state, unresolved blockers, and
// active attempts. This ensures the SQLite adapter's decision logic and the
// domain's pure function are aligned (ISSUE-186 AC4).
func TestEvaluateClaimAgainstIsClaimableProjection(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()

	cases := []struct {
		name      string
		issueType domain.Type
		status    domain.Status
		archived  bool
		blockers  bool
		hasActive bool
		wantKind  domain.AttemptKind
		wantError bool
	}{
		{name: "ready task claims work", issueType: domain.TypeTask, status: domain.StatusReady, wantKind: domain.AttemptKindWork},
		{name: "ready bug claims work", issueType: domain.TypeBug, status: domain.StatusReady, wantKind: domain.AttemptKindWork},
		{name: "review task claims review", issueType: domain.TypeTask, status: domain.StatusReview, wantKind: domain.AttemptKindReview},
		{name: "archived issue rejected regardless of status", issueType: domain.TypeTask, status: domain.StatusReady, archived: true, wantError: true},
		{name: "epic is not executable", issueType: domain.TypeEpic, status: domain.StatusReady, wantError: true},
		{name: "unresolved blockers reject ready", issueType: domain.TypeTask, status: domain.StatusReady, blockers: true, wantError: true},
		{name: "open status is not claimable", issueType: domain.TypeTask, status: domain.StatusOpen, wantError: true},
		{name: "done status is not claimable", issueType: domain.TypeTask, status: domain.StatusDone, wantError: true},
		{name: "cancelled status is not claimable", issueType: domain.TypeTask, status: domain.StatusCancelled, wantError: true},
		{name: "active attempt blocks a second claim on ready", issueType: domain.TypeTask, status: domain.StatusReady, hasActive: true, wantError: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Create the main issue
			issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{
				Type:   testCase.issueType,
				Title:  testCase.name,
				Status: testCase.status,
			})
			if err != nil {
				t.Fatal(err)
			}

			// Archive if needed
			if testCase.archived {
				if _, err := service.ArchiveIssue(ctx, domain.ArchiveIssueInput{
					IssueID:         issue.ID,
					ExpectedVersion: 1,
				}); err != nil {
					t.Fatal(err)
				}
			}

			// Create a blocker if needed
			if testCase.blockers {
				blocker, err := service.CreateIssue(ctx, domain.CreateIssueInput{
					Type:  domain.TypeTask,
					Title: testCase.name + " blocker",
				})
				if err != nil {
					t.Fatal(err)
				}
				relationRepository, err := sqlite.NewRelationRepository(db)
				if err != nil {
					t.Fatal(err)
				}
				generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.New(rand.NewSource(1)))
				if err != nil {
					t.Fatal(err)
				}
				relationService, err := application.NewRelationService(relationRepository, clock.NewFakeClock(now), generator)
				if err != nil {
					t.Fatal(err)
				}
				_, err = relationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
					Action:        domain.RelationActionAdd,
					SourceIssueID: blocker.ID,
					TargetIssueID: issue.ID,
					RelationType:  domain.RelationTypeBlocks,
				})
				if err != nil {
					t.Fatal(err)
				}
			}

			// Create an active attempt if needed
			if testCase.hasActive {
				attemptRepository, err := sqlite.NewAttemptRepository(db)
				if err != nil {
					t.Fatal(err)
				}
				generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.New(rand.NewSource(1)))
				if err != nil {
					t.Fatal(err)
				}
				attemptService, err := application.NewAttemptService(attemptRepository, clock.NewFakeClock(now), generator)
				if err != nil {
					t.Fatal(err)
				}
				_, err = attemptService.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: issue.ID})
				if err != nil {
					t.Fatal(err)
				}
			}

			// Get the issue projection from the database
			repository, err := sqlite.NewIssueRepository(db)
			if err != nil {
				t.Fatal(err)
			}
			identifier, err := domain.ParseIssueIdentifier(issue.ID)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := repository.GetIssueProjection(ctx, ports.GetIssueProjectionCommand{
				Identifier: identifier,
				Now:        now,
			})
			if err != nil {
				t.Fatal(err)
			}

			// Compute the expected result using domain.EvaluateClaim
			var blockerCount int64
			if testCase.blockers {
				blockerCount = 1
			}
			// Build a test issue using the projection's current state
			testIssue := domain.Issue{
				Type:       projection.Type,
				Status:     projection.Status,
				ArchivedAt: projection.ArchivedAt,
			}
			expectedKind, expectedErr := domain.EvaluateClaim(
				testIssue,
				blockerCount,
				testCase.hasActive,
			)

			// Verify the projection matches
			if testCase.wantError {
				if expectedErr == nil {
					t.Fatalf("domain.EvaluateClaim expected error, got nil (kind=%q)", expectedKind)
				}
				if projection.IsClaimable {
					t.Fatalf("is_claimable = true, want false (domain.EvaluateClaim returned error: %v)", expectedErr)
				}
			} else {
				if expectedErr != nil {
					t.Fatalf("domain.EvaluateClaim unexpected error: %v", expectedErr)
				}
				if !projection.IsClaimable {
					t.Fatalf("is_claimable = false, want true (domain.EvaluateClaim succeeded with kind %q)", expectedKind)
				}
			}
		})
	}
}
