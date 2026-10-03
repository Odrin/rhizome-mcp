package sqlite_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

// Run with: go test ./internal/adapters/sqlite -run '^$' -bench '^BenchmarkReview' -benchmem -benchtime=30x
func BenchmarkReviewList(b *testing.B) {
	for _, size := range []int{10000, 100000} {
		b.Run(fmt.Sprintf("n=%d", size), func(b *testing.B) {
			db, repo := reviewBenchDB(b, size)
			defer db.Close(context.Background())
			for _, offset := range []int{0, size - 20} {
				b.Run(fmt.Sprintf("offset=%d", offset), func(b *testing.B) {
					query := ports.ListReviewRequestsQuery{Limit: 20, Offset: offset}
					durations := make([]time.Duration, 0, b.N)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						start := time.Now()
						result, err := repo.ListReviewRequests(context.Background(), query)
						durations = append(durations, time.Since(start))
						if err != nil || len(result.Items) != 20 {
							b.Fatalf("list: %v, items=%d", err, len(result.Items))
						}
					}
					reportReviewP95(b, durations)
				})
			}
			b.Run("deep-keyset", func(b *testing.B) {
				query := ports.ListReviewRequestsQuery{
					Limit: 20, Offset: size - 20,
					AfterCreatedAt: sqlite.FormatStorageTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
					AfterID:        reviewBenchID(2*size + 22),
				}
				durations := make([]time.Duration, 0, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					start := time.Now()
					result, err := repo.ListReviewRequests(context.Background(), query)
					durations = append(durations, time.Since(start))
					if err != nil || len(result.Items) != 20 {
						b.Fatalf("keyset: %v, items=%d", err, len(result.Items))
					}
				}
				reportReviewP95(b, durations)
			})
			b.Run("claim-candidate", func(b *testing.B) {
				ctx := context.Background()
				durations := make([]time.Duration, 0, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					start := time.Now()
					var id string
					err := db.Read(ctx, func(ctx context.Context, q sqlite.Queryer) error {
						return q.QueryRowContext(ctx, `SELECT id FROM review_requests WHERE issue_id = ? AND status = 'open' ORDER BY created_at DESC, id DESC LIMIT 1`, reviewBenchID(1)).Scan(&id)
					})
					durations = append(durations, time.Since(start))
					if err != nil || id == "" {
						b.Fatalf("claim candidate: %v", err)
					}
				}
				reportReviewP95(b, durations)
			})
			b.Run("claim", func(b *testing.B) {
				ctx := context.Background()
				attemptID := reviewBenchID(3*size + 2)
				now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
					_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
						id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
						lease_token_hash, lease_expires_at, started_at, last_heartbeat_at)
						VALUES (?, ?, 'review', 'active', ?, 0, X'01', ?, ?, ?)
						ON CONFLICT(id) DO NOTHING`,
						attemptID, reviewBenchID(1), size, sqlite.FormatStorageTime(now.Add(time.Hour)), sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
					return err
				}); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				durations := make([]time.Duration, 0, b.N)
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
						_, err := tx.ExecContext(ctx, `UPDATE review_requests SET status='open', active_attempt_id=NULL, version=1 WHERE id=?`,
							reviewBenchID(3*size+1))
						return err
					}); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
					start := time.Now()
					_, err := repo.ClaimReviewRequest(ctx, ports.ReviewMutationCommand{
						RequestID: reviewBenchID(3*size + 1), ExpectedVersion: 1, ActiveAttemptID: &attemptID, OccurredAt: now,
					})
					durations = append(durations, time.Since(start))
					if err != nil {
						b.Fatal(err)
					}
				}
				reportReviewP95(b, durations)
			})
		})
	}
}

func reportReviewP95(b *testing.B, durations []time.Duration) {
	slices.Sort(durations)
	b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns")
}

func reviewBenchID(n int) string { return fmt.Sprintf("%026d", n) }

func reviewBenchDB(b *testing.B, size int) (*sqlite.DB, *sqlite.ReviewRepository) {
	b.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(b.TempDir(), "reviews.db"), sqlite.Options{})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := migrations.Migrate(ctx, db, clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))); err != nil {
		b.Fatal(err)
	}
	err = db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		now := sqlite.FormatStorageTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
			VALUES (?, 1, 'task', 'benchmark', 'review', 'medium', ?, ?, ?)`, reviewBenchID(1), size, now, now); err != nil {
			return err
		}
		for i := 1; i <= size; i++ {
			target := reviewBenchID(size + i + 1)
			request := reviewBenchID(2*size + i + 1)
			// Timestamps deliberately tie in groups of 1000; the ID is the ordering tie-breaker.
			created := sqlite.FormatStorageTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i/1000) * time.Second))
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at)
				VALUES (?, ?, ?, 0, '[]', '["implementation"]', 1, ?)`, target, reviewBenchID(1), i, created); err != nil {
				return err
			}
			status, resolved := string(domain.ReviewRequestStatusApproved), any(created)
			if i == size {
				status, resolved = "open", nil
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, version, created_at, resolved_at)
				VALUES (?, ?, ?, ?, 0, '[]', '["implementation"]', ?, 1, ?, ?)`,
				request, target, reviewBenchID(1), i, status, created, resolved); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	repo, err := sqlite.NewReviewRepository(db)
	if err != nil {
		b.Fatal(err)
	}
	return db, repo
}
