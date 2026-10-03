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

// BenchmarkPlanValidation measures complete apply calls (including transaction
// acquisition and commit) while a separate database handle holds the writer lock.
// Use -benchtime=1x -count=3 for the 100k-edge cases.
func BenchmarkPlanValidation(b *testing.B) {
	for _, edges := range []int{1000, 100000} {
		for _, proposed := range []int{0, 1, 100} {
			b.Run(fmt.Sprintf("edges=%d/blocks=%d", edges, proposed), func(b *testing.B) {
				ctx := context.Background()
				path := filepath.Join(b.TempDir(), "plan.db")
				db, err := sqlite.Open(ctx, path, sqlite.Options{})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = db.Close(context.Background()) })
				now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
				if _, err := migrations.Migrate(ctx, db, clock.NewFakeClock(now)); err != nil {
					b.Fatal(err)
				}
				maxIssueNumber := edges + 1
				if proposed > 0 {
					maxIssueNumber += 101 * b.N
				}
				if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
					for _, statement := range []string{
						fmt.Sprintf(`WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers WHERE n <= %d)
						 INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
						 SELECT printf('%%026d', n), n, 'task', 'fixture', 'ready', 'medium', 1,
						 '2026-10-03T00:00:00Z', '2026-10-03T00:00:00Z' FROM numbers`, maxIssueNumber),
						fmt.Sprintf(`WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers WHERE n < %d)
						 INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at)
						 SELECT printf('%%026d', n+200000), printf('%%026d', n), printf('%%026d', n+1),
						 'blocks', '2026-10-03T00:00:00Z' FROM numbers`, edges),
						`CREATE TABLE benchmark_writer (n INTEGER NOT NULL)`,
						`INSERT INTO benchmark_writer VALUES (0)`,
						fmt.Sprintf(`INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES
						 ('00000000000000000000999999', %d, '2026-10-03T00:00:00Z', '2026-10-03T00:00:00Z')`, maxIssueNumber+2),
					} {
						if _, err := tx.ExecContext(ctx, statement); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				other, err := sqlite.Open(ctx, path, sqlite.Options{})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = other.Close(context.Background()) })

				repository, err := sqlite.NewPlanningRepository(db)
				if err != nil {
					b.Fatal(err)
				}
				var writeDuration time.Duration
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					plan := domain.IssuePlan{Issues: []domain.PlannedIssue{{
						Ref: "new", Type: domain.TypeTask, Title: "New", Status: domain.StatusReady, Priority: domain.PriorityMedium,
					}}}
					command := ports.ApplyIssuePlanCommand{
						Plan: plan, IdempotencyKey: fmt.Sprintf("plan-%d", i),
						RequestHash: []byte(fmt.Sprintf("hash-%d", i)), IssueIDs: []string{fmt.Sprintf("1%025d", i)},
						LabelIDs: [][]string{{}}, OccurredAt: now,
					}
					for j := 0; j < proposed; j++ {
						command.Plan.Relations = append(command.Plan.Relations, domain.PlannedRelation{
							SourceRef: fmt.Sprintf("%026d", edges+101*i+j+2),
							TargetRef: fmt.Sprintf("%026d", edges+101*i+j+3),
							Type:      domain.RelationTypeBlocks,
						})
						command.RelationIDs = append(command.RelationIDs, fmt.Sprintf("2%025d", 100*i+j))
					}
					locked := make(chan struct{})
					release := make(chan struct{})
					done := make(chan error, 1)
					go func() {
						done <- other.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
							close(locked)
							<-release
							_, err := tx.ExecContext(ctx, `UPDATE benchmark_writer SET n = n + 1`)
							return err
						})
					}()
					select {
					case <-locked:
					case err := <-done:
						b.Fatalf("competing writer did not acquire lock: %v", err)
					}
					go func() {
						time.Sleep(time.Millisecond)
						close(release)
					}()
					start := time.Now()
					result, err := repository.ApplyIssuePlan(ctx, command)
					writeDuration += time.Since(start)
					if err != nil {
						b.Fatal(err)
					}
					if len(result.CreatedRelations) != proposed {
						b.Fatalf("created %d relations, want %d", len(result.CreatedRelations), proposed)
					}
					if err := <-done; err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(writeDuration.Nanoseconds())/float64(b.N), "write-ns/op")
			})
		}
	}
}
