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
	"rhizome-mcp/internal/migrations"
)

func BenchmarkSearchUpdate(b *testing.B) {
	for _, documents := range []int{1000, 100000} {
		for _, mutation := range []string{"NonText", "Rename", "Reservation", "Evidence"} {
			b.Run(fmt.Sprintf("%s/documents=%d", mutation, documents), func(b *testing.B) {
				ctx := context.Background()
				path := filepath.Join(b.TempDir(), "search.db")
				db, err := sqlite.Open(ctx, path, sqlite.Options{})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = db.Close(context.Background()) })
				if _, err := migrations.Migrate(ctx, db, clock.NewFakeClock(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))); err != nil {
					b.Fatal(err)
				}
				if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
					for _, statement := range []string{
						`INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
						 VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAV', 1, 'task', 'benchmark title', 'ready', 'medium', 1, '2026-09-30T00:00:00.000000000Z', '2026-09-30T00:00:00.000000000Z')`,
						`INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at)
						 VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAW', '01ARZ3NDEKTSV4RRFFQ69G5FAV', 'work', 'active', 1, 0, X'01', '2026-10-01T00:00:00.000000000Z', '2026-09-30T00:00:00.000000000Z', '2026-09-30T00:00:00.000000000Z')`,
						`INSERT INTO resource_reservations(id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json, status, version, created_at)
						 VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAX', '01ARZ3NDEKTSV4RRFFQ69G5FAV', '01ARZ3NDEKTSV4RRFFQ69G5FAW', 'file', 'benchmark file', 'benchmark file', '{}', 'active', 1, '2026-09-30T00:00:00.000000000Z')`,
						`INSERT INTO gate_evidence(id, attempt_id, issue_id, key, result, summary, artifact_ids_json, version, created_at, updated_at)
						 VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAY', '01ARZ3NDEKTSV4RRFFQ69G5FAW', '01ARZ3NDEKTSV4RRFFQ69G5FAV', 'benchmark', 'satisfied', 'benchmark summary', '[]', 1, '2026-09-30T00:00:00.000000000Z', '2026-09-30T00:00:00.000000000Z')`,
						fmt.Sprintf(`WITH RECURSIVE documents(number) AS (VALUES(1) UNION ALL SELECT number+1 FROM documents WHERE number < %d)
						 INSERT INTO comments(id, issue_id, content, created_at)
						 SELECT printf('%%026d', number), '01ARZ3NDEKTSV4RRFFQ69G5FAV', 'background searchable document ' || number, '2026-09-30T00:00:00.000000000Z' FROM documents`, documents),
					} {
						if _, err := tx.ExecContext(ctx, statement); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				if err := db.Read(ctx, func(ctx context.Context, connection sqlite.Queryer) error {
					var busy, frames, checkpointed int
					if err := connection.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil {
						return err
					}
					if busy != 0 {
						return fmt.Errorf("checkpoint busy: %d", busy)
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				before := searchBenchmarkWALBytes(b, path)
				var transactionTime time.Duration
				b.ReportAllocs()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					started := time.Now()
					if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
						if _, err := tx.ExecContext(ctx, "PRAGMA wal_autocheckpoint=0"); err != nil {
							return err
						}
						_, err := tx.ExecContext(ctx, searchBenchmarkMutation(mutation, iteration))
						return err
					}); err != nil {
						b.Fatal(err)
					}
					transactionTime += time.Since(started)
				}
				b.StopTimer()
				b.ReportMetric(float64(transactionTime.Nanoseconds())/float64(b.N), "tx-ns/op")
				b.ReportMetric(float64(searchBenchmarkWALBytes(b, path)-before)/float64(b.N), "wal-B/op")
				searchBenchmarkCLISteps(b, path, searchBenchmarkMutation(mutation, b.N))
			})
		}
	}
}

func searchBenchmarkMutation(mutation string, iteration int) string {
	value := iteration % 2
	switch mutation {
	case "NonText":
		priority := []string{"high", "medium"}[value]
		return fmt.Sprintf(`UPDATE issues SET priority='%s', title=title, description=description, version=version+1 WHERE id='01ARZ3NDEKTSV4RRFFQ69G5FAV'`, priority)
	case "Rename":
		return fmt.Sprintf(`UPDATE issues SET title='benchmark title %d', version=version+1 WHERE id='01ARZ3NDEKTSV4RRFFQ69G5FAV'`, value)
	case "Reservation":
		return fmt.Sprintf(`UPDATE resource_reservations SET display_value='benchmark file %d', version=version+1 WHERE id='01ARZ3NDEKTSV4RRFFQ69G5FAX'`, value)
	case "Evidence":
		return fmt.Sprintf(`UPDATE gate_evidence SET summary='benchmark summary %d', version=version+1 WHERE id='01ARZ3NDEKTSV4RRFFQ69G5FAY'`, value)
	default:
		panic("unknown benchmark mutation")
	}
}

func searchBenchmarkWALBytes(b *testing.B, path string) int64 {
	b.Helper()
	info, err := os.Stat(path + "-wal")
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		b.Fatal(err)
	}
	return info.Size()
}

func searchBenchmarkCLISteps(b *testing.B, path, mutation string) {
	b.Helper()
	version, err := exec.Command("sqlite3", ":memory:", "SELECT sqlite_version();").Output()
	if err != nil {
		b.Fatalf("VM-step measurements require sqlite3 CLI: %v", err)
	}
	output, err := exec.Command("sqlite3", "-batch", path, "PRAGMA wal_autocheckpoint=0;", ".stats vmstep", "BEGIN IMMEDIATE; "+mutation+"; COMMIT;").CombinedOutput()
	if err != nil {
		b.Fatalf("SQLite CLI VM-step probe: %v: %s", err, output)
	}
	steps := 0
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, "VM-steps: ") {
			continue
		}
		count, err := strconv.Atoi(strings.TrimPrefix(line, "VM-steps: "))
		if err != nil {
			b.Fatal(err)
		}
		steps += count
	}
	if steps == 0 {
		b.Fatal("SQLite CLI did not expose executed VM-step counts")
	}
	b.ReportMetric(float64(steps), "cli-vmsteps/tx")
	b.Logf("VM steps use SQLite CLI %s, not modernc driver counters; timings/allocations/WAL use Go DB.Write; corpus contains background documents plus three source documents", strings.TrimSpace(string(version)))
}
