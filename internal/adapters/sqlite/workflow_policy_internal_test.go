package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// openGateSnapshotTestDB builds a minimal schema for insertAttemptGateSnapshot
// and insertReviewTargetGateSnapshot: a package-internal test file cannot
// import internal/migrations (it imports this package, which would cycle),
// so this hand-creates the exact attempt_gate_snapshots/
// review_target_gate_snapshots table and trigger definitions from
// internal/migrations/sql/009_workflow_gates.sql, plus minimal shadow
// work_attempts/review_targets tables satisfying their foreign keys --
// mirroring the same workaround TestLoadAgentSessionMapsMalformedStoredIDToStorageCorrupt
// uses in sessions_internal_test.go.
func openGateSnapshotTestDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "gate-snapshots.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(ctx); err != nil {
			t.Errorf("close gate snapshot database: %v", err)
		}
	})
	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		statements := []string{
			`CREATE TABLE work_attempts (id TEXT PRIMARY KEY)`,
			`CREATE TABLE review_targets (id TEXT PRIMARY KEY)`,
			`CREATE TABLE attempt_gate_snapshots (
				attempt_id TEXT PRIMARY KEY REFERENCES work_attempts(id),
				requirements_json TEXT NOT NULL CHECK (json_valid(requirements_json)),
				source_policies_json TEXT NOT NULL CHECK (json_valid(source_policies_json)),
				fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
				issue_version INTEGER NOT NULL CHECK (issue_version >= 1),
				created_at TEXT NOT NULL
			) STRICT`,
			`CREATE TRIGGER attempt_gate_snapshots_immutable_update
			BEFORE UPDATE ON attempt_gate_snapshots
			BEGIN SELECT RAISE(ABORT, 'attempt gate snapshots are immutable'); END`,
			`CREATE TRIGGER attempt_gate_snapshots_immutable_delete
			BEFORE DELETE ON attempt_gate_snapshots
			BEGIN SELECT RAISE(ABORT, 'attempt gate snapshots are immutable'); END`,
			`CREATE TABLE review_target_gate_snapshots (
				target_id TEXT PRIMARY KEY REFERENCES review_targets(id),
				requirements_json TEXT NOT NULL CHECK (json_valid(requirements_json)),
				source_policies_json TEXT NOT NULL CHECK (json_valid(source_policies_json)),
				fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
				issue_version INTEGER NOT NULL CHECK (issue_version >= 1),
				created_at TEXT NOT NULL
			) STRICT`,
			`CREATE TRIGGER review_target_gate_snapshots_immutable_update
			BEFORE UPDATE ON review_target_gate_snapshots
			BEGIN SELECT RAISE(ABORT, 'review target gate snapshots are immutable'); END`,
			`CREATE TRIGGER review_target_gate_snapshots_immutable_delete
			BEFORE DELETE ON review_target_gate_snapshots
			BEGIN SELECT RAISE(ABORT, 'review target gate snapshots are immutable'); END`,
			`INSERT INTO work_attempts(id) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FD0')`,
			`INSERT INTO review_targets(id) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FD1')`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed gate snapshot schema: %v", err)
	}
	return db
}

func testGateSnapshot(t *testing.T, now time.Time) domain.GateSnapshot {
	t.Helper()
	requirements := []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FD2", Key: "acceptance_criteria", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
	}
	sources := []domain.SourcePolicyRef{{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FD2", Version: 1}}
	snapshot, err := domain.NewGateSnapshot(requirements, sources, 3, now)
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	return snapshot
}

func TestInsertAttemptGateSnapshotRoundTripsAndIsImmutable(t *testing.T) {
	db := openGateSnapshotTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snapshot := testGateSnapshot(t, now)

	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		return insertAttemptGateSnapshot(ctx, tx, "01ARZ3NDEKTSV4RRFFQ69G5FD0", snapshot)
	}); err != nil {
		t.Fatalf("insertAttemptGateSnapshot() error = %v", err)
	}

	loaded, err := (&WorkflowPolicyRepository{db: db}).GetAttemptGateSnapshot(ctx, ports.GetAttemptGateSnapshotCommand{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FD0"})
	if err != nil {
		t.Fatalf("GetAttemptGateSnapshot() error = %v", err)
	}
	if loaded.Fingerprint != snapshot.Fingerprint || loaded.IssueVersion != snapshot.IssueVersion {
		t.Fatalf("loaded = %#v, want it to match the inserted snapshot %#v", loaded, snapshot)
	}
	if len(loaded.Requirements) != 1 || loaded.Requirements[0].PolicyID != "01ARZ3NDEKTSV4RRFFQ69G5FD2" {
		t.Fatalf("loaded requirements = %#v, want the policy ID preserved (a snapshot spans multiple source policies, unlike a workflow_policies row)", loaded.Requirements)
	}

	// Immutability: neither UPDATE nor DELETE may succeed once written.
	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE attempt_gate_snapshots SET issue_version = 99 WHERE attempt_id = ?`, "01ARZ3NDEKTSV4RRFFQ69G5FD0")
		return err
	}); err == nil {
		t.Fatal("UPDATE on attempt_gate_snapshots succeeded, want the immutability trigger to reject it")
	}
	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM attempt_gate_snapshots WHERE attempt_id = ?`, "01ARZ3NDEKTSV4RRFFQ69G5FD0")
		return err
	}); err == nil {
		t.Fatal("DELETE on attempt_gate_snapshots succeeded, want the immutability trigger to reject it")
	}
}

func TestInsertReviewTargetGateSnapshotRoundTripsAndIsImmutable(t *testing.T) {
	db := openGateSnapshotTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snapshot := testGateSnapshot(t, now)

	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		return insertReviewTargetGateSnapshot(ctx, tx, "01ARZ3NDEKTSV4RRFFQ69G5FD1", snapshot)
	}); err != nil {
		t.Fatalf("insertReviewTargetGateSnapshot() error = %v", err)
	}

	loaded, err := (&WorkflowPolicyRepository{db: db}).GetReviewTargetGateSnapshot(ctx, ports.GetReviewTargetGateSnapshotCommand{TargetID: "01ARZ3NDEKTSV4RRFFQ69G5FD1"})
	if err != nil {
		t.Fatalf("GetReviewTargetGateSnapshot() error = %v", err)
	}
	if loaded.Fingerprint != snapshot.Fingerprint {
		t.Fatalf("loaded fingerprint = %q, want %q", loaded.Fingerprint, snapshot.Fingerprint)
	}

	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM review_target_gate_snapshots WHERE target_id = ?`, "01ARZ3NDEKTSV4RRFFQ69G5FD1")
		return err
	}); err == nil {
		t.Fatal("DELETE on review_target_gate_snapshots succeeded, want the immutability trigger to reject it")
	}
}

func TestInsertAttemptGateSnapshotRejectsDuplicateAttempt(t *testing.T) {
	db := openGateSnapshotTestDB(t)
	ctx := context.Background()
	snapshot := testGateSnapshot(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		return insertAttemptGateSnapshot(ctx, tx, "01ARZ3NDEKTSV4RRFFQ69G5FD0", snapshot)
	}); err != nil {
		t.Fatalf("insertAttemptGateSnapshot() error = %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		return insertAttemptGateSnapshot(ctx, tx, "01ARZ3NDEKTSV4RRFFQ69G5FD0", snapshot)
	}); err == nil {
		t.Fatal("second insertAttemptGateSnapshot() for the same attempt succeeded, want a primary-key conflict (one immutable row per attempt)")
	}
}
