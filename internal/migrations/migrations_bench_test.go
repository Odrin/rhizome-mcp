package migrations

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
)

// BenchmarkMigrateOpen measures a current-schema startup scan alongside an
// independent writer process. Run with -benchtime=3x to bound the 1m fixture.
func BenchmarkMigrateOpen(b *testing.B) {
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			b.StopTimer()
			path := b.TempDir() + "/tasks.db"
			db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := Migrate(context.Background(), db, clock.NewFakeClock(migrationTime)); err != nil {
				b.Fatal(err)
			}
			if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
				if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at)
					VALUES ('00000000000000000000000001', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
					return err
				}
				for start := 1; start <= size; start += 10_000 {
					end := min(start+9_999, size)
					if _, err := tx.ExecContext(ctx, `WITH RECURSIVE n(x) AS (
						SELECT ? UNION ALL SELECT x+1 FROM n WHERE x < ?
					) INSERT INTO issue_events(event_type, payload, created_at)
					SELECT 'fixture', '{}', '2026-01-01T00:00:00Z' FROM n`, start, end); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			if err := db.Close(context.Background()); err != nil {
				b.Fatal(err)
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestMigrationBenchmarkWriter$")
			cmd.Env = append(os.Environ(), "RHIZOME_BENCH_WRITER="+path)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				b.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				b.Fatal(err)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				b.Fatal(err)
			}
			reader := bufio.NewReader(stdout)
			if line, err := reader.ReadString('\n'); err != nil || line != "READY\n" {
				b.Fatalf("writer readiness = %q, %v; stderr: %s", line, err, stderr.String())
			}
			b.Cleanup(func() {
				_ = stdin.Close()
				if cmd.ProcessState == nil {
					_ = cmd.Wait()
				}
			})

			b.ReportAllocs()
			b.StartTimer()
			for i := 0; i < b.N; i++ {
				opened, err := sqlite.Open(context.Background(), path, sqlite.Options{})
				if err != nil {
					b.Fatal(err)
				}
				result, err := Migrate(context.Background(), opened, clock.NewFakeClock(migrationTime))
				if err != nil {
					b.Fatal(err)
				}
				if result.Version != CurrentVersion() || result.Applied != 0 {
					b.Fatalf("unexpected migration result: %+v", result)
				}
				if err := opened.Close(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if err := stdin.Close(); err != nil {
				b.Fatal(err)
			}
			var samples []int64
			if err := json.NewDecoder(reader).Decode(&samples); err != nil {
				b.Fatalf("decode writer samples: %v; stderr: %s", err, stderr.String())
			}
			if err := cmd.Wait(); err != nil {
				b.Fatalf("writer exited: %v; stderr: %s", err, stderr.String())
			}
			if len(samples) == 0 {
				b.Fatal("writer did not complete any writes")
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			b.ReportMetric(float64(samples[(99*len(samples)-1)/100]), "writer_p99_ns")
			b.ReportMetric(float64(len(samples)), "writer_samples")
		})
	}
}

func TestMigrationBenchmarkWriter(t *testing.T) {
	path := os.Getenv("RHIZOME_BENCH_WRITER")
	if path == "" {
		t.Skip("benchmark subprocess only")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY")
	stop := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, os.Stdin)
		stop <- err
		close(stop)
	}()
	samples := make([]int64, 0, 1024)
	for {
		select {
		case err := <-stop:
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(os.Stdout).Encode(samples); err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
		start := time.Now()
		if _, err := db.Exec(`UPDATE projects SET updated_at = ? WHERE id = '00000000000000000000000001'`,
			time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, time.Since(start).Nanoseconds())
		select {
		case <-stop:
		case <-time.After(5 * time.Millisecond):
		}
	}
}
