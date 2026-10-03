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
	"rhizome-mcp/internal/pagination"
)

type searchPageBenchmarkCursor struct {
	Score      float64 `json:"score"`
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
}

func BenchmarkSearchPage(b *testing.B) {
	for _, documents := range []int{1000, 100000} {
		for _, padding := range []int{16, 512} {
			b.Run(fmt.Sprintf("documents=%d/padding=%d", documents, padding), func(b *testing.B) {
				ctx := context.Background()
				path := filepath.Join(b.TempDir(), "pages.db")
				db, err := sqlite.Open(ctx, path, sqlite.Options{})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = db.Close(context.Background()) })
				if _, err := migrations.Migrate(ctx, db, clock.NewFakeClock(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))); err != nil {
					b.Fatal(err)
				}
				if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
					if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id,sequence_no,type,title,status,priority,version,created_at,updated_at)
					 VALUES('01ARZ3NDEKTSV4RRFFQ69G5FAV',1,'task','search benchmark source','ready','medium',1,'2026-10-03T00:00:00.000000000Z','2026-10-03T00:00:00.000000000Z')`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, fmt.Sprintf(`WITH RECURSIVE documents(number) AS (VALUES(1) UNION ALL SELECT number+1 FROM documents WHERE number < %d)
					 INSERT INTO comments(id,issue_id,content,created_at)
					 SELECT printf('%%026d',number),'01ARZ3NDEKTSV4RRFFQ69G5FAV','commonneedle ' || CASE WHEN number <= 100 THEN 'rareneedle ' ELSE '' END || ?,'2026-10-03T00:00:00.000000000Z' FROM documents`, documents), strings.Repeat("padding ", padding))
					return err
				}); err != nil {
					b.Fatal(err)
				}
				repository, err := sqlite.NewSearchRepository(db)
				if err != nil {
					b.Fatal(err)
				}
				for _, term := range []string{"commonneedle", "rareneedle"} {
					for _, deep := range []bool{false, true} {
						b.Run(fmt.Sprintf("%s/deep=%t", term, deep), func(b *testing.B) {
							input := domain.SearchInput{Query: term, Limit: 20, SnippetLength: 300}
							var matches int
							var after *searchPageBenchmarkCursor
							if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
								if err := query.QueryRowContext(ctx, `SELECT count(*) FROM search_index WHERE search_index MATCH ?`, term).Scan(&matches); err != nil {
									return err
								}
								if deep {
									cursor := searchPageBenchmarkCursor{}
									if err := query.QueryRowContext(ctx, `SELECT bm25(search_index) AS score,entity_type,entity_id FROM search_index WHERE search_index MATCH ? ORDER BY score,entity_type,entity_id LIMIT 1 OFFSET ?`, term, matches*9/10-1).Scan(&cursor.Score, &cursor.EntityType, &cursor.EntityID); err != nil {
										return err
									}
									encoded, err := pagination.NewCodec[searchPageBenchmarkCursor](0).Encode(cursor)
									if err != nil {
										return err
									}
									input.Cursor = encoded
									after = &cursor
								}
								return nil
							}); err != nil {
								b.Fatal(err)
							}
							var page domain.SearchPage
							b.ReportAllocs()
							b.ResetTimer()
							for iteration := 0; iteration < b.N; iteration++ {
								page, err = repository.Search(ctx, portsSearch(input))
								if err != nil {
									b.Fatal(err)
								}
							}
							b.StopTimer()
							want := 20
							if deep && matches/10 < want {
								want = matches / 10
							}
							if len(page.Results) != want || page.HasMore != (!deep || matches/10 > 20) {
								b.Fatalf("unexpected page: %#v", page)
							}
							for _, item := range page.Results {
								if !strings.Contains(item.Snippet, "["+term+"]") || len([]rune(item.Snippet)) > 300 {
									b.Fatalf("unexpected snippet: %q", item.Snippet)
								}
							}
							b.ReportMetric(float64(matches), "matches/query")
							searchPageBenchmarkDiagnostics(b, path, term, after)
						})
					}
				}
			})
		}
	}
}

func searchPageBenchmarkStatement(term string, after *searchPageBenchmarkCursor, optimized bool) string {
	where := ""
	if after != nil {
		where = fmt.Sprintf(` WHERE score > %.17g OR (score = %.17g AND (entity_type > '%s' OR (entity_type = '%s' AND entity_id > '%s')))`, after.Score, after.Score, after.EntityType, after.EntityType, after.EntityID)
	}
	filter := fmt.Sprintf(` FROM search_index LEFT JOIN issues ON issues.id = search_index.issue_id WHERE search_index MATCH '%s' AND (search_index.issue_id IS NULL OR issues.archived_at IS NULL)`, term)
	if !optimized {
		return `WITH matches AS MATERIALIZED (SELECT search_index.entity_type,search_index.entity_id,search_index.issue_id,search_index.title,substr(snippet(search_index,-1,'[',']','...',64),1,300) AS snippet,bm25(search_index) AS score` + filter + `) SELECT entity_type,entity_id,issue_id,title,snippet,score FROM matches` + where + ` ORDER BY score,entity_type,entity_id LIMIT 21;`
	}
	return `WITH matches AS MATERIALIZED (SELECT search_index.rowid AS fts_rowid,search_index.entity_type,search_index.entity_id,bm25(search_index) AS score` + filter + `), page AS MATERIALIZED (SELECT * FROM matches` + where + ` ORDER BY score,entity_type,entity_id LIMIT 21)
	 SELECT page.entity_type,page.entity_id,search_index.issue_id,search_index.title,substr(snippet(search_index,-1,'[',']','...',64),1,300),page.score FROM page CROSS JOIN search_index
	 WHERE search_index.rowid = page.fts_rowid AND search_index MATCH '` + term + `' ORDER BY page.score,page.entity_type,page.entity_id;`
}

func searchPageBenchmarkDiagnostics(b *testing.B, path, term string, after *searchPageBenchmarkCursor) {
	b.Helper()
	statement := searchPageBenchmarkStatement(term, after, os.Getenv("RHIZOME_SEARCH_BENCH_PLAN") != "legacy")
	output, err := exec.Command("sqlite3", "-batch", path, "PRAGMA temp_store=MEMORY;", ".eqp on", ".stats on", statement).CombinedOutput()
	if err != nil {
		b.Fatalf("search plan/heap probe: %v: %s", err, output)
	}
	var current, peak int
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "Memory Used:") {
			_, err = fmt.Sscanf(line, "Memory Used: %d (max %d) bytes", &current, &peak)
			if err != nil {
				b.Fatal(err)
			}
		}
		if strings.Contains(line, "MATERIALIZE") || strings.Contains(line, "VIRTUAL TABLE INDEX") || strings.Contains(line, "TEMP B-TREE") {
			b.Log(line)
		}
	}
	if peak == 0 {
		b.Fatal("SQLite CLI did not report peak memory")
	}
	b.ReportMetric(float64(peak), "cli-heap-peak-B")
	b.ReportMetric(float64(peak-current), "cli-transient-peak-B")
	b.Log("CLI heap/transient high-water measurements include in-memory temporary results and other transient SQLite allocations; not temp-only or modernc driver counters")
}

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
