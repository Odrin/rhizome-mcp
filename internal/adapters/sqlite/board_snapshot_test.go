package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/compose"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
	"rhizome-mcp/internal/runtime"
)

func openBoardSnapshotFixture(t *testing.T) (*sqlite.DB, string, time.Time, *clock.FakeClock, application.Bundle) {
	t.Helper()
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	dbPath := filepath.Join(t.TempDir(), "board-snapshot.db")
	db := openTestDB(t, dbPath, true)
	fakeClock := clock.NewFakeClock(now)
	if _, err := migrations.Migrate(context.Background(), db, fakeClock); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		ts := sqlite.FormatStorageTime(now)
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`, sqliteTestProjectID, ts, ts)
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	services, err := compose.NewServices(&runtime.Project{ProjectID: sqliteTestProjectID, Database: db}, fakeClock)
	if err != nil {
		t.Fatalf("compose.NewServices() error = %v", err)
	}
	return db, dbPath, now, fakeClock, services.Bundle()
}

func latestEventIDForIssue(t *testing.T, db *sqlite.DB, issueID string) int64 {
	t.Helper()
	var eventID int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events WHERE issue_id = ?`, issueID).Scan(&eventID)
	}); err != nil {
		t.Fatalf("query latest event for %s: %v", issueID, err)
	}
	return eventID
}

func TestNewBoardSnapshotRepositoryRejectsNilDB(t *testing.T) {
	if _, err := sqlite.NewBoardSnapshotRepository(nil); !errors.Is(err, &domain.Error{Code: domain.CodeStorageConfiguration}) {
		t.Fatalf("NewBoardSnapshotRepository(nil) error = %v, want %s", err, domain.CodeStorageConfiguration)
	}
}

func TestBoardSnapshotRepositoryGetBoardParityWithComposedServiceAndSingleConnection(t *testing.T) {
	db, _, now, fakeClock, bundle := openBoardSnapshotFixture(t)
	ctx := context.Background()

	// Seed active policy requiring nonblank acceptance criteria and attempt evidence.
	if _, err := bundle.WorkflowPolicyService.CreatePolicy(ctx, domain.WorkflowPolicyInput{
		Selector: domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeTask}},
		Requirements: []domain.PolicyRequirementInput{
			{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
			{Key: "tests", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "tests"},
		},
	}, nil, ""); err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}

	acceptance := "clear acceptance criteria"
	epic, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeEpic, Title: "Parent Epic", Status: domain.StatusOpen, Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("CreateIssue(epic) error = %v", err)
	}
	taskOne, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Attempted Task With Evidence", AcceptanceCriteria: &acceptance,
		Status: domain.StatusReady, Priority: domain.PriorityHigh, ParentID: &epic.ID,
		Labels: []string{"backend"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatalf("CreateIssue(taskOne) error = %v", err)
	}
	taskTwo, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Attempted Task Missing Evidence", AcceptanceCriteria: &acceptance,
		Status: domain.StatusReady, Priority: domain.PriorityMedium, ParentID: &epic.ID,
		Labels: []string{"api"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatalf("CreateIssue(taskTwo) error = %v", err)
	}
	blockedTask, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Blocked Task", Status: domain.StatusReady, Priority: domain.PriorityHigh,
		Labels: []string{"blocked-label"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatalf("CreateIssue(blockedTask) error = %v", err)
	}
	reviewTask, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeBug, Title: "Review Bug", Status: domain.StatusReview, Priority: domain.PriorityMedium,
	})
	if err != nil {
		t.Fatalf("CreateIssue(reviewTask) error = %v", err)
	}
	doneTask, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeBug, Title: "Done Bug", Status: domain.StatusDone, Priority: domain.PriorityLow,
	})
	if err != nil {
		t.Fatalf("CreateIssue(doneTask) error = %v", err)
	}
	_ = doneTask

	if _, err := bundle.RelationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: taskOne.ID, TargetIssueID: blockedTask.ID, RelationType: domain.RelationTypeBlocks,
	}); err != nil {
		t.Fatalf("ManageIssueRelation(blocks) error = %v", err)
	}

	claimOne, err := bundle.AttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID: taskOne.ID,
		Resources: []domain.Resource{
			{Kind: domain.ResourceKindFile, Path: "internal/compose/compose.go"},
		},
	})
	if err != nil {
		t.Fatalf("ClaimIssue(taskOne) error = %v", err)
	}
	if _, err := bundle.AttemptService.SubmitGateEvidence(ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimOne.Attempt.ID, LeaseToken: claimOne.LeaseToken,
		Key: "tests", Result: domain.EvidenceResultSatisfied, Summary: "unit tests passed",
	}); err != nil {
		t.Fatalf("SubmitGateEvidence(taskOne) error = %v", err)
	}

	if _, err := bundle.AttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID: taskTwo.ID,
		Resources: []domain.Resource{
			{Kind: domain.ResourceKindFile, Path: "internal/ports/board.go"},
		},
	}); err != nil {
		t.Fatalf("ClaimIssue(taskTwo) error = %v", err)
	}

	// Seed an un-swept expired active attempt holding an orphaned active reservation
	// to verify FilterReservationsByActiveAttempts parity.
	orphanIssue, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Expired Attempt Issue", Status: domain.StatusOpen, Priority: domain.PriorityLow,
	})
	if err != nil {
		t.Fatalf("CreateIssue(orphanIssue) error = %v", err)
	}
	expiredAttemptID := "01ARZ3NDEKTSV4RRFFQ69G5FA9"
	if err := seedAttempt(t, db, expiredAttemptID, orphanIssue.ID, domain.AttemptStatusActive, now.Add(-time.Minute), now.Add(-10*time.Minute), nil, now); err != nil {
		t.Fatalf("seedAttempt(expired) error = %v", err)
	}
	if err := seedReservation(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FAB", orphanIssue.ID, expiredAttemptID, domain.ResourceKindFile, "orphan.go", domain.ReservationStatusActive, now, nil, nil); err != nil {
		t.Fatalf("seedReservation(orphan) error = %v", err)
	}

	reviewTargetEvent := latestEventIDForIssue(t, db, reviewTask.ID)
	if _, err := bundle.ReviewService.CreateReviewRequest(ctx, application.CreateReviewRequestInput{
		IssueID: reviewTask.ID, TargetIssueVersion: reviewTask.Issue.Version, TargetEventID: reviewTargetEvent,
	}); err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}

	composedBoardService, err := application.NewBoardService(
		bundle.IssueService, bundle.AttemptService, bundle.ReservationService,
		bundle.ReviewService, bundle.GraphService, bundle.WorkflowPolicyService, fakeClock,
	)
	if err != nil {
		t.Fatalf("NewBoardService(composed) error = %v", err)
	}

	composedResult, err := composedBoardService.GetBoard(ctx)
	if err != nil {
		t.Fatalf("composedBoardService.GetBoard() error = %v", err)
	}
	snapshotResult, err := bundle.BoardService.GetBoard(ctx)
	if err != nil {
		t.Fatalf("snapshot BoardService.GetBoard() error = %v", err)
	}

	if !reflect.DeepEqual(snapshotResult, composedResult) {
		t.Fatalf("snapshot GetBoard mismatch with composed GetBoard:\nsnapshot = %#v\ncomposed = %#v", snapshotResult, composedResult)
	}
	if len(snapshotResult.AttemptGates) != 2 {
		t.Fatalf("len(AttemptGates) = %d, want 2", len(snapshotResult.AttemptGates))
	}
	if snapshotResult.AttemptGates[0].IssueID != taskOne.ID || snapshotResult.AttemptGates[0].Gates.SatisfiedCount != 2 || len(snapshotResult.AttemptGates[0].Gates.Unmet) != 0 {
		t.Fatalf("AttemptGates[0] = %#v, want satisfied gates for taskOne", snapshotResult.AttemptGates[0])
	}
	if snapshotResult.AttemptGates[1].IssueID != taskTwo.ID || snapshotResult.AttemptGates[1].Gates.SatisfiedCount != 1 || len(snapshotResult.AttemptGates[1].Gates.Unmet) != 1 {
		t.Fatalf("AttemptGates[1] = %#v, want 1 satisfied / 1 unmet gate for taskTwo", snapshotResult.AttemptGates[1])
	}
	if len(snapshotResult.ActiveReservations) != 2 {
		t.Fatalf("len(ActiveReservations) = %d, want 2 (orphaned reservation excluded)", len(snapshotResult.ActiveReservations))
	}
	if len(snapshotResult.BlockedIssues) != 1 || len(snapshotResult.BlockedIssues[0].Labels) != 1 || snapshotResult.BlockedIssues[0].Labels[0].Name != "blocked-label" {
		t.Fatalf("BlockedIssues = %#v, want blockedTask with label", snapshotResult.BlockedIssues)
	}
	if len(snapshotResult.ReviewRequests) != 1 || snapshotResult.ReviewRequests[0].IssueID != reviewTask.ID {
		t.Fatalf("ReviewRequests = %#v, want open review for reviewTask", snapshotResult.ReviewRequests)
	}

	// Pin the database pool to a single open connection: if any sub-query opens a
	// second read transaction while readSnapshot holds the connection, it blocks.
	sqlite.SetPoolMaxOpenConnsForTest(db, 1)
	singleConnCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	singleConnResult, err := bundle.BoardService.GetBoard(singleConnCtx)
	if err != nil {
		t.Fatalf("GetBoard(MaxOpenConns=1) error = %v", err)
	}
	if !reflect.DeepEqual(singleConnResult, composedResult) {
		t.Fatalf("single-conn GetBoard mismatch: got %#v, want %#v", singleConnResult, composedResult)
	}
}

func TestBoardSnapshotRepositoryGetBoardConsistentUnderConcurrentWriters(t *testing.T) {
	db, dbPath, now, fakeClock, bundle := openBoardSnapshotFixture(t)
	ctx := context.Background()

	seedBlockedTitle := "Initial Blocker"
	blocker, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: seedBlockedTitle, Status: domain.StatusOpen, Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("CreateIssue(blocker) error = %v", err)
	}
	_ = blocker

	repo, err := sqlite.NewBoardSnapshotRepository(db)
	if err != nil {
		t.Fatalf("NewBoardSnapshotRepository() error = %v", err)
	}

	t.Run("uncommitted writer transaction is invisible", func(t *testing.T) {
		rawDB, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("sql.Open() error = %v", err)
		}
		defer rawDB.Close()
		conn, err := rawDB.Conn(ctx)
		if err != nil {
			t.Fatalf("rawDB.Conn() error = %v", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatalf("BEGIN error = %v", err)
		}
		defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()

		uncommittedIssueID := "01ARZ3NDEKTSV4RRFFQ69G5FC1"
		ts := sqlite.FormatStorageTime(now)
		if _, err := conn.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 99, 'task', 'uncommitted', 'ready', 'high', 1, ?, ?)`, uncommittedIssueID, ts, ts); err != nil {
			t.Fatalf("INSERT uncommitted issue: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FC2', ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`, uncommittedIssueID, sqlite.FormatStorageTime(now.Add(time.Hour)), ts, ts); err != nil {
			t.Fatalf("INSERT uncommitted attempt: %v", err)
		}

		board, err := repo.LoadBoardSnapshot(ctx, now)
		if err != nil {
			t.Fatalf("LoadBoardSnapshot() error = %v", err)
		}
		if len(board.ActiveAttempts) != 0 || len(board.AttemptGates) != 0 || len(board.PlanningGraph.Nodes) != 1 {
			t.Fatalf("board leaked uncommitted data: attempts=%d gates=%d nodes=%d", len(board.ActiveAttempts), len(board.AttemptGates), len(board.PlanningGraph.Nodes))
		}
	})

	t.Run("committed concurrent writer mid-snapshot produces zero torn sections", func(t *testing.T) {
		var committedDuringHook bool
		cleanup := sqlite.SetBoardSnapshotAfterFirstReadHookForTest(repo, func() {
			if committedDuringHook {
				return
			}
			committedDuringHook = true
			// Commit a brand-new issue, active attempt, reservation, blocked relation,
			// and review request through a separate writer connection mid-snapshot.
			fakeClock.Advance(time.Second)
			concurrentIssue, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
				Type: domain.TypeTask, Title: "Concurrent Committed Issue", Status: domain.StatusReady, Priority: domain.PriorityCritical,
			})
			if err != nil {
				t.Fatalf("concurrent CreateIssue() error = %v", err)
			}
			claim, err := bundle.AttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
				IssueID: concurrentIssue.ID,
				Resources: []domain.Resource{
					{Kind: domain.ResourceKindFile, Path: "concurrent.go"},
				},
			})
			if err != nil {
				t.Fatalf("concurrent ClaimIssue() error = %v", err)
			}
			_ = claim
			if _, err := bundle.RelationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
				Action: domain.RelationActionAdd, SourceIssueID: concurrentIssue.ID, TargetIssueID: blocker.ID, RelationType: domain.RelationTypeBlocks,
			}); err != nil {
				t.Fatalf("concurrent ManageIssueRelation() error = %v", err)
			}
			if _, err := bundle.ReviewService.CreateReviewRequest(ctx, application.CreateReviewRequestInput{
				IssueID: concurrentIssue.ID, TargetIssueVersion: claim.Issue.Version, TargetEventID: latestEventIDForIssue(t, db, concurrentIssue.ID),
			}); err != nil {
				t.Fatalf("concurrent CreateReviewRequest() error = %v", err)
			}
		})
		defer cleanup()

		snapshotBoard, err := repo.LoadBoardSnapshot(ctx, now)
		if err != nil {
			t.Fatalf("LoadBoardSnapshot() error = %v", err)
		}
		if !committedDuringHook {
			t.Fatal("expected mid-snapshot hook to run")
		}
		if len(snapshotBoard.ActiveAttempts) != 0 || len(snapshotBoard.AttemptGates) != 0 ||
			len(snapshotBoard.ActiveReservations) != 0 || len(snapshotBoard.BlockedIssues) != 0 ||
			len(snapshotBoard.ReviewRequests) != 0 || len(snapshotBoard.PlanningGraph.Nodes) != 1 {
			t.Fatalf("torn board snapshot observed mid-transaction: %#v", snapshotBoard)
		}

		// Subsequent read after the transaction finishes observes the full new committed state.
		laterBoard, err := repo.LoadBoardSnapshot(ctx, fakeClock.Now().UTC())
		if err != nil {
			t.Fatalf("subsequent LoadBoardSnapshot() error = %v", err)
		}
		if len(laterBoard.ActiveAttempts) != 1 || len(laterBoard.AttemptGates) != 1 ||
			len(laterBoard.ActiveReservations) != 1 || len(laterBoard.BlockedIssues) != 1 ||
			len(laterBoard.ReviewRequests) != 1 || len(laterBoard.PlanningGraph.Nodes) != 2 {
			t.Fatalf("subsequent LoadBoardSnapshot() missing committed state: %#v", laterBoard)
		}
	})
}

func TestBoardSnapshotRepositoryGetIssueDetailParityAndArchivedSupport(t *testing.T) {
	db, _, now, fakeClock, bundle := openBoardSnapshotFixture(t)
	ctx := context.Background()

	if _, err := bundle.WorkflowPolicyService.CreatePolicy(ctx, domain.WorkflowPolicyInput{
		Selector: domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeTask}},
		Requirements: []domain.PolicyRequirementInput{
			{Key: "ac", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		},
	}, nil, ""); err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}

	parent, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeEpic, Title: "Parent Epic", Status: domain.StatusOpen, Priority: domain.PriorityMedium,
	})
	if err != nil {
		t.Fatalf("CreateIssue(parent) error = %v", err)
	}
	acceptance := "clear acceptance criteria"
	target, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Active Detail Issue", AcceptanceCriteria: &acceptance,
		Status: domain.StatusReady, Priority: domain.PriorityHigh,
		ParentID: &parent.ID, Labels: []string{"core"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatalf("CreateIssue(target) error = %v", err)
	}
	claim, err := bundle.AttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID: target.ID,
		Resources: []domain.Resource{
			{Kind: domain.ResourceKindFile, Path: "internal/adapters/sqlite/board_snapshot.go"},
		},
	})
	if err != nil {
		t.Fatalf("ClaimIssue(target) error = %v", err)
	}
	blocker, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Upstream Blocker", Status: domain.StatusOpen, Priority: domain.PriorityMedium,
	})
	if err != nil {
		t.Fatalf("CreateIssue(blocker) error = %v", err)
	}
	if _, err := bundle.RelationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: blocker.ID, TargetIssueID: target.ID, RelationType: domain.RelationTypeBlocks,
	}); err != nil {
		t.Fatalf("ManageIssueRelation() error = %v", err)
	}
	fakeClock.Advance(time.Second)
	if _, err := bundle.CommentService.AddComment(ctx, domain.AddCommentInput{
		IssueID: target.ID, Content: "snapshot comment",
	}); err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	fakeClock.Advance(time.Second)
	if _, err := bundle.DecisionService.RecordDecision(ctx, domain.RecordDecisionInput{
		IssueID: &target.ID, Title: "Single Snapshot", Summary: "Use readSnapshot", Content: "Full detail inside one tx", Status: domain.DecisionStatusActive,
	}); err != nil {
		t.Fatalf("RecordDecision() error = %v", err)
	}
	fakeClock.Advance(time.Second)
	if _, err := bundle.ReviewService.CreateReviewRequest(ctx, application.CreateReviewRequestInput{
		IssueID: target.ID, TargetIssueVersion: claim.Issue.Version, TargetEventID: latestEventIDForIssue(t, db, target.ID),
	}); err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}

	// Archived issue (ISSUE-341 parity).
	archivedTarget, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Archived Detail Issue", AcceptanceCriteria: &acceptance, Status: domain.StatusDone, Priority: domain.PriorityLow,
		Labels: []string{"legacy"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatalf("CreateIssue(archivedTarget) error = %v", err)
	}
	if _, err := bundle.CommentService.AddComment(ctx, domain.AddCommentInput{
		IssueID: archivedTarget.ID, Content: "comment before archive",
	}); err != nil {
		t.Fatalf("AddComment(archivedTarget) error = %v", err)
	}
	expiredID := "01ARZ3NDEKTSV4RRFFQ69G5FD1"
	if err := seedAttempt(t, db, expiredID, archivedTarget.ID, domain.AttemptStatusCompleted, now.Add(-time.Minute), now.Add(-2*time.Minute), &now, now); err != nil {
		t.Fatalf("seedAttempt(archivedTarget) error = %v", err)
	}
	releasedReason := domain.ReservationReleaseReasonCompleted
	if err := seedReservation(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FD2", archivedTarget.ID, expiredID, domain.ResourceKindFile, "archived_file.go", domain.ReservationStatusReleased, now, &now, &releasedReason); err != nil {
		t.Fatalf("seedReservation(archivedTarget) error = %v", err)
	}
	fakeClock.Advance(time.Second)
	if _, err := bundle.IssueService.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID: archivedTarget.ID, ExpectedVersion: archivedTarget.Issue.Version,
	}); err != nil {
		t.Fatalf("ArchiveIssue() error = %v", err)
	}

	composedDetailService, err := application.NewIssueDetailService(
		bundle.IssueService, bundle.GraphService, bundle.ActivityService, bundle.ReservationService, bundle.WorkflowPolicyService,
	)
	if err != nil {
		t.Fatalf("NewIssueDetailService(composed) error = %v", err)
	}

	for _, id := range []string{target.ID, target.DisplayID, archivedTarget.ID, archivedTarget.DisplayID} {
		composed, err := composedDetailService.GetIssueDetail(ctx, id)
		if err != nil {
			t.Fatalf("composed GetIssueDetail(%s) error = %v", id, err)
		}
		snapshot, err := bundle.IssueDetailService.GetIssueDetail(ctx, id)
		if err != nil {
			t.Fatalf("snapshot GetIssueDetail(%s) error = %v", id, err)
		}
		if !reflect.DeepEqual(snapshot, composed) {
			t.Fatalf("snapshot GetIssueDetail(%s) mismatch:\nsnapshot = %#v\ncomposed = %#v", id, snapshot, composed)
		}
	}

	archivedDetail, err := bundle.IssueDetailService.GetIssueDetail(ctx, archivedTarget.DisplayID)
	if err != nil {
		t.Fatalf("GetIssueDetail(archived) error = %v", err)
	}
	if archivedDetail.Graph.RootIssueID == nil || *archivedDetail.Graph.RootIssueID != archivedTarget.ID {
		t.Fatalf("archived Graph.RootIssueID = %#v, want %q", archivedDetail.Graph.RootIssueID, archivedTarget.ID)
	}
	if archivedDetail.Graph.Nodes == nil || len(archivedDetail.Graph.Nodes) != 0 || archivedDetail.Graph.Edges == nil || len(archivedDetail.Graph.Edges) != 0 {
		t.Fatalf("archived Graph = %#v, want non-nil empty nodes and edges", archivedDetail.Graph)
	}
	if archivedDetail.RootIssueProjection == nil || archivedDetail.RootIssueProjection.ArchivedAt == nil {
		t.Fatalf("archived RootIssueProjection = %#v, want populated projection with ArchivedAt", archivedDetail.RootIssueProjection)
	}
	if len(archivedDetail.Activity.Items) == 0 || len(archivedDetail.Reservations) != 1 || archivedDetail.Gates.RequirementCount != 1 {
		t.Fatalf("archived detail missing activity/reservations/gates: activity=%d res=%d gates=%#v",
			len(archivedDetail.Activity.Items), len(archivedDetail.Reservations), archivedDetail.Gates)
	}

	// Single-connection check proves zero nested transactions during detail snapshot reads.
	sqlite.SetPoolMaxOpenConnsForTest(db, 1)
	singleConnCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := bundle.IssueDetailService.GetIssueDetail(singleConnCtx, target.DisplayID); err != nil {
		t.Fatalf("GetIssueDetail(MaxOpenConns=1) error = %v", err)
	}
}

func TestBoardSnapshotRepositoryGetIssueDetailConsistentUnderConcurrentWriters(t *testing.T) {
	db, dbPath, now, fakeClock, bundle := openBoardSnapshotFixture(t)
	ctx := context.Background()

	target, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Concurrent Detail Issue", Status: domain.StatusReady, Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("CreateIssue(target) error = %v", err)
	}

	repo, err := sqlite.NewBoardSnapshotRepository(db)
	if err != nil {
		t.Fatalf("NewBoardSnapshotRepository() error = %v", err)
	}
	identifier, err := domain.ParseIssueIdentifier(target.ID)
	if err != nil {
		t.Fatalf("ParseIssueIdentifier() error = %v", err)
	}

	t.Run("uncommitted writer transaction is invisible", func(t *testing.T) {
		rawDB, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("sql.Open() error = %v", err)
		}
		defer rawDB.Close()
		conn, err := rawDB.Conn(ctx)
		if err != nil {
			t.Fatalf("rawDB.Conn() error = %v", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatalf("BEGIN error = %v", err)
		}
		defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()

		if _, err := conn.ExecContext(ctx, `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FE1', ?, 'uncommitted', 'summary', 'content', 'active', ?)`, target.ID, sqlite.FormatStorageTime(now)); err != nil {
			t.Fatalf("INSERT uncommitted decision: %v", err)
		}

		detail, err := repo.LoadIssueDetailSnapshot(ctx, ports.LoadIssueDetailSnapshotCommand{Identifier: identifier, Now: now})
		if err != nil {
			t.Fatalf("LoadIssueDetailSnapshot() error = %v", err)
		}
		if detail.LatestDecision != nil || len(detail.Activity.Items) != 1 {
			t.Fatalf("leaked uncommitted decision into detail: latestDecision=%#v activity=%d", detail.LatestDecision, len(detail.Activity.Items))
		}
	})

	t.Run("committed concurrent writer mid-snapshot produces zero torn sections", func(t *testing.T) {
		var committedDuringHook bool
		cleanup := sqlite.SetBoardSnapshotAfterFirstReadHookForTest(repo, func() {
			if committedDuringHook {
				return
			}
			committedDuringHook = true
			fakeClock.Advance(time.Second)
			if _, err := bundle.AttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
				IssueID: target.ID,
				Resources: []domain.Resource{
					{Kind: domain.ResourceKindFile, Path: "mid_snapshot.go"},
				},
			}); err != nil {
				t.Fatalf("concurrent ClaimIssue() error = %v", err)
			}
			blocker, err := bundle.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
				Type: domain.TypeTask, Title: "Mid-Snapshot Blocker", Status: domain.StatusOpen, Priority: domain.PriorityHigh,
			})
			if err != nil {
				t.Fatalf("concurrent CreateIssue() error = %v", err)
			}
			if _, err := bundle.RelationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
				Action: domain.RelationActionAdd, SourceIssueID: blocker.ID, TargetIssueID: target.ID, RelationType: domain.RelationTypeBlocks,
			}); err != nil {
				t.Fatalf("concurrent ManageIssueRelation() error = %v", err)
			}
			if _, err := bundle.DecisionService.RecordDecision(ctx, domain.RecordDecisionInput{
				IssueID: &target.ID, Title: "Mid-Snapshot Decision", Summary: "S", Content: "C", Status: domain.DecisionStatusActive,
			}); err != nil {
				t.Fatalf("concurrent RecordDecision() error = %v", err)
			}
		})
		defer cleanup()

		detail, err := repo.LoadIssueDetailSnapshot(ctx, ports.LoadIssueDetailSnapshotCommand{Identifier: identifier, Now: now})
		if err != nil {
			t.Fatalf("LoadIssueDetailSnapshot() error = %v", err)
		}
		if !committedDuringHook {
			t.Fatal("expected mid-snapshot hook to run")
		}
		if len(detail.Graph.Edges) != 0 || detail.LatestAttempt != nil || detail.LatestDecision != nil || len(detail.Reservations) != 0 || len(detail.Activity.Items) != 1 {
			t.Fatalf("torn issue detail snapshot observed: edges=%d attempt=%#v decision=%#v res=%d activity=%d",
				len(detail.Graph.Edges), detail.LatestAttempt, detail.LatestDecision, len(detail.Reservations), len(detail.Activity.Items))
		}

		later, err := repo.LoadIssueDetailSnapshot(ctx, ports.LoadIssueDetailSnapshotCommand{Identifier: identifier, Now: fakeClock.Now().UTC()})
		if err != nil {
			t.Fatalf("subsequent LoadIssueDetailSnapshot() error = %v", err)
		}
		if len(later.Graph.Edges) != 1 || later.LatestAttempt == nil || later.LatestDecision == nil || len(later.Reservations) != 1 {
			t.Fatalf("subsequent detail missing committed mutations: %#v", later)
		}
	})
}
