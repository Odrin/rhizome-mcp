package runtime_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestProjectIssueTimestampRecovery(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	ctx := context.Background()
	project, err := projectruntime.OpenProject(ctx, projectruntime.Options{
		StartingPath: repository, DataRoot: dataRoot, Clock: clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = project.Close(ctx) }()
	const legacy = "2026-10-13 03:42:33"
	if err := project.Database.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
			VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAV', 1, 'task', 'recover', 'ready', 'medium', 1, ?, ?)`,
			sqlite.FormatStorageTime(testTime), legacy)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, full := range []bool{false, true} {
		report, err := project.Doctor(ctx, projectruntime.DoctorOptions{Full: full})
		if err == nil || report.Healthy() {
			t.Fatalf("Doctor(full=%v) = %+v, %v", full, report, err)
		}
		var found bool
		for _, check := range report.Checks {
			if check.Name == "issue_timestamps" {
				found = true
				if check.Healthy || !strings.Contains(check.Message, "ISSUE-1") ||
					!strings.Contains(check.Message, legacy) || !strings.Contains(check.Message, "normalize-issue-timestamps") {
					t.Fatalf("timestamp check = %+v", check)
				}
			}
		}
		if !found {
			t.Fatal("missing timestamp check")
		}
	}
	if _, err := project.NormalizeIssueTimestamps(ctx, project.DatabasePath); err == nil {
		t.Fatal("normalization succeeded without a new backup")
	}
	backupPath := filepath.Join(t.TempDir(), "before-repair.db")
	report, err := project.NormalizeIssueTimestamps(ctx, backupPath)
	if err != nil || report.BackupPath != backupPath || report.NormalizedCount != 1 {
		t.Fatalf("normalize = %+v, %v", report, err)
	}
	backup, err := sqlite.Open(ctx, backupPath, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close(ctx) }()
	if err := backup.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		var value string
		if err := query.QueryRowContext(ctx, `SELECT updated_at FROM issues`).Scan(&value); err != nil {
			return err
		}
		if value != legacy {
			t.Fatalf("backup lost original timestamp: %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	health, err := project.Doctor(ctx, projectruntime.DoctorOptions{Full: true})
	if err != nil || !health.Healthy() {
		t.Fatalf("doctor after repair = %+v, %v", health, err)
	}
	second, err := project.NormalizeIssueTimestamps(ctx, filepath.Join(t.TempDir(), "repeat.db"))
	if err != nil || second.NormalizedCount != 0 {
		t.Fatalf("repeat normalization = %+v, %v", second, err)
	}
}

func TestProjectIssueTimestampRecoveryRetainsBackupOnFailure(t *testing.T) {
	repository, dataRoot := initializeProject(t)
	ctx := context.Background()
	project, err := projectruntime.OpenProject(ctx, projectruntime.Options{
		StartingPath: repository, DataRoot: dataRoot, Clock: clock.NewFakeClock(testTime),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = project.Close(ctx) }()
	if err := project.Database.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
			VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAV', 1, 'task', 'invalid', 'ready', 'medium', 1, ?, 'bad timestamp')`,
			sqlite.FormatStorageTime(testTime))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "failed-recovery.db")
	report, err := project.NormalizeIssueTimestamps(ctx, path)
	if err == nil || report.NormalizedCount != 0 || report.BackupPath != path || !strings.Contains(err.Error(), fmt.Sprintf("%q", path)) {
		t.Fatalf("normalize = %+v, %v", report, err)
	}
	backup, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatalf("backup not retained: %v", err)
	}
	if err := backup.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
