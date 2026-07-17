package sqlite_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
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
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

func TestClaimIssueIdempotentReplayAndConflict(t *testing.T) {
	fixture := newAttemptTestFixture(t, "claim-idempotency")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "claim retry", domain.StatusReady)
	key := "claim-retry"
	input := domain.ClaimIssueInput{IssueID: issue.ID, IdempotencyKey: &key}
	first, err := fixture.attempts.ClaimIssue(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.attempts.ClaimIssue(fixture.ctx, input)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !reflect.DeepEqual(first.Attempt, second.Attempt) || first.LeaseToken != second.LeaseToken {
		t.Fatalf("replay = %#v, want %#v", second, first)
	}
	changed := input
	changed.LeaseSeconds = intPointer(60)
	if _, err := fixture.attempts.ClaimIssue(fixture.ctx, changed); !errors.Is(err, &domain.Error{Code: domain.CodeIdempotencyConflict}) {
		t.Fatalf("conflict = %v", err)
	}
	var attempts, events, records int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM work_attempts WHERE issue_id = ?`, issue.ID).Scan(&attempts); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE issue_id = ? AND event_type = 'attempt_started'`, issue.ID).Scan(&events); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records WHERE operation = 'claim_issue' AND idempotency_key = ?`, key).Scan(&records)
	}); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || events != 1 || records != 1 {
		t.Fatalf("durable state = attempts %d events %d records %d", attempts, events, records)
	}
}

func TestFinishAttemptIdempotentReplayAndConflict(t *testing.T) {
	fixture := newAttemptTestFixture(t, "finish-idempotency")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "finish retry", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	key := "finish-retry"
	input := finishInput(claim, domain.AttemptOutcomeCompleted)
	input.TargetIssueStatus = statusPointer(domain.StatusDone)
	input.IdempotencyKey = &key
	input.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeFile, URI: "result.txt"}}
	first, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("replay = %#v, %v; first = %#v", second, err, first)
	}
	changed := input
	changed.ResultSummary = "changed"
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, changed); !errors.Is(err, &domain.Error{Code: domain.CodeIdempotencyConflict}) {
		t.Fatalf("conflict = %v", err)
	}
	var events, artifacts, records int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE attempt_id = ? AND event_type = 'attempt_completed'`, claim.Attempt.ID).Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifacts); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records WHERE operation = 'finish_attempt' AND idempotency_key = ?`, key).Scan(&records)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 1 || artifacts != 1 || records != 1 {
		t.Fatalf("durable retry state = events %d artifacts %d records %d", events, artifacts, records)
	}
}

func TestFinishAttemptIdempotentReplaySurvivesReopen(t *testing.T) {
	fixture := newAttemptTestFixture(t, "finish-reopen")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "finish reopen", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	key := "finish-reopen-key"
	input := finishInput(claim, domain.AttemptOutcomeCompleted)
	input.TargetIssueStatus = statusPointer(domain.StatusDone)
	input.IdempotencyKey = &key
	input.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeFile, URI: "reopen.txt"}}
	first, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	reopenAttemptTestFixture(t, fixture)
	second, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("reopen replay = %#v, %v; first = %#v", second, err, first)
	}

	var events, artifacts, records int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events
			WHERE attempt_id = ? AND event_type = 'attempt_completed'`, claim.Attempt.ID).Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifacts); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records
			WHERE operation = 'finish_attempt' AND idempotency_key = ?`, key).Scan(&records)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 1 || artifacts != 1 || records != 1 {
		t.Fatalf("reopen durable state = events %d artifacts %d records %d", events, artifacts, records)
	}
}

func TestFinishAttemptWithoutKeyRemainsNonIdempotent(t *testing.T) {
	fixture := newAttemptTestFixture(t, "finish-no-key")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "finish without key", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	input := finishInput(claim, domain.AttemptOutcomeCompleted)
	input.TargetIssueStatus = statusPointer(domain.StatusDone)
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); !errors.Is(err, &domain.Error{Code: domain.CodeAttemptNotActive}) {
		t.Fatalf("duplicate no-key finish = %v", err)
	}

	var events, artifacts, records int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events
			WHERE attempt_id = ? AND event_type = 'attempt_completed'`, claim.Attempt.ID).Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifacts); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records
			WHERE operation = 'finish_attempt'`).Scan(&records)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 1 || artifacts != 0 || records != 0 {
		t.Fatalf("no-key durable state = events %d artifacts %d records %d", events, artifacts, records)
	}
}

func TestFinishAttemptConcurrentSameKeyReplay(t *testing.T) {
	fixture := newAttemptTestFixture(t, "finish-concurrent-replay")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "finish concurrent replay", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	key := "finish-concurrent-key"
	input := finishInput(claim, domain.AttemptOutcomeCompleted)
	input.TargetIssueStatus = statusPointer(domain.StatusDone)
	input.IdempotencyKey = &key
	input.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeFile, URI: "concurrent.txt"}}

	start := make(chan struct{})
	results := make(chan struct {
		result ports.FinishAttemptResult
		err    error
	}, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := fixture.attempts.FinishAttempt(context.Background(), input)
			results <- struct {
				result ports.FinishAttemptResult
				err    error
			}{result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var first ports.FinishAttemptResult
	for index := 0; index < 2; index++ {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("concurrent finish %d = %v", index, outcome.err)
		}
		if index == 0 {
			first = outcome.result
		} else if !reflect.DeepEqual(first, outcome.result) {
			t.Fatalf("concurrent replay = %#v; first = %#v", outcome.result, first)
		}
	}

	var events, records, artifacts int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events
			WHERE attempt_id = ? AND event_type = 'attempt_completed'`, claim.Attempt.ID).Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records
			WHERE operation = 'finish_attempt' AND idempotency_key = ?`, key).Scan(&records); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifacts)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 1 || records != 1 || artifacts != 1 {
		t.Fatalf("concurrent durable state = events %d records %d artifacts %d", events, records, artifacts)
	}
}

func TestFinishAttemptIdempotentReplayAcrossSessionReconnect(t *testing.T) {
	fixture := newAttemptTestFixture(t, "finish-session-reconnect")
	defer fixture.close()

	sessionA := "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	sessionB := "01BX5ZZKBKACTAV9WEVGEMMVS0"
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		timestamp := fixture.clock.Now().Format(time.RFC3339Nano)
		for _, id := range []string{sessionA, sessionB} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(
				id, client_name, started_at, last_seen_at) VALUES (?, 'test', ?, ?)`,
				id, timestamp, timestamp); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	issue := createAttemptIssue(t, fixture, "finish session reconnect", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{
		IssueID: issue.ID, SessionID: &sessionA,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "finish-session-key"
	firstInput := finishInput(claim, domain.AttemptOutcomeCompleted)
	firstInput.SessionID = &sessionA
	firstInput.TargetIssueStatus = statusPointer(domain.StatusDone)
	firstInput.IdempotencyKey = &key
	firstInput.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeFile, URI: "session.txt"}}
	first, err := fixture.attempts.FinishAttempt(fixture.ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}

	retryInput := firstInput
	retryInput.SessionID = &sessionB
	second, err := fixture.attempts.FinishAttempt(fixture.ctx, retryInput)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("session reconnect replay = %#v, %v; first = %#v", second, err, first)
	}

	var events int
	var eventSession sql.NullString
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events
			WHERE attempt_id = ? AND event_type = 'attempt_completed'`, claim.Attempt.ID).Scan(&events); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT session_id FROM issue_events
			WHERE attempt_id = ? AND event_type = 'attempt_completed'`, claim.Attempt.ID).Scan(&eventSession)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 1 || !eventSession.Valid || eventSession.String != sessionA {
		t.Fatalf("session reconnect event = count %d session %#v", events, eventSession)
	}
}

func TestFinishAttemptCorruptStoredResponses(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		ignoreCheck bool
	}{
		{name: "invalid-json", response: "not-json", ignoreCheck: true},
		{name: "valid-invalid-shape", response: "{}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttemptTestFixture(t, "finish-corrupt-"+test.name)
			defer fixture.close()

			issue := createAttemptIssue(t, fixture, "finish corrupt "+test.name, domain.StatusReady)
			claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
			if err != nil {
				t.Fatal(err)
			}
			key := "finish-corrupt-" + test.name
			input := finishInput(claim, domain.AttemptOutcomeCompleted)
			input.TargetIssueStatus = statusPointer(domain.StatusDone)
			input.IdempotencyKey = &key
			if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
				if test.ignoreCheck {
					if _, err := tx.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
						return err
					}
					defer tx.ExecContext(ctx, `PRAGMA ignore_check_constraints = OFF`)
				}
				_, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET response_json = ?
					WHERE operation = 'finish_attempt' AND idempotency_key = ?`, test.response, key)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, err = fixture.attempts.FinishAttempt(fixture.ctx, input)
			if !errors.Is(err, &domain.Error{Code: domain.CodeStorageCorrupt}) {
				t.Fatalf("corrupt response error = %v", err)
			}
		})
	}
}

func TestFinishAttemptIdempotencyInsertFailureRollsBack(t *testing.T) {
	fixture := newAttemptTestFixture(t, "finish-idempotency-rollback")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "finish idempotency rollback", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	key := "finish-idempotency-rollback-key"
	input := finishInput(claim, domain.AttemptOutcomeCompleted)
	input.TargetIssueStatus = statusPointer(domain.StatusDone)
	input.IdempotencyKey = &key
	input.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeFile, URI: "rollback.txt"}}
	const triggerName = "test_fail_finish_idempotency_insert"
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER `+triggerName+`
			BEFORE INSERT ON idempotency_records
			WHEN NEW.operation = 'finish_attempt'
			BEGIN
				SELECT RAISE(ABORT, 'forced finish idempotency insert failure');
			END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `DROP TRIGGER `+triggerName)
			return err
		}); err != nil {
			t.Errorf("drop test trigger: %v", err)
		}
	}()

	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err == nil {
		t.Fatal("finish succeeded despite idempotency insert failure")
	}

	var attemptStatus, issueStatus string
	var issueVersion int64
	var events, artifacts, records int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`,
			claim.Attempt.ID).Scan(&attemptStatus); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT status, version FROM issues WHERE id = ?`,
			issue.ID).Scan(&issueStatus, &issueVersion); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events
			WHERE attempt_id = ? AND event_type = 'attempt_completed'`, claim.Attempt.ID).Scan(&events); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`,
			claim.Attempt.ID).Scan(&artifacts); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records
			WHERE operation = 'finish_attempt' AND idempotency_key = ?`, key).Scan(&records)
	}); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != string(domain.AttemptStatusActive) || issueStatus != string(domain.StatusReady) ||
		issueVersion != issue.Issue.Version || events != 0 || artifacts != 0 || records != 0 {
		t.Fatalf("idempotency rollback state = attempt %q issue %q version %d events %d artifacts %d records %d",
			attemptStatus, issueStatus, issueVersion, events, artifacts, records)
	}
}

func TestClaimIssueIdempotencyInsertFailureRollsBack(t *testing.T) {
	fixture := newAttemptTestFixture(t, "claim-idempotency-rollback")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "claim idempotency rollback", domain.StatusReady)
	key := "claim-idempotency-rollback-key"
	const triggerName = "test_fail_claim_issue_idempotency_insert"
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER `+triggerName+`
			BEFORE INSERT ON idempotency_records
			WHEN NEW.operation = 'claim_issue'
			BEGIN
				SELECT RAISE(ABORT, 'forced claim issue idempotency insert failure');
			END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `DROP TRIGGER `+triggerName)
			return err
		}); err != nil {
			t.Errorf("drop test trigger: %v", err)
		}
	}()

	_, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID, LeaseSeconds: intPointer(60), IdempotencyKey: &key})
	assertDomainCode(t, err, domain.CodeStorageConstraint)

	var attemptCount, eventCount, recordCount int
	var issueStatus string
	var issueVersion int64
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM work_attempts WHERE issue_id = ?`, issue.ID).Scan(&attemptCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE issue_id = ? AND event_type = 'attempt_started'`, issue.ID).Scan(&eventCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records WHERE operation = 'claim_issue' AND idempotency_key = ?`, key).Scan(&recordCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT status, version FROM issues WHERE id = ?`, issue.ID).Scan(&issueStatus, &issueVersion); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 || eventCount != 0 || recordCount != 0 || issueStatus != string(domain.StatusReady) || issueVersion != issue.Issue.Version {
		t.Fatalf("claim rollback state = attempts %d events %d records %d issue %q version %d",
			attemptCount, eventCount, recordCount, issueStatus, issueVersion)
	}
}

func TestAttemptClaimRenewExpiryAndTakeover(t *testing.T) {
	ctx := context.Background()
	source := clock.NewFakeClock(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "attempts.db")
	db, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = db.Close(ctx) }()
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			"01ARZ3NDEKTSV4RRFFQ69G5FAV", source.Now().Format(time.RFC3339Nano), source.Now().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issues, _ := sqlite.NewIssueRepository(db)
	issueService, err := application.NewIssueService(issues, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "claim me", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := sqlite.NewAttemptRepository(db)
	service, err := application.NewAttemptService(repository, source, generator)
	if err != nil {
		t.Fatal(err)
	}

	claim, err := service.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Attempt.Kind != domain.AttemptKindWork || claim.Attempt.ContextEventIDAtStart != 1 || claim.LeaseToken == "" {
		t.Fatalf("claim metadata = kind %q context event %d token present %t", claim.Attempt.Kind, claim.Attempt.ContextEventIDAtStart, claim.LeaseToken != "")
	}
	var storedHash []byte
	var starts, renewEvents int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT lease_token_hash FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&storedHash); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE event_type = 'attempt_started'`).Scan(&starts); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE event_type = 'attempt_renewed'`).Scan(&renewEvents)
	}); err != nil {
		t.Fatal(err)
	}
	if len(storedHash) != 32 || string(storedHash) == claim.LeaseToken || starts != 1 || renewEvents != 0 {
		t.Fatalf("stored lease/event state = hash %x starts %d renew %d", storedHash, starts, renewEvents)
	}
	if err := db.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	repository, err = sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err = application.NewAttemptService(repository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RenewAttempt(ctx, domain.RenewAttemptInput{AttemptID: claim.Attempt.ID, LeaseToken: "wrong"}); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidLeaseToken}) {
		t.Fatalf("invalid token error = %v", err)
	}
	source.Advance(time.Second)
	renewed, err := service.RenewAttempt(ctx, domain.RenewAttemptInput{AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken})
	if err != nil || !renewed.LeaseExpiresAt.After(claim.Attempt.LeaseExpiresAt) {
		t.Fatalf("renew = %#v, %v", renewed, err)
	}
	source.Advance(time.Duration(domain.DefaultLeaseSeconds) * time.Second)
	if _, err := service.RenewAttempt(ctx, domain.RenewAttemptInput{AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken}); !errors.Is(err, &domain.Error{Code: domain.CodeLeaseExpired}) {
		t.Fatalf("boundary renewal error = %v", err)
	}
	takeover, err := service.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil || takeover.Attempt.ID == claim.Attempt.ID {
		t.Fatalf("takeover metadata = id changed %t, error %v", takeover.Attempt.ID != claim.Attempt.ID, err)
	}
	var expiredEvents int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE event_type = 'attempt_expired'`).Scan(&expiredEvents)
	}); err != nil {
		t.Fatal(err)
	}
	if expiredEvents != 1 {
		t.Fatalf("expired events = %d, want 1", expiredEvents)
	}
}

func TestExpireAttemptsCleansAllIssuesAtBoundaryAndPreservesState(t *testing.T) {
	fixture := newAttemptTestFixture(t, "cleanup")
	defer fixture.close()

	firstIssue := createAttemptIssue(t, fixture, "first cleanup issue", domain.StatusReady)
	secondIssue := createAttemptIssue(t, fixture, "second cleanup issue", domain.StatusReady)
	laterIssue := createAttemptIssue(t, fixture, "later cleanup issue", domain.StatusReady)
	resultIssue := createAttemptIssue(t, fixture, "result preservation issue", domain.StatusReady)

	first, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: firstIssue.ID})
	if err != nil {
		t.Fatal(err)
	}
	note, err := fixture.attempts.SaveAttemptNote(fixture.ctx, domain.SaveAttemptNoteInput{
		AttemptID: first.Attempt.ID, LeaseToken: first.LeaseToken,
		Kind: domain.AttemptNoteKindCheckpoint, Content: "durable checkpoint",
		Artifacts: []domain.ArtifactInput{{Type: domain.ArtifactTypeFile, URI: "checkpoint.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: secondIssue.ID})
	if err != nil {
		t.Fatal(err)
	}
	resultClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: resultIssue.ID})
	if err != nil {
		t.Fatal(err)
	}
	finish := finishInput(resultClaim, domain.AttemptOutcomeFailed)
	finish.FailureReasonCode = failurePointer(domain.FailureReasonOther)
	finished, err := fixture.attempts.FinishAttempt(fixture.ctx, finish)
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(time.Second)
	later, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: laterIssue.ID})
	if err != nil {
		t.Fatal(err)
	}

	fixture.clock.Advance(time.Duration(domain.DefaultLeaseSeconds)*time.Second - time.Second)
	repository, err := sqlite.NewAttemptRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ExpireAttempts(fixture.ctx, ports.ExpireAttemptsCommand{}); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("zero cleanup timestamp error = %v", err)
	}
	cleaned, err := fixture.attempts.ExpireAttempts(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.ExpiredAttemptCount != 2 {
		t.Fatalf("expired attempt count = %d, want 2", cleaned.ExpiredAttemptCount)
	}
	repeated, err := fixture.attempts.ExpireAttempts(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ExpiredAttemptCount != 0 {
		t.Fatalf("repeated cleanup count = %d, want 0", repeated.ExpiredAttemptCount)
	}

	var firstStatus, secondStatus, laterStatus, resultStatus string
	var laterExpiry, resultSummary string
	var issueStatus string
	var noteCount, artifactCount, resultEvents int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`, first.Attempt.ID).Scan(&firstStatus); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`, second.Attempt.ID).Scan(&secondStatus); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT status, lease_expires_at FROM work_attempts WHERE id = ?`, later.Attempt.ID).Scan(&laterStatus, &laterExpiry); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT status, result_summary FROM work_attempts WHERE id = ?`, resultClaim.Attempt.ID).Scan(&resultStatus, &resultSummary); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT status FROM issues WHERE id = ?`, firstIssue.ID).Scan(&issueStatus); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM attempt_notes WHERE id = ?`, note.Note.ID).Scan(&noteCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, first.Attempt.ID).Scan(&artifactCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE attempt_id = ? AND event_type = 'attempt_failed'`, resultClaim.Attempt.ID).Scan(&resultEvents)
	}); err != nil {
		t.Fatal(err)
	}
	if finished.Attempt.Status != domain.AttemptStatusFailed ||
		firstStatus != string(domain.AttemptStatusExpired) || secondStatus != string(domain.AttemptStatusExpired) ||
		laterStatus != string(domain.AttemptStatusActive) || resultStatus != string(domain.AttemptStatusFailed) ||
		resultSummary != "summary" || issueStatus != string(domain.StatusReady) ||
		noteCount != 1 || artifactCount != 1 || resultEvents != 1 {
		t.Fatalf("cleanup state = first %q second %q later %q later expiry %q result %q/%q issue %q notes %d artifacts %d result events %d",
			firstStatus, secondStatus, laterStatus, laterExpiry, resultStatus, resultSummary, issueStatus,
			noteCount, artifactCount, resultEvents)
	}
	if countAttemptEvents(t, fixture, first.Attempt.ID, "attempt_expired") != 1 ||
		countAttemptEvents(t, fixture, second.Attempt.ID, "attempt_expired") != 1 {
		t.Fatal("cleanup did not write exactly one expiry event per attempt")
	}

	takeover, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: firstIssue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if takeover.Attempt.ID == first.Attempt.ID {
		t.Fatal("cleanup did not release the active-attempt claim")
	}
}

func TestAttemptSessionAttributionAndExpiryFallback(t *testing.T) {
	fixture := newAttemptTestFixture(t, "session-attribution")
	defer fixture.close()
	sessionA := "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	sessionB := "01BX5ZZKBKACTAV9WEVGEMMVS0"
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		timestamp := fixture.clock.Now().Format(time.RFC3339Nano)
		for _, id := range []string{sessionA, sessionB} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(id, client_name, started_at, last_seen_at) VALUES (?, 'test', ?, ?)`, id, timestamp, timestamp); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	issue := createAttemptIssue(t, fixture, "session attribution", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID, SessionID: stringPointer(sessionA)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.attempts.SaveAttemptNote(fixture.ctx, domain.SaveAttemptNoteInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, SessionID: stringPointer(sessionB),
		Kind: domain.AttemptNoteKindCheckpoint, Content: "handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	finish := finishInput(claim, domain.AttemptOutcomeCompleted)
	finish.SessionID = stringPointer(sessionB)
	finish.TargetIssueStatus = statusPointer(domain.StatusDone)
	finished, err := fixture.attempts.FinishAttempt(fixture.ctx, finish)
	if err != nil || finished.Attempt.SessionID == nil || *finished.Attempt.SessionID != sessionA {
		t.Fatalf("continuation result = %#v, %v", finished.Attempt, err)
	}
	var events []struct {
		Type    string
		Session sql.NullString
	}
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		rows, err := query.QueryContext(ctx, `SELECT event_type, session_id FROM issue_events WHERE attempt_id = ? ORDER BY id`, claim.Attempt.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event struct {
				Type    string
				Session sql.NullString
			}
			if err := rows.Scan(&event.Type, &event.Session); err != nil {
				return err
			}
			events = append(events, event)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != "attempt_started" || !events[0].Session.Valid || events[0].Session.String != sessionA ||
		events[1].Type != "checkpoint_saved" || !events[1].Session.Valid || events[1].Session.String != sessionB ||
		events[2].Type != "attempt_completed" || !events[2].Session.Valid || events[2].Session.String != sessionB {
		t.Fatalf("attempt events = %#v", events)
	}
	unknownIssue := createAttemptIssue(t, fixture, "unknown session", domain.StatusReady)
	unknown := "01BX5ZZKBKACTAV9WEVGEMMVS1"
	if _, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: unknownIssue.ID, SessionID: &unknown}); err == nil {
		t.Fatal("unknown session claim succeeded")
	}
	var attempts, starts int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM work_attempts WHERE issue_id = ?`, unknownIssue.ID).Scan(&attempts); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE issue_id = ? AND event_type = 'attempt_started'`, unknownIssue.ID).Scan(&starts)
	}); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || starts != 0 {
		t.Fatalf("unknown session rollback = attempts %d starts %d", attempts, starts)
	}
	expiryIssue := createAttemptIssue(t, fixture, "expiry attribution", domain.StatusReady)
	expiryClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: expiryIssue.ID, SessionID: stringPointer(sessionA)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(time.Duration(domain.DefaultLeaseSeconds) * time.Second)
	_, err = fixture.attempts.SaveAttemptNote(fixture.ctx, domain.SaveAttemptNoteInput{
		AttemptID: expiryClaim.Attempt.ID, LeaseToken: expiryClaim.LeaseToken, SessionID: stringPointer(sessionB),
		Kind: domain.AttemptNoteKindCheckpoint, Content: "too late",
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeLeaseExpired}) {
		t.Fatalf("expired note error = %v", err)
	}
	var expirySession sql.NullString
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT session_id FROM issue_events WHERE attempt_id = ? AND event_type = 'attempt_expired'`, expiryClaim.Attempt.ID).Scan(&expirySession)
	}); err != nil {
		t.Fatal(err)
	}
	if expirySession.Valid {
		t.Fatalf("expiry session = %#v, want NULL", expirySession)
	}
}

func TestSaveAttemptNoteAuthorizesPersistsEventsAndExpiresAtBoundary(t *testing.T) {
	ctx := context.Background()
	source := clock.NewFakeClock(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "notes.db")
	db, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(ctx) }()
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			"01ARZ3NDEKTSV4RRFFQ69G5FAV", source.Now().Format(time.RFC3339Nano), source.Now().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issueService, err := application.NewIssueService(issues, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "note me", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewAttemptService(repository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", LeaseToken: claim.LeaseToken, Kind: domain.AttemptNoteKindProgress, Content: "missing",
	}); !errors.Is(err, &domain.Error{Code: domain.CodeAttemptNotFound}) {
		t.Fatalf("missing attempt error = %v", err)
	}

	kinds := []domain.AttemptNoteKind{
		domain.AttemptNoteKindProgress,
		domain.AttemptNoteKindFinding,
		domain.AttemptNoteKindWarning,
		domain.AttemptNoteKindCheckpoint,
	}
	var checkpoint domain.AttemptNote
	for _, kind := range kinds {
		content := "note " + string(kind)
		if kind == domain.AttemptNoteKindCheckpoint {
			content = "durable state"
		}
		result, err := service.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
			AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Kind: kind, Content: content,
			NextSteps: []string{"next " + string(kind)}, Important: kind == domain.AttemptNoteKindCheckpoint,
		})
		if err != nil || result.Note.ID == "" || result.Note.CreatedAt != source.Now() || result.Note.Kind != kind {
			t.Fatalf("save %q = %#v, %v", kind, result, err)
		}
		if kind == domain.AttemptNoteKindCheckpoint {
			checkpoint = result.Note
		}
	}
	if _, err := service.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
		AttemptID: claim.Attempt.ID, LeaseToken: "wrong", Kind: domain.AttemptNoteKindProgress, Content: "not saved",
	}); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidLeaseToken}) {
		t.Fatalf("invalid token error = %v", err)
	}

	if err := db.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	repository, err = sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err = application.NewAttemptService(repository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	var noteCount, ordinaryEvents, checkpointEvents int
	var content, nextSteps, payload string
	var important int
	var createdAtText string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM attempt_notes`).Scan(&noteCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT content, next_steps_json, important, created_at
				FROM attempt_notes WHERE id = ?`, checkpoint.ID).Scan(&content, &nextSteps, &important, &createdAtText); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE event_type = 'attempt_note_saved'`).Scan(&ordinaryEvents); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE event_type = 'checkpoint_saved'`).Scan(&checkpointEvents); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT payload FROM issue_events WHERE event_type = 'checkpoint_saved'`).Scan(&payload)
	}); err != nil {
		t.Fatal(err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdAtText)
	if err != nil {
		t.Fatal(err)
	}
	if noteCount != 4 || content != "durable state" || nextSteps != `["next checkpoint"]` || important != 1 ||
		!createdAt.Equal(source.Now()) || ordinaryEvents != 3 || checkpointEvents != 1 ||
		strings.Contains(payload, "durable state") || strings.Contains(payload, "next checkpoint") ||
		strings.Contains(payload, claim.LeaseToken) {
		t.Fatalf("persisted notes/events = notes %d content %q next %q important %d time %s ordinary %d checkpoint %d payload %q",
			noteCount, content, nextSteps, important, createdAt, ordinaryEvents, checkpointEvents, payload)
	}

	source.Advance(time.Duration(domain.DefaultLeaseSeconds) * time.Second)
	if _, err := service.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Kind: domain.AttemptNoteKindCheckpoint, Content: "expired",
	}); !errors.Is(err, &domain.Error{Code: domain.CodeLeaseExpired}) {
		t.Fatalf("boundary save error = %v", err)
	}
	if _, err := service.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Kind: domain.AttemptNoteKindProgress, Content: "inactive",
	}); !errors.Is(err, &domain.Error{Code: domain.CodeAttemptNotActive}) {
		t.Fatalf("post-expiry save error = %v", err)
	}
	var expiredEvents int
	var status string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM attempt_notes`).Scan(&noteCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE event_type = 'attempt_expired'`).Scan(&expiredEvents); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if noteCount != 4 || expiredEvents != 1 || status != string(domain.AttemptStatusExpired) {
		t.Fatalf("boundary state = notes %d expiry events %d status %q", noteCount, expiredEvents, status)
	}
}

func TestSaveAttemptNotePersistsArtifactsAtomicallyAndSafely(t *testing.T) {
	ctx := context.Background()
	source := clock.NewFakeClock(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "artifacts.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			"01ARZ3NDEKTSV4RRFFQ69G5FAS", source.Now().Format(time.RFC3339Nano), source.Now().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	issueService, err := application.NewIssueService(issues, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "artifact note", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewAttemptService(repository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Kind: domain.AttemptNoteKindCheckpoint,
		Content: "private note body", Artifacts: []domain.ArtifactInput{
			{Type: domain.ArtifactTypeFile, URI: "internal/application/attempt_service.go", Title: stringPointer("service"), Metadata: json.RawMessage(`{"language":"go"}`)},
			{Type: domain.ArtifactTypeURL, URI: "https://example.invalid/build/42"},
		},
	})
	if err != nil || len(result.Artifacts) != 2 {
		t.Fatalf("save result = %#v, %v", result, err)
	}
	for index, artifact := range result.Artifacts {
		if artifact.ID == "" || artifact.IssueID != issue.ID || artifact.AttemptID == nil ||
			*artifact.AttemptID != claim.Attempt.ID || !artifact.CreatedAt.Equal(source.Now()) {
			t.Fatalf("artifact %d = %#v", index, artifact)
		}
	}
	var noteCount, artifactCount int
	var payload string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM attempt_notes`).Scan(&noteCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT payload FROM issue_events WHERE event_type = 'checkpoint_saved' ORDER BY id DESC LIMIT 1`).Scan(&payload)
	}); err != nil {
		t.Fatal(err)
	}
	if noteCount != 1 || artifactCount != 2 || strings.Contains(payload, "internal/application/attempt_service.go") ||
		strings.Contains(payload, "service") || strings.Contains(payload, "language") ||
		strings.Contains(payload, "private note body") || strings.Contains(payload, claim.LeaseToken) {
		t.Fatalf("atomic or unsafe state = notes %d artifacts %d payload %q", noteCount, artifactCount, payload)
	}

	hash := sha256.Sum256([]byte(claim.LeaseToken))
	duplicate := ports.SaveAttemptNoteCommand{
		NoteID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", AttemptID: claim.Attempt.ID, TokenHash: hash[:],
		Kind: domain.AttemptNoteKindProgress, Content: "rollback", OccurredAt: source.Now(),
		Artifacts: []domain.Artifact{{
			ID: result.Artifacts[0].ID, Type: domain.ArtifactTypeOther, URI: "duplicate", CreatedAt: source.Now(),
		}},
	}
	if _, err := repository.SaveAttemptNote(ctx, duplicate); !errors.Is(err, &domain.Error{Code: domain.CodeStorageConstraint}) {
		t.Fatalf("duplicate artifact error = %v", err)
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM attempt_notes`).Scan(&noteCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount)
	}); err != nil {
		t.Fatal(err)
	}
	if noteCount != 1 || artifactCount != 2 {
		t.Fatalf("rollback state = notes %d artifacts %d", noteCount, artifactCount)
	}
}

func TestAttemptSimultaneousClaimsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	source := clock.NewFakeClock(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "concurrent.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			"01ARZ3NDEKTSV4RRFFQ69G5FAV", source.Now().Format(time.RFC3339Nano), source.Now().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	generator, _ := ids.NewGenerator(source, rand.Reader)
	issues, _ := sqlite.NewIssueRepository(db)
	issueService, _ := application.NewIssueService(issues, source, generator)
	issue, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeBug, Title: "race", Status: domain.StatusReview})
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := sqlite.NewAttemptRepository(db)
	service, _ := application.NewAttemptService(repository, source, generator)
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			claim, err := service.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: issue.ID})
			if err == nil && claim.Attempt.Kind != domain.AttemptKindReview {
				err = errors.New("wrong attempt kind")
			}
			results <- err
		}()
	}
	group.Wait()
	close(results)
	var succeeded, activeExists int
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, &domain.Error{Code: domain.CodeActiveAttemptExists}) {
			activeExists++
		} else {
			t.Fatalf("claim error = %v", err)
		}
	}
	if succeeded != 1 || activeExists != 1 {
		t.Fatalf("successes %d active errors %d", succeeded, activeExists)
	}
}

type attemptTestFixture struct {
	ctx       context.Context
	clock     *clock.FakeClock
	path      string
	db        *sqlite.DB
	issues    *application.IssueService
	attempts  *application.AttemptService
	relations *application.RelationService
}

func newAttemptTestFixture(t *testing.T, name string) *attemptTestFixture {
	t.Helper()
	ctx := context.Background()
	source := clock.NewFakeClock(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), name+".db")
	db, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			sqliteTestProjectID, source.Now().Format(time.RFC3339Nano), source.Now().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	attemptRepository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	relationRepository, err := sqlite.NewRelationRepository(db)
	if err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	issues, err := application.NewIssueService(issueRepository, source, generator)
	if err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	attempts, err := application.NewAttemptService(attemptRepository, source, generator)
	if err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	relations, err := application.NewRelationService(relationRepository, source, generator)
	if err != nil {
		_ = db.Close(ctx)
		t.Fatal(err)
	}
	return &attemptTestFixture{ctx: ctx, clock: source, path: path, db: db, issues: issues, attempts: attempts, relations: relations}
}

func (fixture *attemptTestFixture) close() {
	_ = fixture.db.Close(fixture.ctx)
}

func reopenAttemptTestFixture(t *testing.T, fixture *attemptTestFixture) {
	t.Helper()
	if err := fixture.db.Close(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(fixture.ctx, fixture.path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(fixture.ctx, db, fixture.clock); err != nil {
		_ = db.Close(fixture.ctx)
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(fixture.clock, rand.Reader)
	if err != nil {
		_ = db.Close(fixture.ctx)
		t.Fatal(err)
	}
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		_ = db.Close(fixture.ctx)
		t.Fatal(err)
	}
	attemptRepository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		_ = db.Close(fixture.ctx)
		t.Fatal(err)
	}
	relationRepository, err := sqlite.NewRelationRepository(db)
	if err != nil {
		_ = db.Close(fixture.ctx)
		t.Fatal(err)
	}
	issues, err := application.NewIssueService(issueRepository, fixture.clock, generator)
	if err != nil {
		_ = db.Close(fixture.ctx)
		t.Fatal(err)
	}
	attempts, err := application.NewAttemptService(attemptRepository, fixture.clock, generator)
	if err != nil {
		_ = db.Close(fixture.ctx)
		t.Fatal(err)
	}
	relations, err := application.NewRelationService(relationRepository, fixture.clock, generator)
	if err != nil {
		_ = db.Close(fixture.ctx)
		t.Fatal(err)
	}
	fixture.db = db
	fixture.issues = issues
	fixture.attempts = attempts
	fixture.relations = relations
}

func createAttemptIssue(t *testing.T, fixture *attemptTestFixture, title string, status domain.Status) application.CreateIssueResult {
	t.Helper()
	issue, err := fixture.issues.CreateIssue(fixture.ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: title, Status: status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

func finishInput(claim application.ClaimIssueResult, outcome domain.AttemptOutcome) domain.FinishAttemptInput {
	return domain.FinishAttemptInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Outcome: outcome, ResultSummary: "summary",
	}
}

func statusPointer(value domain.Status) *domain.Status { return &value }

func intPointer(value int) *int { return &value }

func reviewPointer(value domain.ReviewOutcome) *domain.ReviewOutcome { return &value }

func failurePointer(value domain.FailureReasonCode) *domain.FailureReasonCode { return &value }

func interruptionPointer(value domain.InterruptionReasonCode) *domain.InterruptionReasonCode {
	return &value
}

func countAttemptEvents(t *testing.T, fixture *attemptTestFixture, attemptID, eventType string) int {
	t.Helper()
	var count int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events
			WHERE attempt_id = ? AND event_type = ?`, attemptID, eventType).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func requireAttemptActive(t *testing.T, fixture *attemptTestFixture, claim application.ClaimIssueResult) {
	t.Helper()
	if _, err := fixture.attempts.RenewAttempt(fixture.ctx, domain.RenewAttemptInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
	}); err != nil {
		t.Fatalf("attempt is not active: %v", err)
	}
}

func requireAttemptInactive(t *testing.T, fixture *attemptTestFixture, claim application.ClaimIssueResult) {
	t.Helper()
	if _, err := fixture.attempts.RenewAttempt(fixture.ctx, domain.RenewAttemptInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
	}); !errors.Is(err, &domain.Error{Code: domain.CodeAttemptNotActive}) {
		t.Fatalf("attempt renewal after finish = %v", err)
	}
}

func TestFinishAttemptCompletedWorkPersistsAtomicOutcomeAndSafeEvent(t *testing.T) {
	fixture := newAttemptTestFixture(t, "complete")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "complete work", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Outcome: domain.AttemptOutcomeCompleted, TargetIssueStatus: statusPointer(domain.StatusDone),
		ResultSummary: "implemented", NextSteps: []string{"follow up"},
		Verification: []string{"go test ./..."},
		Artifacts: []domain.ArtifactInput{
			{Type: domain.ArtifactTypeFile, URI: "build/result.txt", Title: stringPointer("result"), Metadata: json.RawMessage(`{"kind":"result"}`)},
			{Type: domain.ArtifactTypeURL, URI: "https://example.invalid/build/42"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Attempt.Status != domain.AttemptStatusCompleted ||
		finished.Attempt.FinishedAt == nil || !finished.Attempt.FinishedAt.Equal(fixture.clock.Now()) ||
		finished.Attempt.ResultSummary == nil || *finished.Attempt.ResultSummary != "implemented" ||
		!equalStrings(finished.Attempt.NextSteps, []string{"follow up"}) ||
		!equalStrings(finished.Attempt.Verification, []string{"go test ./..."}) || len(finished.Artifacts) != 2 ||
		finished.Artifacts[0].IssueID != issue.ID || finished.Artifacts[0].AttemptID == nil ||
		*finished.Artifacts[0].AttemptID != claim.Attempt.ID ||
		finished.Artifacts[0].Title == nil || *finished.Artifacts[0].Title != "result" ||
		string(finished.Artifacts[0].Metadata) != `{"kind":"result"}` ||
		!finished.Artifacts[0].CreatedAt.Equal(fixture.clock.Now()) {
		t.Fatalf("finished attempt = %#v", finished.Attempt)
	}
	if finished.Issue.Status != domain.StatusDone || finished.Issue.Version != claim.Issue.Version+1 ||
		finished.Issue.ClosedAt == nil || !finished.Issue.ClosedAt.Equal(fixture.clock.Now()) {
		t.Fatalf("finished issue = %#v", finished.Issue)
	}
	var status, resultSummary, nextJSON, verificationJSON string
	var finishedAt, failureCode, interruptionCode sql.NullString
	var eventPayload string
	var artifactCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT status, finished_at, result_summary, next_steps_json,
			verification_json, failure_reason_code, interruption_reason_code
			FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&status, &finishedAt, &resultSummary,
			&nextJSON, &verificationJSON, &failureCode, &interruptionCode); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT payload FROM issue_events
			WHERE attempt_id = ? AND event_type = 'attempt_completed' ORDER BY id`, claim.Attempt.ID).Scan(&eventPayload); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount)
	}); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.AttemptStatusCompleted) || artifactCount != 2 || !finishedAt.Valid || !finishedAtTimeIs(finishedAt.String, fixture.clock.Now()) ||
		resultSummary != "implemented" || nextJSON != `["follow up"]` ||
		verificationJSON != `["go test ./..."]` || failureCode.Valid || interruptionCode.Valid {
		t.Fatalf("stored completion = status %q finished %q summary %q next %q verification %q failure %#v interruption %#v",
			status, finishedAt.String, resultSummary, nextJSON, verificationJSON, failureCode, interruptionCode)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(eventPayload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["attempt_id"] != claim.Attempt.ID || payload["outcome"] != string(domain.AttemptOutcomeCompleted) ||
		payload["target_status"] != string(domain.StatusDone) ||
		strings.Contains(eventPayload, claim.LeaseToken) || strings.Contains(eventPayload, "implemented") ||
		strings.Contains(eventPayload, "follow up") || strings.Contains(eventPayload, "go test ./...") ||
		strings.Contains(eventPayload, "build/result.txt") || strings.Contains(eventPayload, `"title":"result"`) ||
		strings.Contains(eventPayload, `"kind":"result"`) ||
		strings.Contains(eventPayload, "https://example.invalid/build/42") {
		t.Fatalf("unsafe completion event = %q", eventPayload)
	}
	if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed") != 1 {
		t.Fatal("completion event count is not exactly one")
	}
	requireAttemptInactive(t, fixture, claim)
}

func finishedAtTimeIs(value string, want time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Equal(want)
}

func TestFinishAttemptDuplicateArtifactRollsBackCompletion(t *testing.T) {
	fixture := newAttemptTestFixture(t, "finish-rollback")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "rollback completion", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	existingID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO artifacts(
			id, issue_id, attempt_id, type, uri, title, metadata, created_at
		) VALUES (?, ?, ?, ?, ?, NULL, NULL, ?)`, existingID, issue.ID, claim.Attempt.ID,
			domain.ArtifactTypeOther, "existing", fixture.clock.Now().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(claim.LeaseToken))
	repository, err := sqlite.NewAttemptRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	input := finishInput(claim, domain.AttemptOutcomeCompleted)
	input.TargetIssueStatus = statusPointer(domain.StatusDone)
	_, err = repository.FinishAttempt(fixture.ctx, ports.FinishAttemptCommand{
		AttemptID: claim.Attempt.ID, TokenHash: hash[:], Input: input,
		Artifacts: []domain.Artifact{{
			ID: existingID, Type: domain.ArtifactTypeOther, URI: "duplicate", CreatedAt: fixture.clock.Now(),
		}},
		OccurredAt: fixture.clock.Now(),
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeStorageConstraint}) {
		t.Fatalf("duplicate final artifact error = %v", err)
	}
	var attemptStatus, issueStatus string
	var issueVersion int64
	var artifactCount, completionEvents int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&attemptStatus); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT status, version FROM issues WHERE id = ?`, issue.ID).Scan(&issueStatus, &issueVersion); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE attempt_id = ? AND event_type = 'attempt_completed'`, claim.Attempt.ID).Scan(&completionEvents)
	}); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != string(domain.AttemptStatusActive) || issueStatus != string(domain.StatusReady) ||
		issueVersion != issue.Issue.Version || artifactCount != 1 || completionEvents != 0 {
		t.Fatalf("completion rollback state = attempt %q issue %q version %d artifacts %d events %d",
			attemptStatus, issueStatus, issueVersion, artifactCount, completionEvents)
	}
}

func TestFinishAttemptFailedAndInterruptedPreserveIssueState(t *testing.T) {
	fixture := newAttemptTestFixture(t, "outcomes")
	defer fixture.close()

	tests := []struct {
		name         string
		outcome      domain.AttemptOutcome
		eventType    string
		failure      *domain.FailureReasonCode
		interruption *domain.InterruptionReasonCode
	}{
		{name: "failed", outcome: domain.AttemptOutcomeFailed, eventType: "attempt_failed", failure: failurePointer(domain.FailureReasonTestsFailed)},
		{name: "interrupted", outcome: domain.AttemptOutcomeInterrupted, eventType: "attempt_interrupted", interruption: interruptionPointer(domain.InterruptionReasonHandoff)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issue := createAttemptIssue(t, fixture, test.name, domain.StatusReady)
			claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
			if err != nil {
				t.Fatal(err)
			}
			before := claim.Issue
			input := finishInput(claim, test.outcome)
			input.FailureReasonCode = test.failure
			input.InterruptionReasonCode = test.interruption
			input.ReasonDetails = pointer("private diagnostic")
			input.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeOther, URI: "result/" + test.name}}
			finished, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if finished.Attempt.Status != domain.AttemptStatus(test.outcome) ||
				finished.Issue.Status != before.Status || finished.Issue.Version != before.Version ||
				!finished.Issue.UpdatedAt.Equal(before.UpdatedAt) ||
				(finished.Issue.ClosedAt != nil && before.ClosedAt == nil) ||
				(finished.Issue.ClosedAt == nil && before.ClosedAt != nil) {
				t.Fatalf("finish state changed issue: before=%#v after=%#v", before, finished.Issue)
			}
			var status, payload string
			var artifactCount int
			var storedFailure, storedInterruption, storedDetails sql.NullString
			if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
				if err := query.QueryRowContext(ctx, `SELECT status, failure_reason_code,
					interruption_reason_code, reason_details FROM work_attempts WHERE id = ?`,
					claim.Attempt.ID).Scan(&status, &storedFailure, &storedInterruption, &storedDetails); err != nil {
					return err
				}
				if err := query.QueryRowContext(ctx, `SELECT payload FROM issue_events
					WHERE attempt_id = ? AND event_type = ? ORDER BY id`, claim.Attempt.ID, test.eventType).Scan(&payload); err != nil {
					return err
				}
				return query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount)
			}); err != nil {
				t.Fatal(err)
			}
			if status != string(test.outcome) || artifactCount != 1 || !storedDetails.Valid || storedDetails.String != "private diagnostic" ||
				(test.failure == nil && storedFailure.Valid) || (test.failure != nil && (!storedFailure.Valid || storedFailure.String != string(*test.failure))) ||
				(test.interruption == nil && storedInterruption.Valid) || (test.interruption != nil && (!storedInterruption.Valid || storedInterruption.String != string(*test.interruption))) ||
				strings.Contains(payload, "summary") || strings.Contains(payload, "private diagnostic") ||
				strings.Contains(payload, claim.LeaseToken) {
				t.Fatalf("stored outcome = status %q failure %#v interruption %#v details %#v payload %q",
					status, storedFailure, storedInterruption, storedDetails, payload)
			}
			if !strings.Contains(payload, string(testReasonCode(test.failure, test.interruption))) {
				t.Fatalf("event does not contain reason code: %q", payload)
			}
			if countAttemptEvents(t, fixture, claim.Attempt.ID, test.eventType) != 1 {
				t.Fatalf("event count = %d", countAttemptEvents(t, fixture, claim.Attempt.ID, test.eventType))
			}
		})
	}
}

func TestForceReleaseAttemptInterruptsAttemptAndPreservesRecoveryData(t *testing.T) {
	fixture := newAttemptTestFixture(t, "force-release")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "force release", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(id, attempt_id, kind, content, important, created_at)
			VALUES (?, ?, 'progress', 'keep me', 0, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FAY", claim.Attempt.ID, fixture.clock.Now().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id, issue_id, attempt_id, type, uri, title, metadata, created_at)
			VALUES (?, ?, ?, ?, 'artifact.txt', NULL, NULL, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FAZ", issue.ID, claim.Attempt.ID, domain.ArtifactTypeOther, fixture.clock.Now().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE work_attempts
			SET result_summary = ?, next_steps_json = ?, verification_json = ?, reason_details = ?
			WHERE id = ?`, "keep result", `["next step"]`, `["checked"]`, "preserve details", claim.Attempt.ID)
		return err
	}); err != nil {
		t.Fatalf("setup release fixture: %v", err)
	}

	repository, err := sqlite.NewAttemptRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	release, err := repository.ForceReleaseAttempt(fixture.ctx, ports.ForceReleaseAttemptCommand{AttemptID: claim.Attempt.ID, OccurredAt: fixture.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if release.Attempt.Status != domain.AttemptStatusInterrupted || release.Attempt.InterruptionReasonCode == nil || *release.Attempt.InterruptionReasonCode != domain.InterruptionReasonUserRequest || release.Attempt.FinishedAt == nil {
		t.Fatalf("release attempt = %#v", release.Attempt)
	}
	if release.LatestEventID == 0 {
		t.Fatal("expected latest event id")
	}
	if release.Attempt.ResultSummary == nil || *release.Attempt.ResultSummary != "keep result" ||
		release.Attempt.ReasonDetails == nil || *release.Attempt.ReasonDetails != "preserve details" ||
		!reflect.DeepEqual(release.Attempt.NextSteps, []string{"next step"}) ||
		!reflect.DeepEqual(release.Attempt.Verification, []string{"checked"}) {
		t.Fatalf("release recovery projection = %#v", release.Attempt)
	}

	var issueStatus string
	var issueVersion int64
	var noteCount, artifactCount int
	var leaseExpiresAt string
	var resultSummary, failureReason, reasonDetails sql.NullString
	var eventCount int
	var eventSession sql.NullString
	var eventPayload string
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT status, version FROM issues WHERE id = ?`, issue.ID).Scan(&issueStatus, &issueVersion); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM attempt_notes WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&noteCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT lease_expires_at, result_summary, failure_reason_code, reason_details FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&leaseExpiresAt, &resultSummary, &failureReason, &reasonDetails); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT count(*), session_id, payload FROM issue_events WHERE attempt_id = ? AND event_type = 'attempt_interrupted'`, claim.Attempt.ID).Scan(&eventCount, &eventSession, &eventPayload); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if issueStatus != string(domain.StatusReady) || issueVersion != issue.Issue.Version {
		t.Fatalf("issue state changed: status %q version %d", issueStatus, issueVersion)
	}
	if noteCount != 1 || artifactCount != 1 || !resultSummary.Valid || resultSummary.String != "keep result" || failureReason.Valid || !reasonDetails.Valid || reasonDetails.String != "preserve details" {
		t.Fatalf("recovery data changed: notes %d artifacts %d result %#v failure %#v reason %#v", noteCount, artifactCount, resultSummary, failureReason, reasonDetails)
	}
	if eventCount != 1 || eventSession.Valid || !strings.Contains(eventPayload, `"outcome":"interrupted"`) || !strings.Contains(eventPayload, `"interruption_reason_code":"user_request"`) {
		t.Fatalf("event count %d session %#v payload %q", eventCount, eventSession, eventPayload)
	}
	if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_interrupted") != 1 {
		t.Fatalf("unexpected interruption event count")
	}
	if leaseExpiresAt == "" {
		t.Fatal("expected lease expiry to be preserved")
	}
}

func TestForceReleaseAttemptMissingAndInactiveReturnStableErrors(t *testing.T) {
	fixture := newAttemptTestFixture(t, "force-release-errors")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "force release errors", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlite.NewAttemptRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ForceReleaseAttempt(fixture.ctx, ports.ForceReleaseAttemptCommand{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", OccurredAt: fixture.clock.Now()}); !errors.Is(err, &domain.Error{Code: domain.CodeAttemptNotFound}) {
		t.Fatalf("missing attempt error = %v", err)
	}
	if _, err := repository.ForceReleaseAttempt(fixture.ctx, ports.ForceReleaseAttemptCommand{AttemptID: claim.Attempt.ID}); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("zero release timestamp error = %v", err)
	}
	input := finishInput(claim, domain.AttemptOutcomeCompleted)
	input.TargetIssueStatus = statusPointer(domain.StatusDone)
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ForceReleaseAttempt(fixture.ctx, ports.ForceReleaseAttemptCommand{AttemptID: claim.Attempt.ID, OccurredAt: fixture.clock.Now()}); !errors.Is(err, &domain.Error{Code: domain.CodeAttemptNotActive}) {
		t.Fatalf("inactive attempt error = %v", err)
	}
}

func TestForceReleaseAttemptConcurrentCallsCreateSingleEvent(t *testing.T) {
	fixture := newAttemptTestFixture(t, "force-release-concurrent")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "force release concurrent", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlite.NewAttemptRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan struct {
		result ports.ForceReleaseAttemptResult
		err    error
	}, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := repository.ForceReleaseAttempt(fixture.ctx, ports.ForceReleaseAttemptCommand{AttemptID: claim.Attempt.ID, OccurredAt: fixture.clock.Now()})
			results <- struct {
				result ports.ForceReleaseAttemptResult
				err    error
			}{result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var successCount, inactiveCount int
	for outcome := range results {
		if outcome.err == nil {
			successCount++
		} else if errors.Is(outcome.err, &domain.Error{Code: domain.CodeAttemptNotActive}) {
			inactiveCount++
		} else {
			t.Fatalf("unexpected concurrent release error = %v", outcome.err)
		}
	}
	if successCount != 1 || inactiveCount != 1 {
		t.Fatalf("concurrent outcomes = success %d inactive %d", successCount, inactiveCount)
	}
	if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_interrupted") != 1 {
		t.Fatalf("concurrent interruption event count = %d", countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_interrupted"))
	}
}

func testReasonCode(failure *domain.FailureReasonCode, interruption *domain.InterruptionReasonCode) string {
	if failure != nil {
		return string(*failure)
	}
	return string(*interruption)
}

func TestFinishAttemptReviewMappingAndShapeRejection(t *testing.T) {
	fixture := newAttemptTestFixture(t, "review")
	defer fixture.close()

	tests := []struct {
		name          string
		review        domain.ReviewOutcome
		wantStatus    domain.Status
		blockedReason *string
	}{
		{name: "approved", review: domain.ReviewOutcomeApproved, wantStatus: domain.StatusDone},
		{name: "changes requested", review: domain.ReviewOutcomeChangesRequested, wantStatus: domain.StatusReady},
		{name: "blocked", review: domain.ReviewOutcomeBlocked, wantStatus: domain.StatusBlocked, blockedReason: pointer("needs revision")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issue := createAttemptIssue(t, fixture, test.name, domain.StatusReview)
			claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
			if err != nil {
				t.Fatal(err)
			}
			input := finishInput(claim, domain.AttemptOutcomeCompleted)
			input.ReviewOutcome = reviewPointer(test.review)
			input.BlockedReason = test.blockedReason
			finished, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if finished.Issue.Status != test.wantStatus {
				t.Fatalf("review %q status = %q, want %q", test.review, finished.Issue.Status, test.wantStatus)
			}
			if test.blockedReason == nil && finished.Issue.BlockedReason != nil {
				t.Fatalf("review %q blocked reason = %v", test.review, finished.Issue.BlockedReason)
			}
			if test.blockedReason != nil && (finished.Issue.BlockedReason == nil || *finished.Issue.BlockedReason != *test.blockedReason) {
				t.Fatalf("blocked review reason = %v", finished.Issue.BlockedReason)
			}
		})
	}

	work := createAttemptIssue(t, fixture, "work shape", domain.StatusReady)
	workClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: work.ID})
	if err != nil {
		t.Fatal(err)
	}
	workInput := finishInput(workClaim, domain.AttemptOutcomeCompleted)
	workInput.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeOther, URI: "invalid-shape"}}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, workInput); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("missing work target error = %v", err)
	}
	requireAttemptActive(t, fixture, workClaim)
	if countAttemptEvents(t, fixture, workClaim.Attempt.ID, "attempt_completed") != 0 {
		t.Fatal("invalid work shape appended completion event")
	}
	var artifactCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, workClaim.Attempt.ID).Scan(&artifactCount)
	}); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 0 {
		t.Fatalf("invalid completion attached %d artifacts", artifactCount)
	}

	review := createAttemptIssue(t, fixture, "review shape", domain.StatusReview)
	reviewClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: review.ID})
	if err != nil {
		t.Fatal(err)
	}
	reviewInput := finishInput(reviewClaim, domain.AttemptOutcomeCompleted)
	reviewInput.ReviewOutcome = reviewPointer(domain.ReviewOutcomeApproved)
	reviewInput.TargetIssueStatus = statusPointer(domain.StatusDone)
	reviewInput.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeOther, URI: "invalid-review-shape"}}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, reviewInput); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("invalid review shape error = %v", err)
	}
	requireAttemptActive(t, fixture, reviewClaim)
	if countAttemptEvents(t, fixture, reviewClaim.Attempt.ID, "attempt_completed") != 0 {
		t.Fatal("invalid review shape appended completion event")
	}
}

func TestFinishAttemptAuthorizationAndExpiryPreserveCompletionData(t *testing.T) {
	fixture := newAttemptTestFixture(t, "authorization")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "authorize finish", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	wrongToken := finishInput(claim, domain.AttemptOutcomeCompleted)
	wrongToken.LeaseToken = "wrong-token"
	wrongToken.TargetIssueStatus = statusPointer(domain.StatusDone)
	wrongToken.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeOther, URI: "wrong-token"}}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, wrongToken); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidLeaseToken}) {
		t.Fatalf("wrong token error = %v", err)
	}
	requireAttemptActive(t, fixture, claim)
	if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed") != 0 {
		t.Fatal("wrong token appended completion event")
	}
	var artifactCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount)
	}); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 0 {
		t.Fatalf("wrong token attached %d artifacts", artifactCount)
	}
	missing := finishInput(claim, domain.AttemptOutcomeCompleted)
	missing.AttemptID = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	missing.TargetIssueStatus = statusPointer(domain.StatusDone)
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, missing); !errors.Is(err, &domain.Error{Code: domain.CodeAttemptNotFound}) {
		t.Fatalf("missing attempt error = %v", err)
	}
	fixture.clock.Advance(time.Duration(domain.DefaultLeaseSeconds) * time.Second)
	valid := finishInput(claim, domain.AttemptOutcomeCompleted)
	valid.TargetIssueStatus = statusPointer(domain.StatusDone)
	valid.Artifacts = []domain.ArtifactInput{{Type: domain.ArtifactTypeOther, URI: "expired"}}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, valid); !errors.Is(err, &domain.Error{Code: domain.CodeLeaseExpired}) {
		t.Fatalf("boundary finish error = %v", err)
	}
	if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_expired") != 1 ||
		countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed") != 0 {
		t.Fatalf("expiry events = expired %d completed %d",
			countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_expired"),
			countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed"))
	}
	var status string
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.AttemptStatusExpired) {
		t.Fatalf("expired attempt status = %q", status)
	}
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount)
	}); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 0 {
		t.Fatalf("expired attempt attached %d artifacts", artifactCount)
	}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, valid); !errors.Is(err, &domain.Error{Code: domain.CodeAttemptNotActive}) {
		t.Fatalf("second finish after expiry = %v", err)
	}
	if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_expired") != 1 {
		t.Fatal("second finish duplicated expiry event")
	}
}

func TestFinishAttemptChangeAcknowledgementsAndWarnings(t *testing.T) {
	fixture := newAttemptTestFixture(t, "changes")
	defer fixture.close()

	t.Run("description requires exact acknowledgement", func(t *testing.T) {
		issue := createAttemptIssue(t, fixture, "description", domain.StatusReady)
		claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
		if err != nil {
			t.Fatal(err)
		}
		updated, err := fixture.issues.UpdateIssue(fixture.ctx, domain.UpdateIssueInput{
			IssueID: issue.ID, ExpectedVersion: claim.Issue.Version,
			Changes: domain.IssuePatch{Description: domain.OptionalString{Set: true, Value: pointer("new description")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		input := finishInput(claim, domain.AttemptOutcomeCompleted)
		input.TargetIssueStatus = statusPointer(domain.StatusDone)
		input.Artifacts = []domain.ArtifactInput{{
			Type: domain.ArtifactTypeFile, URI: "rejected-description-artifact",
		}}
		finished, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
		requireAcknowledgementError(t, err, "description")
		if finished.Attempt.ID != "" {
			t.Fatal("rejected completion returned an attempt")
		}
		var artifactCount int
		if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
			return query.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE attempt_id = ?`, claim.Attempt.ID).Scan(&artifactCount)
		}); err != nil {
			t.Fatal(err)
		}
		if artifactCount != 0 {
			t.Fatalf("rejected completion persisted %d artifacts", artifactCount)
		}
		requireAttemptActive(t, fixture, claim)
		if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed") != 0 {
			t.Fatal("description rejection appended completion event")
		}
		version, latestEventID := currentIssueVersionAndLatestEvent(t, fixture, issue.ID)
		if version != updated.Issue.Version {
			t.Fatalf("current version = %d, update version = %d", version, updated.Issue.Version)
		}
		input.AcknowledgedChanges = &domain.AttemptAcknowledgement{IssueVersion: version, LatestEventID: latestEventID}
		if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("acceptance criteria rejects missing mismatched and stale acknowledgements", func(t *testing.T) {
		issue := createAttemptIssue(t, fixture, "criteria", domain.StatusReady)
		claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.issues.UpdateIssue(fixture.ctx, domain.UpdateIssueInput{
			IssueID: issue.ID, ExpectedVersion: claim.Issue.Version,
			Changes: domain.IssuePatch{AcceptanceCriteria: domain.OptionalString{Set: true, Value: pointer("new criteria")}},
		}); err != nil {
			t.Fatal(err)
		}
		input := finishInput(claim, domain.AttemptOutcomeCompleted)
		input.TargetIssueStatus = statusPointer(domain.StatusDone)
		if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err == nil {
			t.Fatal("missing acceptance acknowledgement succeeded")
		} else {
			requireAcknowledgementError(t, err, "acceptance_criteria")
		}
		version, latestEventID := currentIssueVersionAndLatestEvent(t, fixture, issue.ID)
		for _, ack := range []*domain.AttemptAcknowledgement{
			{IssueVersion: version - 1, LatestEventID: latestEventID},
			{IssueVersion: version, LatestEventID: latestEventID - 1},
		} {
			input.AcknowledgedChanges = ack
			if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err == nil {
				t.Fatalf("mismatched acknowledgement %+v succeeded", ack)
			} else {
				requireAcknowledgementError(t, err, "acceptance_criteria")
			}
			requireAttemptActive(t, fixture, claim)
			if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed") != 0 {
				t.Fatal("invalid acknowledgement appended completion event")
			}
		}
		input.AcknowledgedChanges = &domain.AttemptAcknowledgement{IssueVersion: version, LatestEventID: latestEventID}
		if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("title priority and labels are warnings", func(t *testing.T) {
		issue := createAttemptIssue(t, fixture, "warnings", domain.StatusReady)
		claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
		if err != nil {
			t.Fatal(err)
		}
		_, err = fixture.issues.UpdateIssue(fixture.ctx, domain.UpdateIssueInput{
			IssueID: issue.ID, ExpectedVersion: claim.Issue.Version, CreateMissingLabels: true,
			Changes: domain.IssuePatch{
				Title:    domain.OptionalValue[string]{Set: true, Value: "renamed"},
				Priority: domain.OptionalValue[domain.Priority]{Set: true, Value: domain.PriorityHigh},
				Labels:   domain.OptionalValue[[]string]{Set: true, Value: []string{"changed-label"}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		input := finishInput(claim, domain.AttemptOutcomeCompleted)
		input.TargetIssueStatus = statusPointer(domain.StatusDone)
		finished, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		wantWarnings := []string{"ISSUE_CHANGED:labels", "ISSUE_CHANGED:priority", "ISSUE_CHANGED:title"}
		if !equalStrings(finished.Warnings, wantWarnings) {
			t.Fatalf("warnings = %v, want %v", finished.Warnings, wantWarnings)
		}
	})

	t.Run("attempt note does not require acknowledgement", func(t *testing.T) {
		issue := createAttemptIssue(t, fixture, "note", domain.StatusReady)
		claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.attempts.SaveAttemptNote(fixture.ctx, domain.SaveAttemptNoteInput{
			AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
			Kind: domain.AttemptNoteKindProgress, Content: "progress",
		}); err != nil {
			t.Fatal(err)
		}
		input := finishInput(claim, domain.AttemptOutcomeCompleted)
		input.TargetIssueStatus = statusPointer(domain.StatusDone)
		finished, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(finished.Warnings) != 0 {
			t.Fatalf("note completion warnings = %v", finished.Warnings)
		}
	})
}

func currentIssueVersionAndLatestEvent(t *testing.T, fixture *attemptTestFixture, issueID string) (int64, int64) {
	t.Helper()
	var version, latestEventID int64
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT version FROM issues WHERE id = ?`, issueID).Scan(&version); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventID)
	}); err != nil {
		t.Fatal(err)
	}
	return version, latestEventID
}

func requireAcknowledgementError(t *testing.T, err error, field string) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeIssueChangedDuringAttempt ||
		!domainErr.Retryable || len(domainErr.Details) != 1 || domainErr.Details[0].Field != field {
		t.Fatalf("acknowledgement error = %#v", err)
	}
}

func TestFinishAttemptBlockerAndCompletionUpdateRace(t *testing.T) {
	fixture := newAttemptTestFixture(t, "race")
	defer fixture.close()

	t.Run("blocker added after claim", func(t *testing.T) {
		target := createAttemptIssue(t, fixture, "blocked target", domain.StatusReady)
		claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: target.ID})
		if err != nil {
			t.Fatal(err)
		}
		blocker := createAttemptIssue(t, fixture, "unresolved blocker", domain.StatusReady)
		if _, err := fixture.relations.ManageIssueRelation(fixture.ctx, domain.ManageIssueRelationInput{
			Action: domain.RelationActionAdd, SourceIssueID: blocker.ID, TargetIssueID: target.ID,
			RelationType: domain.RelationTypeBlocks,
		}); err != nil {
			t.Fatal(err)
		}
		input := finishInput(claim, domain.AttemptOutcomeCompleted)
		input.TargetIssueStatus = statusPointer(domain.StatusDone)
		if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); !errors.Is(err, &domain.Error{Code: domain.CodeUnresolvedBlockersAdded}) {
			t.Fatalf("blocker completion error = %v", err)
		} else {
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || !domainErr.Retryable {
				t.Fatalf("blocker error is not retryable: %v", err)
			}
		}
		requireAttemptActive(t, fixture, claim)
		if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed") != 0 {
			t.Fatal("blocker rejection appended completion event")
		}
	})

	t.Run("completion and optimistic update have one winner", func(t *testing.T) {
		issue := createAttemptIssue(t, fixture, "race target", domain.StatusReady)
		claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		type raceResult struct {
			finish bool
			err    error
		}
		results := make(chan raceResult, 2)
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			input := finishInput(claim, domain.AttemptOutcomeCompleted)
			input.TargetIssueStatus = statusPointer(domain.StatusDone)
			_, err := fixture.attempts.FinishAttempt(fixture.ctx, input)
			results <- raceResult{finish: true, err: err}
		}()
		go func() {
			defer group.Done()
			<-start
			_, err := fixture.issues.UpdateIssue(fixture.ctx, domain.UpdateIssueInput{
				IssueID: issue.ID, ExpectedVersion: claim.Issue.Version,
				Changes: domain.IssuePatch{
					Title:       domain.OptionalValue[string]{Set: true, Value: "race updated"},
					Description: domain.OptionalString{Set: true, Value: pointer("race description")},
				},
			})
			results <- raceResult{err: err}
		}()
		close(start)
		group.Wait()
		close(results)
		var finishErr, updateErr error
		for result := range results {
			if result.finish {
				finishErr = result.err
			} else {
				updateErr = result.err
			}
		}
		finishSucceeded := finishErr == nil
		updateSucceeded := updateErr == nil
		if finishSucceeded == updateSucceeded {
			t.Fatalf("race outcomes: finish=%v update=%v", finishErr, updateErr)
		}
		if finishSucceeded {
			if !errors.Is(updateErr, &domain.Error{Code: domain.CodeVersionConflict}) {
				t.Fatalf("update loser error = %v", updateErr)
			}
			var status, title string
			if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
				if err := query.QueryRowContext(ctx, `SELECT status, title FROM issues WHERE id = ?`, issue.ID).Scan(&status, &title); err != nil {
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if status != string(domain.StatusDone) || title != "race target" {
				t.Fatalf("completion winner state = status %q title %q", status, title)
			}
			if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed") != 1 {
				t.Fatal("completion winner did not persist one completion event")
			}
			requireAttemptInactive(t, fixture, claim)
		} else {
			if !errors.Is(finishErr, &domain.Error{Code: domain.CodeIssueChangedDuringAttempt}) {
				t.Fatalf("finish loser error = %v", finishErr)
			}
			var domainErr *domain.Error
			if !errors.As(finishErr, &domainErr) || !domainErr.Retryable {
				t.Fatalf("finish loser error is not retryable: %v", finishErr)
			}
			var status, title string
			var description sql.NullString
			if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
				if err := query.QueryRowContext(ctx, `SELECT status, title, description FROM issues WHERE id = ?`, issue.ID).Scan(&status, &title, &description); err != nil {
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if status != string(domain.StatusReady) || title != "race updated" ||
				!description.Valid || description.String != "race description" {
				t.Fatalf("update winner state = status %q title %q description %#v", status, title, description)
			}
			if countAttemptEvents(t, fixture, claim.Attempt.ID, "attempt_completed") != 0 {
				t.Fatal("update winner persisted completion event")
			}
			requireAttemptActive(t, fixture, claim)
		}
	})
}
