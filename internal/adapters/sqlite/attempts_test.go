package sqlite_test

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
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
)

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
		note, err := service.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
			AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, Kind: kind, Content: content,
			NextSteps: []string{"next " + string(kind)}, Important: kind == domain.AttemptNoteKindCheckpoint,
		})
		if err != nil || note.ID == "" || note.CreatedAt != source.Now() || note.Kind != kind {
			t.Fatalf("save %q = %#v, %v", kind, note, err)
		}
		if kind == domain.AttemptNoteKindCheckpoint {
			checkpoint = note
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
