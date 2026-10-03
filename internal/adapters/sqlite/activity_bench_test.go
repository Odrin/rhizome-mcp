package sqlite_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

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
