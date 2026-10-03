package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// BenchmarkWriterContention runs an independently opened writer against local
// writers and WAL-compatible reads. Use -benchtime=1x to keep the lock window
// and the read latency distribution representative of one contention burst.
func BenchmarkWriterContention(b *testing.B) {
	for _, writers := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("writers=%d", writers), func(b *testing.B) {
			ctx := context.Background()
			path := filepath.Join(b.TempDir(), "contention.db")
			db, err := Open(ctx, path, Options{})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = db.Close(context.Background()) })
			if _, err := db.pool.ExecContext(ctx, "CREATE TABLE contention (id INTEGER PRIMARY KEY)"); err != nil {
				b.Fatal(err)
			}
			external, err := sql.Open(driverName, dataSourceName(path))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = external.Close() })
			externalConn, err := external.Conn(ctx)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = externalConn.Close() })

			var readTimes, acquisitionTimes, holdTimes []time.Duration
			var poolWait time.Duration
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				if _, err := externalConn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
					b.Fatal(err)
				}
				before := db.pool.Stats().WaitDuration
				writerErrors := make(chan error, writers)
				writerMetrics := make(chan writerTiming, writers)
				var wg sync.WaitGroup
				for i := 0; i < writers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						start := time.Now()
						var acquired time.Time
						err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
							acquired = time.Now()
							_, err := tx.ExecContext(ctx, "INSERT INTO contention DEFAULT VALUES")
							return err
						})
						finished := time.Now()
						if err == nil {
							writerErrors <- nil
							// Each goroutine owns these two values until the group completes.
							writerMetrics <- writerTiming{acquired.Sub(start), finished.Sub(acquired)}
						} else {
							writerErrors <- err
						}
					}()
				}
				time.Sleep(10 * time.Millisecond)
				readCtx, cancel := context.WithCancel(ctx)
				type readResult struct {
					samples []time.Duration
					err     error
				}
				readDone := make(chan readResult, 1)
				go func() {
					var samples []time.Duration
					for readCtx.Err() == nil {
						start := time.Now()
						err := db.Read(readCtx, func(ctx context.Context, q Queryer) error {
							var n int
							return q.QueryRowContext(ctx, "SELECT count(*) FROM contention").Scan(&n)
						})
						if err != nil {
							if readCtx.Err() == nil {
								readDone <- readResult{err: err}
								return
							}
							break
						}
						samples = append(samples, time.Since(start))
						select {
						case <-readCtx.Done():
						case <-time.After(5 * time.Millisecond):
						}
					}
					readDone <- readResult{samples: samples}
				}()
				time.Sleep(140 * time.Millisecond)
				if _, err := externalConn.ExecContext(ctx, "COMMIT"); err != nil {
					cancel()
					b.Fatal(err)
				}
				wg.Wait()
				cancel()
				reads := <-readDone
				if reads.err != nil {
					b.Fatal(reads.err)
				}
				readTimes = append(readTimes, reads.samples...)
				for range writers {
					if err := <-writerErrors; err != nil {
						b.Fatal(err)
					}
					timing := <-writerMetrics
					acquisitionTimes = append(acquisitionTimes, timing.acquire)
					holdTimes = append(holdTimes, timing.hold)
				}
				poolWait += db.pool.Stats().WaitDuration - before
			}
			b.StopTimer()
			if len(readTimes) == 0 {
				b.Fatal("no successful reads during writer contention")
			}
			b.ReportMetric(float64(len(readTimes))/float64(b.N), "reads/op")
			b.ReportMetric(float64(poolWait.Nanoseconds())/float64(b.N), "pool-wait-ns/op")
			b.ReportMetric(float64(percentile(readTimes, 95).Nanoseconds()), "read-p95-ns")
			b.ReportMetric(float64(percentile(readTimes, 99).Nanoseconds()), "read-p99-ns")
			b.ReportMetric(float64(percentile(acquisitionTimes, 95).Nanoseconds()), "acquire-p95-ns")
			b.ReportMetric(float64(percentile(holdTimes, 95).Nanoseconds()), "hold-p95-ns")
		})
	}
}

type writerTiming struct {
	acquire time.Duration
	hold    time.Duration
}

func percentile(samples []time.Duration, rank int) time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[(len(samples)*rank+99)/100-1]
}
