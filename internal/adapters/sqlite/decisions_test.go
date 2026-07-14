package sqlite_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
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

const (
	decisionTestID                     = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	decisionReplacementID              = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	decisionConcurrentID               = "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	decisionConcurrentID2              = "01ARZ3NDEKTSV4RRFFQ69G5FB0"
	decisionSessionTestID              = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	decisionReopenSessionID            = "01ARZ3NDEKTSV4RRFFQ69G5FB2"
	decisionReopenReplacementSessionID = "01ARZ3NDEKTSV4RRFFQ69G5FB3"
)

func TestNewDecisionRepositoryRejectsNilDatabase(t *testing.T) {
	_, err := sqlite.NewDecisionRepository(nil)
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
}

func TestDecisionRepositoryPersistsScopesEventAndSupersession(t *testing.T) {
	issues, db, now := openIssueService(t)
	repository, err := sqlite.NewDecisionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Decision issue"})
	if err != nil {
		t.Fatal(err)
	}
	seedCommentSession(t, db, decisionSessionTestID, now)
	ctx := context.Background()
	first, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID:         decisionTestID,
		Input:      domain.RecordDecisionInput{IssueID: stringPtr(issue.DisplayID), Title: "Choice", Summary: "Summary", Content: "  exact  ", SessionID: stringPtr(decisionSessionTestID)},
		OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID: decisionConcurrentID, Input: domain.RecordDecisionInput{Title: "Project", Summary: "Project summary"}, OccurredAt: now,
	})
	if err != nil || project.Decision.IssueID != nil {
		t.Fatalf("project result=%#v error=%v", project, err)
	}
	replacement, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID:         decisionReplacementID,
		Input:      domain.RecordDecisionInput{IssueID: stringPtr(issue.ID), Title: "Choice 2", Summary: "Summary 2", SupersedesID: stringPtr(first.Decision.ID), SessionID: stringPtr(decisionSessionTestID)},
		OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.SupersededDecisionID == nil || *replacement.SupersededDecisionID != decisionTestID ||
		replacement.Decision.SupersedesID == nil || *replacement.Decision.SupersedesID != decisionTestID {
		t.Fatalf("replacement=%#v", replacement)
	}
	var status, payload, eventSession string
	var eventIssue, attemptID sql.NullString
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT status FROM decisions WHERE id = ?", decisionTestID).Scan(&status); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT issue_id, session_id, attempt_id, payload FROM issue_events
			WHERE event_type = 'decision_recorded' AND issue_id = ? ORDER BY id DESC LIMIT 1`, issue.ID).
			Scan(&eventIssue, &eventSession, &attemptID, &payload)
	}); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatal(err)
	}
	if status != "superseded" || !eventIssue.Valid || eventIssue.String != issue.ID || eventSession != decisionSessionTestID ||
		attemptID.Valid || event["decision_id"] != decisionReplacementID || event["status"] != "active" ||
		event["supersedes_id"] != decisionTestID || len(event) != 3 || replacement.Decision.Content != "" {
		t.Fatalf("status=%q event=%q issue=%q session=%q attempt=%q result=%#v", status, payload, eventIssue.String, eventSession, attemptID.String, replacement)
	}
}

func TestDecisionRepositoryRejectsPredecessorErrorsAtomically(t *testing.T) {
	issues, db, now := openIssueService(t)
	repository, err := sqlite.NewDecisionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Scope"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = repository.RecordDecision(ctx, ports.RecordDecisionCommand{ID: decisionTestID, Input: domain.RecordDecisionInput{
		IssueID: stringPtr(issue.ID), Title: "Original", Summary: "Summary",
	}, OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		id   string
		in   domain.RecordDecisionInput
		code string
	}{
		{"missing", decisionReplacementID, domain.RecordDecisionInput{Title: "x", Summary: "s", SupersedesID: stringPtr(decisionConcurrentID2)}, "NOT_FOUND"},
		{"scope", decisionConcurrentID, domain.RecordDecisionInput{Title: "x", Summary: "s", SupersedesID: stringPtr(decisionTestID)}, "SCOPE_MISMATCH"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{ID: test.id, Input: test.in, OccurredAt: now})
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument || len(domainErr.Details) != 1 || domainErr.Details[0].Code != test.code {
				t.Fatalf("error=%v", err)
			}
			var count int
			if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
				return query.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&count)
			}); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("decision count=%d", count)
			}
		})
	}
}

func TestDecisionRepositoryConcurrentSupersessionHasOneWinner(t *testing.T) {
	issues, db, now := openIssueService(t)
	repository, err := sqlite.NewDecisionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Race"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.RecordDecision(context.Background(), ports.RecordDecisionCommand{
		ID: decisionTestID, Input: domain.RecordDecisionInput{IssueID: stringPtr(issue.ID), Title: "Original", Summary: "Summary"}, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct{ err error }
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, id := range []string{decisionReplacementID, decisionConcurrentID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := repository.RecordDecision(context.Background(), ports.RecordDecisionCommand{
				ID: id, Input: domain.RecordDecisionInput{IssueID: stringPtr(issue.ID), Title: "Replacement", Summary: "Summary", SupersedesID: stringPtr(decisionTestID)}, OccurredAt: now,
			})
			results <- outcome{err: err}
		}(id)
	}
	wg.Wait()
	close(results)
	successes, failures := 0, 0
	for result := range results {
		if result.err == nil {
			successes++
		} else {
			failures++
			if !errors.Is(result.err, &domain.Error{Code: domain.CodeInvalidArgument}) {
				t.Fatalf("race error=%v", result.err)
			}
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("successes=%d failures=%d", successes, failures)
	}
	var decisions, events, active, superseded int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&decisions); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'decision_recorded'").Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions WHERE status = 'active'").Scan(&active); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM decisions WHERE status = 'superseded'").Scan(&superseded)
	}); err != nil {
		t.Fatal(err)
	}
	if decisions != 2 || events != 2 || active != 1 || superseded != 1 {
		t.Fatalf("decisions=%d events=%d active=%d superseded=%d", decisions, events, active, superseded)
	}
}

func TestDecisionRepositoryUnknownSessionRollsBack(t *testing.T) {
	issues, db, now := openIssueService(t)
	repository, err := sqlite.NewDecisionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "FK"})
	if err != nil {
		t.Fatal(err)
	}
	unknown := "01ARZ3NDEKTSV4RRFFQ69G5FB1"
	_, err = repository.RecordDecision(context.Background(), ports.RecordDecisionCommand{
		ID: decisionTestID, Input: domain.RecordDecisionInput{IssueID: stringPtr(issue.ID), Title: "x", Summary: "s", SessionID: &unknown}, OccurredAt: now,
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeStorageConstraint}) {
		t.Fatalf("error=%v", err)
	}
	var decisions, events int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&decisions); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'decision_recorded'").Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 || events != 0 {
		t.Fatalf("rollback decisions=%d events=%d", decisions, events)
	}
}

func TestDecisionRepositoryRejectsNonActivePredecessorWithoutMutation(t *testing.T) {
	issues, db, now := openIssueService(t)
	repository, err := sqlite.NewDecisionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Non-active predecessor"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID:         decisionTestID,
		Input:      domain.RecordDecisionInput{IssueID: stringPtr(issue.ID), Title: "Original", Summary: "Original summary"},
		OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID: decisionReplacementID,
		Input: domain.RecordDecisionInput{
			IssueID: stringPtr(issue.ID), Title: "Replacement", Summary: "Replacement summary",
			SupersedesID: stringPtr(decisionTestID),
		},
		OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID: decisionConcurrentID,
		Input: domain.RecordDecisionInput{
			IssueID: stringPtr(issue.ID), Title: "Second replacement", Summary: "Second summary",
			SupersedesID: stringPtr(decisionTestID),
		},
		OccurredAt: now,
	})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument ||
		len(domainErr.Details) != 1 || domainErr.Details[0].Field != "supersedes_id" ||
		domainErr.Details[0].Code != "NOT_ACTIVE" {
		t.Fatalf("non-active predecessor error = %v", err)
	}

	var decisions, events, active, superseded int
	var predecessorStatus, replacementStatus string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&decisions); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'decision_recorded'").Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions WHERE status = 'active'").Scan(&active); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions WHERE status = 'superseded'").Scan(&superseded); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT status FROM decisions WHERE id = ?", decisionTestID).Scan(&predecessorStatus); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT status FROM decisions WHERE id = ?", decisionReplacementID).Scan(&replacementStatus)
	}); err != nil {
		t.Fatal(err)
	}
	if decisions != 2 || events != 2 || active != 1 || superseded != 1 ||
		predecessorStatus != string(domain.DecisionStatusSuperseded) ||
		replacementStatus != string(domain.DecisionStatusActive) {
		t.Fatalf("non-active predecessor mutation: decisions=%d events=%d active=%d superseded=%d predecessor=%q replacement=%q",
			decisions, events, active, superseded, predecessorStatus, replacementStatus)
	}
}

func TestDecisionRepositoryRejectsArchivedAndMissingIssueWithoutWrites(t *testing.T) {
	t.Run("archived", func(t *testing.T) {
		issues, db, now := openIssueService(t)
		repository, err := sqlite.NewDecisionRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Archived"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := issues.ArchiveIssue(context.Background(), domain.ArchiveIssueInput{
			IssueID: issue.ID, ExpectedVersion: issue.Issue.Version,
		}); err != nil {
			t.Fatal(err)
		}
		_, err = repository.RecordDecision(context.Background(), ports.RecordDecisionCommand{
			ID:         decisionTestID,
			Input:      domain.RecordDecisionInput{IssueID: stringPtr(issue.ID), Title: "No decision", Summary: "Archived"},
			OccurredAt: now,
		})
		assertDomainCode(t, err, domain.CodeIssueArchived)
		assertDecisionCounts(t, db, 0, 0)
	})

	t.Run("missing", func(t *testing.T) {
		_, db, now := openIssueService(t)
		repository, err := sqlite.NewDecisionRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		_, err = repository.RecordDecision(context.Background(), ports.RecordDecisionCommand{
			ID:         decisionTestID,
			Input:      domain.RecordDecisionInput{IssueID: stringPtr("ISSUE-999"), Title: "No decision", Summary: "Missing"},
			OccurredAt: now,
		})
		assertDomainCode(t, err, domain.CodeIssueNotFound)
		assertDecisionCounts(t, db, 0, 0)
	})
}

func TestDecisionRepositoryRollsBackSupersessionWhenEventAppendFails(t *testing.T) {
	issues, db, now := openIssueService(t)
	repository, err := sqlite.NewDecisionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "Event failure"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID:         decisionTestID,
		Input:      domain.RecordDecisionInput{IssueID: stringPtr(issue.ID), Title: "Original", Summary: "Summary"},
		OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_decision_recorded_event
			BEFORE INSERT ON issue_events
			WHEN NEW.event_type = 'decision_recorded'
			BEGIN SELECT RAISE(ABORT, 'rejected decision event'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, "DROP TRIGGER reject_decision_recorded_event")
			return err
		}); err != nil {
			t.Errorf("drop event trigger: %v", err)
		}
	}()

	_, err = repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID: decisionReplacementID,
		Input: domain.RecordDecisionInput{
			IssueID: stringPtr(issue.ID), Title: "Replacement", Summary: "Replacement summary",
			SupersedesID: stringPtr(decisionTestID),
		},
		OccurredAt: now,
	})
	if err == nil {
		t.Fatal("event failure returned nil error")
	}

	var predecessorStatus string
	var replacementRows, decisions, decisionEvents int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT status FROM decisions WHERE id = ?", decisionTestID).Scan(&predecessorStatus); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions WHERE id = ?", decisionReplacementID).Scan(&replacementRows); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&decisions); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'decision_recorded'").Scan(&decisionEvents)
	}); err != nil {
		t.Fatal(err)
	}
	if predecessorStatus != string(domain.DecisionStatusActive) || replacementRows != 0 ||
		decisions != 1 || decisionEvents != 1 {
		t.Fatalf("supersession rollback: predecessor=%q replacement rows=%d decisions=%d decision events=%d",
			predecessorStatus, replacementRows, decisions, decisionEvents)
	}
}

func TestDecisionRepositoryPersistsSupersessionAcrossReopen(t *testing.T) {
	ctx := context.Background()
	localZone := time.FixedZone("test", 2*60*60)
	predecessorAt := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, localZone)
	replacementAt := predecessorAt.Add(90 * time.Second)
	fakeClock := clock.NewFakeClock(predecessorAt.UTC())
	path := filepath.Join(t.TempDir(), "decisions.db")
	var db *sqlite.DB
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(context.Background()); err != nil {
				t.Errorf("reopened database close: %v", err)
			}
		}
	})

	var err error
	db, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		timestamp := predecessorAt.UTC().Format(time.RFC3339Nano)
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at)
			VALUES (?, 1, ?, ?)`, sqliteTestProjectID, timestamp, timestamp)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := application.NewIssueService(issueRepository, fakeClock, generator)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Decision persistence"})
	if err != nil {
		t.Fatal(err)
	}
	seedCommentSession(t, db, decisionReopenSessionID, predecessorAt.UTC())
	seedCommentSession(t, db, decisionReopenReplacementSessionID, predecessorAt.UTC())
	repository, err := sqlite.NewDecisionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	predecessorContent := "\n  predecessor content  \n"
	replacementContent := "\n\treplacement content  \n"
	if _, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID: decisionTestID,
		Input: domain.RecordDecisionInput{
			IssueID: stringPtr(issue.DisplayID), Title: "Predecessor title", Summary: "Predecessor summary",
			Content: predecessorContent, SessionID: stringPtr(decisionReopenSessionID),
		},
		OccurredAt: predecessorAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID: decisionReplacementID,
		Input: domain.RecordDecisionInput{
			IssueID: stringPtr(issue.ID), Title: "Replacement title", Summary: "Replacement summary",
			Content: replacementContent, SupersedesID: stringPtr(decisionTestID),
			SessionID: stringPtr(decisionReopenReplacementSessionID),
		},
		OccurredAt: replacementAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(ctx); err != nil {
		db = nil
		t.Fatal(err)
	}
	db = nil

	db, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatal(err)
	}

	type storedDecision struct {
		id, issueID, title, summary, content, status, supersedesID, sessionID, createdAt string
	}
	type storedEvent struct {
		issueID, eventType, sessionID, payload, createdAt string
		attemptID                                         sql.NullString
	}
	var decisions []storedDecision
	var events []storedEvent
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		rows, err := query.QueryContext(ctx, `SELECT id, issue_id, title, summary, content, status,
			supersedes_id, created_by_session_id, created_at FROM decisions ORDER BY created_at, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var decision storedDecision
			var issueID, supersedesID, sessionID sql.NullString
			if err := rows.Scan(&decision.id, &issueID, &decision.title, &decision.summary, &decision.content,
				&decision.status, &supersedesID, &sessionID, &decision.createdAt); err != nil {
				return err
			}
			decision.issueID, decision.supersedesID, decision.sessionID = issueID.String, supersedesID.String, sessionID.String
			decisions = append(decisions, decision)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		eventRows, err := query.QueryContext(ctx, `SELECT issue_id, event_type, session_id, attempt_id, payload, created_at
			FROM issue_events WHERE event_type = 'decision_recorded' ORDER BY id`)
		if err != nil {
			return err
		}
		defer eventRows.Close()
		for eventRows.Next() {
			var event storedEvent
			var sessionID sql.NullString
			if err := eventRows.Scan(&event.issueID, &event.eventType, &sessionID, &event.attemptID,
				&event.payload, &event.createdAt); err != nil {
				return err
			}
			event.sessionID = sessionID.String
			events = append(events, event)
		}
		return eventRows.Err()
	}); err != nil {
		t.Fatal(err)
	}

	if len(decisions) != 2 {
		t.Fatalf("persisted decisions = %d, want 2", len(decisions))
	}
	wantPredecessorAt := predecessorAt.UTC().Format(time.RFC3339Nano)
	wantReplacementAt := replacementAt.UTC().Format(time.RFC3339Nano)
	if decisions[0] != (storedDecision{
		id: decisionTestID, issueID: issue.ID, title: "Predecessor title", summary: "Predecessor summary",
		content: predecessorContent, status: string(domain.DecisionStatusSuperseded),
		sessionID: decisionReopenSessionID, createdAt: wantPredecessorAt,
	}) || decisions[1] != (storedDecision{
		id: decisionReplacementID, issueID: issue.ID, title: "Replacement title", summary: "Replacement summary",
		content: replacementContent, status: string(domain.DecisionStatusActive),
		supersedesID: decisionTestID, sessionID: decisionReopenReplacementSessionID, createdAt: wantReplacementAt,
	}) {
		t.Fatalf("persisted decisions = %#v", decisions)
	}
	if len(events) != 2 {
		t.Fatalf("persisted decision events = %d, want 2", len(events))
	}
	wantPayloads := []string{
		`{"decision_id":"` + decisionTestID + `","status":"active"}`,
		`{"decision_id":"` + decisionReplacementID + `","status":"active","supersedes_id":"` + decisionTestID + `"}`,
	}
	wantSessions := []string{decisionReopenSessionID, decisionReopenReplacementSessionID}
	wantTimes := []string{wantPredecessorAt, wantReplacementAt}
	for index, event := range events {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(event.payload), &payload); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"title", "summary", "content"} {
			if _, exists := payload[forbidden]; exists {
				t.Fatalf("event %d payload contains %q: %s", index, forbidden, event.payload)
			}
		}
		if event.issueID != issue.ID || event.eventType != "decision_recorded" ||
			event.sessionID != wantSessions[index] || event.attemptID.Valid ||
			event.payload != wantPayloads[index] || event.createdAt != wantTimes[index] {
			t.Fatalf("persisted event %d = %#v", index, event)
		}
	}
}

func assertDecisionCounts(t *testing.T, db *sqlite.DB, wantDecisions, wantEvents int) {
	t.Helper()
	var decisions, events int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&decisions); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'decision_recorded'").Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if decisions != wantDecisions || events != wantEvents {
		t.Fatalf("decision state = decisions=%d events=%d, want decisions=%d events=%d",
			decisions, events, wantDecisions, wantEvents)
	}
}

func stringPtr(value string) *string { return &value }
