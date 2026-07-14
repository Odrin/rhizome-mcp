package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
)

func TestLoadAgentSessionMapsMalformedStoredIDToStorageCorrupt(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "sessions.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(ctx); err != nil {
			t.Errorf("close session database: %v", err)
		}
	})

	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	const sessionID = "xxxxxxxxxxxxxxxxxxxxxxxxxx"
	timestamp := now.Format(time.RFC3339Nano)
	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE agent_sessions(
			id TEXT PRIMARY KEY,
			client_name TEXT NOT NULL,
			client_version TEXT,
			agent_label TEXT,
			model TEXT,
			instance_key TEXT,
			started_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			ended_at TEXT
		)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(
			id, client_name, client_version, agent_label, model, instance_key,
			started_at, last_seen_at, ended_at
		) VALUES (?, ?, NULL, NULL, NULL, NULL, ?, ?, NULL)`,
			sessionID, "client", timestamp, timestamp)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var loadErr error
	if err := db.Read(ctx, func(ctx context.Context, query Queryer) error {
		_, loadErr = loadAgentSession(ctx, query, sessionID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(loadErr, &domain.Error{Code: domain.CodeStorageCorrupt}) {
		t.Fatalf("error = %v, want %s", loadErr, domain.CodeStorageCorrupt)
	}
}
