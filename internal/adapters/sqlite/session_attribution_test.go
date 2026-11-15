package sqlite_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

func insertTestAgentSession(t *testing.T, db *sqlite.DB, sessionID string, at time.Time) {
	t.Helper()
	ts := sqlite.FormatStorageTime(at)
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO agent_sessions(id, client_name, started_at, last_seen_at) VALUES (?, 'test-agent', ?, ?)`,
			sessionID, ts, ts)
		return err
	}); err != nil {
		t.Fatalf("insert agent_session %s: %v", sessionID, err)
	}
}

func readNullString(t *testing.T, db *sqlite.DB, query string, args ...any) sql.NullString {
	t.Helper()
	var value sql.NullString
	if err := db.Read(context.Background(), func(ctx context.Context, q sqlite.Queryer) error {
		return q.QueryRowContext(ctx, query, args...).Scan(&value)
	}); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

func assertNullStringEqual(t *testing.T, got sql.NullString, want string) {
	t.Helper()
	if !got.Valid || got.String != want {
		t.Fatalf("got %#v, want valid %q", got, want)
	}
}

func TestSQLiteIssueMutationsPersistSessionAttribution(t *testing.T) {
	const (
		sessionA = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
		sessionB = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	)
	issueService, db, now := openIssueService(t)
	ctx := context.Background()
	insertTestAgentSession(t, db, sessionA, now)
	insertTestAgentSession(t, db, sessionB, now)

	sa, sb := sessionA, sessionB
	createKey := "sqlite-create-1"
	created, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:           domain.TypeTask,
		Title:          "Attributed issue",
		SessionID:      &sa,
		IdempotencyKey: &createKey,
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if created.Issue.CreatedBySessionID == nil || *created.Issue.CreatedBySessionID != sessionA {
		t.Fatalf("Created.Issue.CreatedBySessionID = %#v, want %q", created.Issue.CreatedBySessionID, sessionA)
	}
	assertNullStringEqual(t, readNullString(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, created.Issue.ID), sessionA)
	assertNullStringEqual(t, readNullString(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_created'`, created.Issue.ID), sessionA)

	// Replay with a different session preserves original attribution and does not duplicate events.
	replayedCreate, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:           domain.TypeTask,
		Title:          "Attributed issue",
		SessionID:      &sb,
		IdempotencyKey: &createKey,
	})
	if err != nil {
		t.Fatalf("replay CreateIssue() error = %v", err)
	}
	if replayedCreate.Issue.CreatedBySessionID == nil || *replayedCreate.Issue.CreatedBySessionID != sessionA {
		t.Fatalf("replayed CreatedBySessionID = %#v, want %q", replayedCreate.Issue.CreatedBySessionID, sessionA)
	}

	updateKey := "sqlite-update-1"
	updated, err := issueService.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID:         created.Issue.ID,
		ExpectedVersion: 1,
		Changes:         domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "Updated title"}},
		SessionID:       &sb,
		IdempotencyKey:  &updateKey,
	})
	if err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	assertNullStringEqual(t, readNullString(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_updated' ORDER BY id DESC LIMIT 1`, created.Issue.ID), sessionB)

	if _, err := issueService.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID:         created.Issue.ID,
		ExpectedVersion: 1,
		Changes:         domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "Updated title"}},
		SessionID:       &sa,
		IdempotencyKey:  &updateKey,
	}); err != nil {
		t.Fatalf("replay UpdateIssue() error = %v", err)
	}
	var updateEventCount int
	if err := db.Read(ctx, func(ctx context.Context, q sqlite.Queryer) error {
		return q.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_events WHERE issue_id = ? AND event_type = 'issue_updated'`, created.Issue.ID).Scan(&updateEventCount)
	}); err != nil {
		t.Fatal(err)
	}
	if updateEventCount != 1 {
		t.Fatalf("updateEventCount = %d, want 1", updateEventCount)
	}

	archiveKey := "sqlite-archive-1"
	archived, err := issueService.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID:         created.Issue.ID,
		ExpectedVersion: updated.Issue.Version,
		SessionID:       &sb,
		IdempotencyKey:  &archiveKey,
	})
	if err != nil {
		t.Fatalf("ArchiveIssue() error = %v", err)
	}
	if archived.Issue.ArchivedBySessionID == nil || *archived.Issue.ArchivedBySessionID != sessionB {
		t.Fatalf("ArchivedBySessionID = %#v, want %q", archived.Issue.ArchivedBySessionID, sessionB)
	}
	assertNullStringEqual(t, readNullString(t, db, `SELECT archived_by_session_id FROM issues WHERE id = ?`, created.Issue.ID), sessionB)
	assertNullStringEqual(t, readNullString(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_archived'`, created.Issue.ID), sessionB)

	replayedArchive, err := issueService.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID:         created.Issue.ID,
		ExpectedVersion: updated.Issue.Version,
		SessionID:       &sa,
		IdempotencyKey:  &archiveKey,
	})
	if err != nil {
		t.Fatalf("replay ArchiveIssue() error = %v", err)
	}
	if replayedArchive.Issue.ArchivedBySessionID == nil || *replayedArchive.Issue.ArchivedBySessionID != sessionB {
		t.Fatalf("replayed ArchivedBySessionID = %#v, want %q", replayedArchive.Issue.ArchivedBySessionID, sessionB)
	}

	// Omitting session_id stores NULL cleanly.
	unattributed, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "No session"})
	if err != nil {
		t.Fatalf("unattributed CreateIssue() error = %v", err)
	}
	if unattributed.Issue.CreatedBySessionID != nil {
		t.Fatalf("unattributed CreatedBySessionID = %#v, want nil", unattributed.Issue.CreatedBySessionID)
	}
	if got := readNullString(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, unattributed.Issue.ID); got.Valid {
		t.Fatalf("unattributed issues.created_by_session_id = %#v, want NULL", got)
	}
}

func TestSQLiteManageRelationPersistsSessionAttribution(t *testing.T) {
	const (
		sessionA = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
		sessionB = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	)
	issueService, db, now := openIssueService(t)
	relationService := openRelationService(t, db, now)
	ctx := context.Background()
	insertTestAgentSession(t, db, sessionA, now)
	insertTestAgentSession(t, db, sessionB, now)

	first := createRelationTestIssue(t, issueService, "Source")
	second := createRelationTestIssue(t, issueService, "Target")
	sa, sb := sessionA, sessionB
	addKey := "sqlite-rel-add-1"
	added, err := relationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action:         domain.RelationActionAdd,
		SourceIssueID:  first.Issue.ID,
		TargetIssueID:  second.Issue.ID,
		RelationType:   domain.RelationTypeBlocks,
		SessionID:      &sa,
		IdempotencyKey: &addKey,
	})
	if err != nil {
		t.Fatalf("ManageIssueRelation(add) error = %v", err)
	}
	assertNullStringEqual(t, readNullString(t, db, `SELECT created_by_session_id FROM issue_relations WHERE id = ?`, added.Relation.ID), sessionA)
	for _, issueID := range []string{first.Issue.ID, second.Issue.ID} {
		assertNullStringEqual(t, readNullString(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'relation_added' ORDER BY id DESC LIMIT 1`, issueID), sessionA)
	}

	// Replay with different session preserves original relation and event count.
	if _, err := relationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action:         domain.RelationActionAdd,
		SourceIssueID:  first.Issue.ID,
		TargetIssueID:  second.Issue.ID,
		RelationType:   domain.RelationTypeBlocks,
		SessionID:      &sb,
		IdempotencyKey: &addKey,
	}); err != nil {
		t.Fatalf("replay ManageIssueRelation(add) error = %v", err)
	}

	if _, err := relationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action:        domain.RelationActionRemove,
		SourceIssueID: first.Issue.ID,
		TargetIssueID: second.Issue.ID,
		RelationType:  domain.RelationTypeBlocks,
		SessionID:     &sb,
	}); err != nil {
		t.Fatalf("ManageIssueRelation(remove) error = %v", err)
	}
	for _, issueID := range []string{first.Issue.ID, second.Issue.ID} {
		assertNullStringEqual(t, readNullString(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'relation_removed' ORDER BY id DESC LIMIT 1`, issueID), sessionB)
	}
}

func TestSQLiteApplyIssuePlanPersistsSessionAttribution(t *testing.T) {
	const (
		sessionA = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
		sessionB = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	)
	_, db, now := openIssueService(t)
	ctx := context.Background()
	insertTestAgentSession(t, db, sessionA, now)
	insertTestAgentSession(t, db, sessionB, now)

	source := clock.NewFakeClock(now)
	repository, err := sqlite.NewPlanningRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewPlanningService(repository, source, generator)
	if err != nil {
		t.Fatal(err)
	}

	sa, sb := sessionA, sessionB
	issueRef := "a"
	plan := domain.IssuePlan{
		Issues: []domain.PlannedIssue{
			{Ref: "a", Type: domain.TypeTask, Title: "Plan issue A"},
			{Ref: "b", Type: domain.TypeTask, Title: "Plan issue B"},
		},
		Relations: []domain.PlannedRelation{{SourceRef: "a", TargetRef: "b", Type: domain.RelationTypeBlocks}},
		Decisions: []domain.PlannedDecision{{IssueRef: &issueRef, Title: "Plan decision", Summary: "Summary", Content: "Content"}},
	}
	res, err := service.ApplyIssuePlan(ctx, plan, "plan-session-1", &sa)
	if err != nil {
		t.Fatalf("ApplyIssuePlan() error = %v", err)
	}
	for _, created := range res.CreatedIssues {
		if created.Issue.CreatedBySessionID == nil || *created.Issue.CreatedBySessionID != sessionA {
			t.Fatalf("CreatedIssue[%s].CreatedBySessionID = %#v, want %q", created.Ref, created.Issue.CreatedBySessionID, sessionA)
		}
		assertNullStringEqual(t, readNullString(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, created.Issue.ID), sessionA)
		assertNullStringEqual(t, readNullString(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_created'`, created.Issue.ID), sessionA)
		assertNullStringEqual(t, readNullString(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'relation_added'`, created.Issue.ID), sessionA)
	}
	assertNullStringEqual(t, readNullString(t, db, `SELECT created_by_session_id FROM issue_relations WHERE id = ?`, res.CreatedRelations[0].ID), sessionA)
	if len(res.CreatedDecisions) != 1 || res.CreatedDecisions[0].CreatedBySessionID == nil || *res.CreatedDecisions[0].CreatedBySessionID != sessionA {
		t.Fatalf("CreatedDecisions = %#v, want CreatedBySessionID %q", res.CreatedDecisions, sessionA)
	}
	assertNullStringEqual(t, readNullString(t, db, `SELECT created_by_session_id FROM decisions WHERE id = ?`, res.CreatedDecisions[0].ID), sessionA)
	assertNullStringEqual(t, readNullString(t, db, `SELECT session_id FROM issue_events WHERE event_type = 'decision_recorded' AND issue_id = ?`, res.CreatedIssues[0].Issue.ID), sessionA)

	// Idempotency replay with a different session keeps original attribution.
	replay, err := service.ApplyIssuePlan(ctx, plan, "plan-session-1", &sb)
	if err != nil {
		t.Fatalf("replay ApplyIssuePlan() error = %v", err)
	}
	if replay.CreatedIssues[0].Issue.CreatedBySessionID == nil || *replay.CreatedIssues[0].Issue.CreatedBySessionID != sessionA {
		t.Fatalf("replay issue attribution = %#v, want %q", replay.CreatedIssues[0].Issue.CreatedBySessionID, sessionA)
	}
	if replay.CreatedDecisions[0].CreatedBySessionID == nil || *replay.CreatedDecisions[0].CreatedBySessionID != sessionA {
		t.Fatalf("replay decision attribution = %#v, want %q", replay.CreatedDecisions[0].CreatedBySessionID, sessionA)
	}

	// Applying a plan without a session stores NULL across created entities and events.
	nilRef := "u"
	nilPlan := domain.IssuePlan{
		Issues:    []domain.PlannedIssue{{Ref: "u", Type: domain.TypeTask, Title: "Unattributed Plan Issue"}},
		Decisions: []domain.PlannedDecision{{IssueRef: &nilRef, Title: "Unattributed Decision", Summary: "S", Content: "C"}},
	}
	nilRes, err := service.ApplyIssuePlan(ctx, nilPlan, "plan-session-nil")
	if err != nil {
		t.Fatalf("nil-session ApplyIssuePlan() error = %v", err)
	}
	if nilRes.CreatedIssues[0].Issue.CreatedBySessionID != nil || nilRes.CreatedDecisions[0].CreatedBySessionID != nil {
		t.Fatalf("nil-session ApplyIssuePlan() returned non-nil attribution: %#v / %#v", nilRes.CreatedIssues[0].Issue.CreatedBySessionID, nilRes.CreatedDecisions[0].CreatedBySessionID)
	}
	if got := readNullString(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, nilRes.CreatedIssues[0].Issue.ID); got.Valid {
		t.Fatalf("nil-session issues.created_by_session_id = %#v, want NULL", got)
	}
	if got := readNullString(t, db, `SELECT created_by_session_id FROM decisions WHERE id = ?`, nilRes.CreatedDecisions[0].ID); got.Valid {
		t.Fatalf("nil-session decisions.created_by_session_id = %#v, want NULL", got)
	}
}

func TestSQLiteReviewEventsPersistSessionAttribution(t *testing.T) {
	const (
		sessionA = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
		sessionB = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	)
	fixture := newReviewFixture(t, "review-session-attr")
	defer fixture.close()

	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	insertTestAgentSession(t, fixture.db, sessionA, now)
	insertTestAgentSession(t, fixture.db, sessionB, now)
	issueID := fixture.insertIssue(t, "Review session target")
	attemptID := fixture.insertReviewAttempt(t, issueID)

	sa, sb := sessionA, sessionB
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      0,
		ArtifactIDs:        []string{"a-1"},
		Purposes:           []string{"implementation"},
		SessionID:          &sa,
		OccurredAt:         now,
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}
	assertNullStringEqual(t, readNullString(t, fixture.db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_requested' ORDER BY id DESC LIMIT 1`, issueID), sessionA)

	// Advance issue version so replace target differs cleanly, while checking review_superseded + review_requested attribution.
	replaced, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		PredecessorRequestID:       created.Request.ID,
		PredecessorExpectedVersion: created.Request.Version,
		SuccessorID:                fixture.newID(t),
		SuccessorTargetID:          fixture.newID(t),
		TargetIssueVersion:         1,
		TargetEventID:              1,
		ArtifactIDs:                []string{"a-2"},
		Purposes:                   []string{"implementation"},
		SessionID:                  &sb,
		OccurredAt:                 now.Add(time.Minute),
		IdempotencyKey:             "sqlite-replace-1",
		RequestHash:                []byte("hash-1"),
	})
	if err != nil {
		t.Fatalf("ReplaceReviewRequest() error = %v", err)
	}
	assertNullStringEqual(t, readNullString(t, fixture.db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_superseded' ORDER BY id DESC LIMIT 1`, issueID), sessionB)
	assertNullStringEqual(t, readNullString(t, fixture.db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_requested' ORDER BY id DESC LIMIT 1`, issueID), sessionB)

	claimed, err := fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       replaced.Successor.ID,
		ExpectedVersion: replaced.Successor.Version,
		ActiveAttemptID: &attemptID,
		SessionID:       &sa,
		OccurredAt:      now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ClaimReviewRequest() error = %v", err)
	}
	assertNullStringEqual(t, readNullString(t, fixture.db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_claimed' ORDER BY id DESC LIMIT 1`, issueID), sessionA)

	// Attempt-driven resolution preserves NULL session_id.
	if _, err := fixture.repository.ResolveReviewRequest(fixture.ctx, ports.ResolveReviewRequestCommand{
		OutcomeID:       fixture.newID(t),
		RequestID:       claimed.Request.ID,
		ExpectedVersion: claimed.Request.Version,
		AttemptID:       attemptID,
		Outcome:         domain.ReviewOutcomeApproved,
		OccurredAt:      now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("ResolveReviewRequest() error = %v", err)
	}
	if resolvedSession := readNullString(t, fixture.db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_approved' ORDER BY id DESC LIMIT 1`, issueID); resolvedSession.Valid {
		t.Fatalf("review_approved.session_id = %#v, want NULL", resolvedSession)
	}

	// Open second request and cancel with sessionB.
	second, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      4,
		ArtifactIDs:        []string{"a-3"},
		Purposes:           []string{"implementation"},
		SessionID:          &sa,
		OccurredAt:         now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest(second) error = %v", err)
	}
	if _, err := fixture.repository.CancelReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       second.Request.ID,
		ExpectedVersion: second.Request.Version,
		SessionID:       &sb,
		OccurredAt:      now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("CancelReviewRequest() error = %v", err)
	}
	assertNullStringEqual(t, readNullString(t, fixture.db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_cancelled' ORDER BY id DESC LIMIT 1`, issueID), sessionB)
}
