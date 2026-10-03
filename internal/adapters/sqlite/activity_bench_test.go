package sqlite_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

// Compare before and after migration 019 with:
// go test ./internal/adapters/sqlite -run '^$' -bench '^BenchmarkActivityUnrelatedRows$' -benchmem -benchtime=10x
func BenchmarkActivityUnrelatedRows(b *testing.B) {
	for _, count := range []int{1_000, 100_000} {
		b.Run(fmt.Sprintf("unrelated=%d", count), func(b *testing.B) {
			ctx := context.Background()
			path := filepath.Join(b.TempDir(), "activity.db")
			db, err := sqlite.Open(ctx, path, sqlite.Options{})
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close(ctx)
			now := time.Date(2026, 7, 14, 10, 11, 12, 0, time.UTC)
			if _, err := migrations.Migrate(ctx, db, clock.NewFakeClock(now)); err != nil {
				b.Fatal(err)
			}
			targetID, otherID := fmt.Sprintf("%026d", 1), fmt.Sprintf("%026d", 2)
			targetAttempt, otherAttempt := fmt.Sprintf("%026d", 3), fmt.Sprintf("%026d", 4)
			stamp := sqlite.FormatStorageTime(now)
			if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
				for i, issueID := range []string{targetID, otherID} {
					if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
								VALUES (?, ?, 'task', 'benchmark', 'ready', 'medium', 1, ?, ?)`, issueID, i+1, stamp, stamp); err != nil {
						return err
					}
					attemptID := targetAttempt
					if i == 1 {
						attemptID = otherAttempt
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at)
								VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`, attemptID, issueID, stamp, stamp, stamp); err != nil {
						return err
					}
				}
				for i := 0; i < count+2; i++ {
					issueID, attemptID := otherID, otherAttempt
					if i < 2 {
						issueID, attemptID = targetID, targetAttempt
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id, issue_id, type, uri, created_at) VALUES (?, ?, 'file', 'benchmark', ?)`,
						fmt.Sprintf("%026d", i+5), issueID, stamp); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(id, attempt_id, kind, content, important, created_at) VALUES (?, ?, 'progress', 'benchmark', 0, ?)`,
						fmt.Sprintf("%026d", i+count+7), attemptID, stamp); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			repository, err := sqlite.NewActivityRepository(db)
			if err != nil {
				b.Fatal(err)
			}
			for _, category := range []struct {
				name  string
				types []domain.ActivityCategory
				want  int
			}{
				{"artifacts", []domain.ActivityCategory{domain.ActivityCategoryArtifacts}, 2},
				{"notes", []domain.ActivityCategory{domain.ActivityCategoryAttemptNotes}, 2},
				{"combined", []domain.ActivityCategory{domain.ActivityCategoryArtifacts, domain.ActivityCategoryAttemptNotes}, 4},
			} {
				b.Run(category.name, func(b *testing.B) {
					command := ports.GetIssueActivityCommand{Input: domain.GetIssueActivityInput{IssueID: targetID, Types: category.types, Limit: 20}}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						page, err := repository.GetIssueActivity(ctx, command)
						if err != nil || len(page.Items) != category.want {
							b.Fatalf("GetIssueActivity: %v, items=%d, want %d", err, len(page.Items), category.want)
						}
					}
					b.StopTimer()
					if os.Getenv("RHIZOME_ACTIVITY_BENCH_DIAGNOSTICS") == "1" {
						activityBenchmarkDiagnostics(b, path, targetID, category.name)
					}
				})
			}
		})
	}
}

func activityBenchmarkDiagnostics(b *testing.B, path, issueID, category string) {
	b.Helper()
	artifact := fmt.Sprintf(`SELECT 'artifact' AS entity_type, artifacts.id AS entity_id, artifacts.created_at AS occurred_at, 7 AS type_rank, artifacts.id AS sort_id FROM artifacts WHERE artifacts.issue_id = '%s'`, issueID)
	note := fmt.Sprintf(`SELECT 'attempt_note' AS entity_type, attempt_notes.id AS entity_id, attempt_notes.created_at AS occurred_at, 5 AS type_rank, attempt_notes.id AS sort_id FROM work_attempts CROSS JOIN attempt_notes ON attempt_notes.attempt_id = work_attempts.id WHERE work_attempts.issue_id = '%s'`, issueID)
	arms := artifact
	switch category {
	case "notes":
		arms = note
	case "combined":
		arms = note + " UNION ALL " + artifact
	}
	statement := "SELECT entity_type, entity_id, occurred_at, type_rank, sort_id FROM (" + arms + ") AS activity ORDER BY occurred_at DESC, type_rank ASC, sort_id ASC LIMIT 21;"
	output, err := exec.Command("sqlite3", "-batch", path, ".eqp on", ".stats on", statement).CombinedOutput()
	if err != nil {
		b.Fatalf("SQLite CLI activity diagnostics: %v: %s", err, output)
	}
	var steps int
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "Virtual Machine Steps:") {
			steps, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Virtual Machine Steps:")))
			if err != nil {
				b.Fatal(err)
			}
		}
		if strings.Contains(line, "SCAN ") || strings.Contains(line, "SEARCH ") || strings.Contains(line, "TEMP B-TREE") {
			b.Log(line)
		}
	}
	if steps == 0 {
		b.Fatalf("SQLite CLI did not report VM steps: %s", output)
	}
	b.ReportMetric(float64(steps), "cli-vmsteps/query")
}

// Run before and after changes with:
// go test ./internal/adapters/sqlite -run '^$' -bench '^BenchmarkActivity' -benchmem -benchtime=30x
func BenchmarkActivityPage(b *testing.B) {
	for _, mixed := range []bool{false, true} {
		shape := "homogeneous"
		if mixed {
			shape = "mixed"
		}
		for _, size := range []int{1, 20, 100} {
			b.Run(fmt.Sprintf("%s/%d", shape, size), func(b *testing.B) {
				ctx := context.Background()
				db, err := sqlite.Open(ctx, filepath.Join(b.TempDir(), "activity.db"), sqlite.Options{})
				if err != nil {
					b.Fatal(err)
				}
				defer db.Close(ctx)
				now := time.Date(2026, 7, 14, 10, 11, 12, 0, time.UTC)
				if _, err := migrations.Migrate(ctx, db, clock.NewFakeClock(now)); err != nil {
					b.Fatal(err)
				}
				issueID := fmt.Sprintf("%026d", 1)
				timestamp := sqlite.FormatStorageTime(now)
				if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
					if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
						VALUES (?, 1, 'task', 'benchmark', 'ready', 'medium', 1, ?, ?)`, issueID, timestamp, timestamp); err != nil {
						return err
					}
					for i := 0; i < size; i++ {
						if mixed && i%2 == 1 {
							if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (?, 'benchmark', '{"ok":true}', ?)`, issueID, timestamp); err != nil {
								return err
							}
						} else {
							if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, ?, ?)`,
								fmt.Sprintf("%026d", i+2), issueID, fmt.Sprintf("comment %d", i), timestamp); err != nil {
								return err
							}
						}
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				repository, err := sqlite.NewActivityRepository(db)
				if err != nil {
					b.Fatal(err)
				}
				input := domain.GetIssueActivityInput{IssueID: issueID, Limit: size}
				if !mixed {
					input.Types = []domain.ActivityCategory{domain.ActivityCategoryComments}
				} else {
					input.Types = []domain.ActivityCategory{domain.ActivityCategoryComments, domain.ActivityCategoryEvents}
				}
				command := ports.GetIssueActivityCommand{Input: input}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					page, err := repository.GetIssueActivity(ctx, command)
					if err != nil || len(page.Items) != size {
						b.Fatalf("GetIssueActivity: %v, items=%d, want %d", err, len(page.Items), size)
					}
				}
				// Issue resolution and descriptor selection plus one hydration SELECT per represented type.
				selects := 3
				if mixed && size > 1 {
					selects++
				}
				b.ReportMetric(float64(selects), "selects/op")
			})
		}
	}
}
