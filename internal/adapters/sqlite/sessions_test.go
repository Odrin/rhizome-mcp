package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

const sessionTestID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestAgentSessionRepositoryLifecycleSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db := openSessionTestDBWithoutCleanup(t, path)
	repository, err := sqlite.NewAgentSessionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 14, 10, 0, 0, 0, time.FixedZone("test", 2*60*60))
	version, label, model, instance := "1.0", "Luna", "GPT", "worker-1"
	created, err := repository.CreateAgentSession(context.Background(), ports.CreateAgentSessionCommand{Session: domain.AgentSession{
		ID: sessionTestID, ClientName: "  client ", ClientVersion: &version, AgentLabel: &label,
		Model: &model, InstanceKey: &instance, StartedAt: start, LastSeenAt: start,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if created.ClientName != "client" || *created.ClientVersion != "1.0" || !created.StartedAt.Equal(start.UTC()) {
		t.Fatalf("created session = %#v", created)
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	db = openSessionTestDB(t, path)
	repository, err = sqlite.NewAgentSessionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	later := start.Add(time.Hour)
	touched, err := repository.TouchAgentSession(context.Background(), ports.TouchAgentSessionCommand{
		SessionID: sessionTestID, OccurredAt: later,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !touched.LastSeenAt.Equal(later) || touched.EndedAt != nil || *touched.Model != "GPT" {
		t.Fatalf("touched session = %#v", touched)
	}
	ended, err := repository.EndAgentSession(context.Background(), ports.EndAgentSessionCommand{
		SessionID: sessionTestID, OccurredAt: later.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ended.EndedAt == nil || !ended.EndedAt.Equal(later.Add(time.Hour)) || !ended.LastSeenAt.Equal(*ended.EndedAt) {
		t.Fatalf("ended session = %#v", ended)
	}
	repeated, err := repository.EndAgentSession(context.Background(), ports.EndAgentSessionCommand{
		SessionID: sessionTestID, OccurredAt: later.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.EndedAt.Equal(*ended.EndedAt) || !repeated.LastSeenAt.Equal(ended.LastSeenAt) {
		t.Fatalf("repeated end regressed session = %#v", repeated)
	}
	_, err = repository.TouchAgentSession(context.Background(), ports.TouchAgentSessionCommand{
		SessionID: sessionTestID, OccurredAt: later.Add(3 * time.Hour),
	})
	assertSessionCode(t, err, domain.CodeSessionNotActive)
}

func TestAgentSessionRepositoryTouchIsMonotonicAndUnknownCodesAreStable(t *testing.T) {
	db := openSessionTestDB(t, filepath.Join(t.TempDir(), "sessions.db"))
	repository, err := sqlite.NewAgentSessionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	if _, err := repository.CreateAgentSession(context.Background(), ports.CreateAgentSessionCommand{Session: domain.AgentSession{
		ID: sessionTestID, ClientName: "client", StartedAt: start, LastSeenAt: start,
	}}); err != nil {
		t.Fatal(err)
	}
	advanced := start.Add(time.Hour)
	if _, err := repository.TouchAgentSession(context.Background(), ports.TouchAgentSessionCommand{SessionID: sessionTestID, OccurredAt: advanced}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := repository.TouchAgentSession(context.Background(), ports.TouchAgentSessionCommand{SessionID: sessionTestID, OccurredAt: start})
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.LastSeenAt.Equal(advanced) {
		t.Fatalf("touch regressed last_seen_at = %v", unchanged.LastSeenAt)
	}
	_, err = repository.TouchAgentSession(context.Background(), ports.TouchAgentSessionCommand{SessionID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", OccurredAt: start})
	assertSessionCode(t, err, domain.CodeSessionNotFound)
	_, err = repository.EndAgentSession(context.Background(), ports.EndAgentSessionCommand{SessionID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", OccurredAt: start})
	assertSessionCode(t, err, domain.CodeSessionNotFound)
}

func TestAgentSessionRepositoryMapsCorruptProjection(t *testing.T) {
	db := openSessionTestDB(t, filepath.Join(t.TempDir(), "sessions.db"))
	repository, err := sqlite.NewAgentSessionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	if _, err := repository.CreateAgentSession(context.Background(), ports.CreateAgentSessionCommand{Session: domain.AgentSession{
		ID: sessionTestID, ClientName: "client", StartedAt: now, LastSeenAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE agent_sessions SET last_seen_at = 'not-a-timestamp' WHERE id = ?", sessionTestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = repository.EndAgentSession(context.Background(), ports.EndAgentSessionCommand{SessionID: sessionTestID, OccurredAt: now.Add(time.Hour)})
	assertSessionCode(t, err, domain.CodeStorageCorrupt)
}

func TestAgentSessionRepositoryConcurrentTouchAndEndHasValidTerminalState(t *testing.T) {
	db := openSessionTestDB(t, filepath.Join(t.TempDir(), "sessions.db"))
	repository, err := sqlite.NewAgentSessionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	if _, err := repository.CreateAgentSession(context.Background(), ports.CreateAgentSessionCommand{Session: domain.AgentSession{
		ID: sessionTestID, ClientName: "client", StartedAt: start, LastSeenAt: start,
	}}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for i := 1; i <= 12; i++ {
		wait.Add(1)
		go func(offset int) {
			defer wait.Done()
			_, _ = repository.TouchAgentSession(context.Background(), ports.TouchAgentSessionCommand{
				SessionID: sessionTestID, OccurredAt: start.Add(time.Duration(offset) * time.Minute),
			})
		}(i)
	}
	wait.Add(1)
	var endErr error
	go func() {
		defer wait.Done()
		_, endErr = repository.EndAgentSession(context.Background(), ports.EndAgentSessionCommand{
			SessionID: sessionTestID, OccurredAt: start.Add(30 * time.Minute),
		})
	}()
	wait.Wait()
	if endErr != nil {
		t.Fatal(endErr)
	}
	final, err := repository.EndAgentSession(context.Background(), ports.EndAgentSessionCommand{
		SessionID: sessionTestID, OccurredAt: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	if final.EndedAt == nil || final.EndedAt.Before(final.StartedAt) || !final.EndedAt.Equal(final.LastSeenAt) {
		t.Fatalf("invalid concurrent terminal state = %#v", final)
	}
}

func openSessionTestDB(t *testing.T, path string) *sqlite.DB {
	t.Helper()
	db := openSessionTestDBWithoutCleanup(t, path)
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Errorf("close session database: %v", err)
		}
	})
	return db
}

func openSessionTestDBWithoutCleanup(t *testing.T, path string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(context.Background(), db, fixedMigrationClock{now: time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)}); err != nil {
		_ = db.Close(context.Background())
		t.Fatal(err)
	}
	return db
}

func assertSessionCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, &domain.Error{Code: code}) {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
