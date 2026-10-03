package sqlite_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

type measuredGraphRepository struct {
	base  ports.GraphRepository
	stats ports.GraphLoadStats
}

func (repository *measuredGraphRepository) LoadGraph(ctx context.Context, command ports.LoadGraphCommand) (domain.GraphSnapshot, error) {
	command.Stats = &repository.stats
	return repository.base.LoadGraph(ctx, command)
}

func BenchmarkGraphLoadDisconnected(b *testing.B) {
	for _, disconnected := range []int{1000, 10000, 100000} {
		for _, depth := range []int{0, 2} {
			b.Run(fmt.Sprintf("disconnected=%d/depth=%d", disconnected, depth), func(b *testing.B) {
				ctx := context.Background()
				db, err := sqlite.Open(ctx, filepath.Join(b.TempDir(), "graphs.db"), sqlite.Options{})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = db.Close(context.Background()) })
				now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
				if _, err := migrations.Migrate(ctx, db, clock.NewFakeClock(now)); err != nil {
					b.Fatal(err)
				}
				if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
					for _, statement := range []string{
						fmt.Sprintf(`WITH RECURSIVE candidates(number) AS (VALUES(1) UNION ALL SELECT number+1 FROM candidates WHERE number < %d)
						 INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
						 SELECT printf('%%026d', number), number, 'task', 'graph fixture ' || number, 'ready', 'medium', 1,
						 '2026-10-03T00:00:00.000000000Z', '2026-10-03T00:00:00.000000000Z' FROM candidates`, disconnected+3),
						`INSERT INTO labels(id, name, created_at) VALUES ('00000000000000000000000000', 'graph fixture label', '2026-10-03T00:00:00.000000000Z')`,
						`INSERT INTO issue_labels(issue_id, label_id) SELECT id, '00000000000000000000000000' FROM issues`,
						`INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at)
						 VALUES ('00000000000000000000000001', '00000000000000000000000001', '00000000000000000000000002', 'blocks', '2026-10-03T00:00:00.000000000Z'),
						 ('00000000000000000000000002', '00000000000000000000000002', '00000000000000000000000003', 'blocks', '2026-10-03T00:00:00.000000000Z')`,
					} {
						if _, err := tx.ExecContext(ctx, statement); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				base, err := sqlite.NewGraphRepository(db)
				if err != nil {
					b.Fatal(err)
				}
				var measuredBase ports.GraphRepository = base
				if os.Getenv("RHIZOME_GRAPH_BENCH_LEGACY") == "1" {
					measuredBase = legacyGraphRepository{base: base}
				}
				repository := &measuredGraphRepository{base: measuredBase}
				service, err := application.NewGraphService(repository, clock.NewFakeClock(now))
				if err != nil {
					b.Fatal(err)
				}
				cap := depth + 1
				input := domain.GetIssueGraphInput{RootIssueID: "00000000000000000000000001", Depth: &depth, MaxNodes: &cap}
				var result domain.GraphResult
				var snapshotDuration time.Duration
				b.ReportAllocs()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					result, err = service.GetIssueGraph(ctx, input)
					if err != nil {
						b.Fatal(err)
					}
					snapshotDuration += repository.stats.SnapshotDuration
				}
				b.StopTimer()
				if len(result.Nodes) != cap || len(result.Edges) != depth || result.Truncated {
					b.Fatalf("unexpected graph: %#v", result)
				}
				for _, node := range result.Nodes {
					if len(node.Labels) != 1 {
						b.Fatal("missing retained label")
					}
				}
				b.ReportMetric(float64(repository.stats.Queries), "queries/op")
				b.ReportMetric(float64(repository.stats.RowsRead), "rows/op")
				b.ReportMetric(float64(repository.stats.HydratedNodes), "hydrated/op")
				b.ReportMetric(float64(snapshotDuration.Nanoseconds())/float64(b.N), "snapshot-ns/op")
			})
		}
	}
}
