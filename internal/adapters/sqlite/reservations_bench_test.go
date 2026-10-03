package sqlite

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// Compare the candidate shortlist against an exhaustive scan, including the
// earliest conflicting id when rows overlap one another.
func TestReservationCandidateSelectionDifferential(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "candidates.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	generator, err := ids.NewGenerator(clock.NewFakeClock(now), cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newID := func() string {
		id, err := generator.New()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	issueID, attemptID := newID(), newID()
	err = db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE resource_reservations (
							id TEXT PRIMARY KEY, issue_id TEXT, attempt_id TEXT, kind TEXT, display_value TEXT,
							comparison_value TEXT, normalized_json TEXT, status TEXT, version INTEGER,
							created_at TEXT, released_at TEXT, release_reason TEXT)`)
		if err != nil {
			return err
		}
		for i := 0; i < 160; i++ {
			root := fmt.Sprintf("Root%03d", i%37)
			var resource domain.Resource
			switch i % 4 {
			case 0:
				resource = domain.Resource{Kind: domain.ResourceKindFile, Path: fmt.Sprintf("%s/sub/%d", root, i)}
			case 1:
				resource = domain.Resource{Kind: domain.ResourceKindDirectory, Path: fmt.Sprintf("%s/sub/%d", root, i)}
			case 2:
				resource = domain.Resource{Kind: domain.ResourceKindGlob, Path: fmt.Sprintf("%s/*/%d", root, i)}
			default:
				resource = domain.Resource{Kind: domain.ResourceKindLogical, Namespace: "names", Name: fmt.Sprintf("item:%d", i)}
			}
			normalized, err := domain.Normalize(resource)
			if err != nil {
				return err
			}
			identity, err := marshalIdentity(normalized)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations
								(id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json,
								status, version, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'active', 1, ?)`,
				newID(), issueID, attemptID, resource.Kind, normalized.Display(), normalized.Key(),
				string(identity), FormatStorageTime(now)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var exhaustive []reservationRow
	if err := db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var err error
		exhaustive, err = loadActiveReservations(ctx, query)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(286))
	for i := 0; i < 700; i++ {
		root := fmt.Sprintf("rOoT%03d", random.Intn(42))
		var resource domain.Resource
		switch i % 9 {
		case 0:
			resource = domain.Resource{Kind: domain.ResourceKindFile, Path: root + "/sub/2"}
		case 1:
			resource = domain.Resource{Kind: domain.ResourceKindDirectory, Path: root}
		case 2:
			resource = domain.Resource{Kind: domain.ResourceKindDirectory, Path: root + "/sub/2"}
		case 3:
			resource = domain.Resource{Kind: domain.ResourceKindGlob, Path: root + "/**"}
		case 4:
			resource = domain.Resource{Kind: domain.ResourceKindGlob, Path: root + "/*/2"}
		case 5:
			resource = domain.Resource{Kind: domain.ResourceKindGlob, Path: "*/sub/*"}
		case 6:
			resource = domain.Resource{Kind: domain.ResourceKindGlob, Path: "**"}
		case 7:
			resource = domain.Resource{Kind: domain.ResourceKindLogical, Namespace: "names", Name: fmt.Sprintf("item:%d", random.Intn(180))}
		default:
			resource = domain.Resource{Kind: domain.ResourceKindFile, Path: root + "/sub/2:part"}
		}
		normalized, err := domain.Normalize(resource)
		if err != nil {
			t.Fatal(err)
		}
		shape := domain.PrepareOverlap(normalized)
		var selected []reservationRow
		if err := db.Read(ctx, func(ctx context.Context, query Queryer) error {
			var err error
			selected, err = loadCandidateReservations(ctx, query, []domain.PreparedResource{{Resource: normalized}}, []domain.PreparedOverlap{shape})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		selectedIDs := make(map[string]bool, len(selected))
		var gotFirst, wantFirst string
		for _, row := range selected {
			selectedIDs[row.id] = true
			if gotFirst == "" && shape.Overlaps(domain.PrepareOverlap(row.normalized)) {
				gotFirst = row.id
			}
		}
		for _, row := range exhaustive {
			if domain.Overlaps(normalized, row.normalized) {
				if wantFirst == "" {
					wantFirst = row.id
				}
				if !selectedIDs[row.id] {
					t.Fatalf("case %d omitted overlap with %s", i, row.id)
				}
			}
		}
		if gotFirst != wantFirst {
			t.Fatalf("case %d first conflict %q, exhaustive %q", i, gotFirst, wantFirst)
		}
	}
}

// BenchmarkReservationAcquire measures a conflicting write transaction (no
// inserts), including its lock hold and conflict diagnostics. Setup is excluded.
func BenchmarkReservationAcquire(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		for _, width := range []int{1, 50} {
			b.Run(fmt.Sprintf("active=%d/resources=%d", count, width), func(b *testing.B) {
				ctx := context.Background()
				now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
				db, err := Open(ctx, filepath.Join(b.TempDir(), "bench.db"), Options{})
				if err != nil {
					b.Fatal(err)
				}
				defer db.Close(ctx)
				if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
					for _, name := range []string{"001_initial_schema.sql", "011_reservations.sql"} {
						schema, err := os.ReadFile(filepath.Join("..", "..", "migrations", "sql", name))
						if err != nil {
							return err
						}
						if _, err := tx.ExecContext(ctx, string(schema)); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				generator, err := ids.NewGenerator(clock.NewFakeClock(now), cryptorand.Reader)
				if err != nil {
					b.Fatal(err)
				}
				newID := func() string {
					id, err := generator.New()
					if err != nil {
						b.Fatal(err)
					}
					return id
				}
				projectID := newID()
				ownerIssue, ownerAttempt := newID(), newID()
				otherIssue, otherAttempt := newID(), newID()
				err = db.Write(ctx, func(ctx context.Context, tx Executor) error {
					if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at)
						VALUES (?, 3, ?, ?)`, projectID, FormatStorageTime(now), FormatStorageTime(now)); err != nil {
						return err
					}
					for index, pair := range [][2]string{{ownerIssue, ownerAttempt}, {otherIssue, otherAttempt}} {
						if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
							VALUES (?, ?, 'task', 'bench', 'ready', 'medium', 1, ?, ?)`,
							pair[0], index+1, FormatStorageTime(now), FormatStorageTime(now)); err != nil {
							return err
						}
						if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start,
							context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at)
							VALUES (?, ?, 'work', 'active', 1, 0, ?, ?, ?, ?)`,
							pair[1], pair[0], []byte("bench"), FormatStorageTime(now.Add(time.Hour)),
							FormatStorageTime(now), FormatStorageTime(now)); err != nil {
							return err
						}
					}
					for i := 0; i < count; i++ {
						var resource domain.Resource
						switch i % 4 {
						case 0:
							resource = domain.Resource{Kind: domain.ResourceKindFile, Path: fmt.Sprintf("root%05d/item", i)}
						case 1:
							resource = domain.Resource{Kind: domain.ResourceKindDirectory, Path: fmt.Sprintf("root%05d", i)}
						case 2:
							resource = domain.Resource{Kind: domain.ResourceKindGlob, Path: fmt.Sprintf("root%05d/**", i)}
						default:
							resource = domain.Resource{Kind: domain.ResourceKindLogical, Namespace: "bench", Name: fmt.Sprintf("item%05d", i)}
						}
						normalized, err := domain.Normalize(resource)
						if err != nil {
							return err
						}
						identity := map[string]string{}
						if resource.Kind == domain.ResourceKindLogical {
							identity["namespace"], identity["name"] = normalized.Namespace(), normalized.Name()
						} else {
							identity["path"] = normalized.Display()
						}
						payload, err := json.Marshal(identity)
						if err != nil {
							return err
						}
						_, err = tx.ExecContext(ctx, `INSERT INTO resource_reservations
							(id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json,
							 status, version, created_at)
							VALUES (?, ?, ?, ?, ?, ?, ?, 'active', 1, ?)`,
							newID(), ownerIssue, ownerAttempt, resource.Kind,
							normalized.Display(), normalized.Key(), string(payload), FormatStorageTime(now))
						if err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					b.Fatal(err)
				}
				resources := make([]domain.Resource, width)
				for i := range resources {
					switch i % 4 {
					case 0:
						resources[i] = domain.Resource{Kind: domain.ResourceKindFile, Path: fmt.Sprintf("other%05d/item", i)}
					case 1:
						resources[i] = domain.Resource{Kind: domain.ResourceKindDirectory, Path: fmt.Sprintf("other%05d", i)}
					case 2:
						resources[i] = domain.Resource{Kind: domain.ResourceKindGlob, Path: fmt.Sprintf("other%05d/**", i)}
					default:
						resources[i] = domain.Resource{Kind: domain.ResourceKindLogical, Namespace: "bench", Name: fmt.Sprintf("other%05d", i)}
					}
				}
				resources[width-1].Name = fmt.Sprintf("item%05d", count-1-((count-1-3+4)%4))
				resources[width-1].Kind = domain.ResourceKindLogical
				resources[width-1].Namespace = "bench"
				resources[width-1].Path = ""
				inputs := make([]ports.ReservationResourceInput, width)
				for i, resource := range resources {
					inputs[i] = ports.ReservationResourceInput{ID: newID(), Resource: resource}
				}
				raw := make([]domain.Resource, width)
				for i := range inputs {
					raw[i] = inputs[i].Resource
				}
				prepared, err := domain.PrepareReservationRequest(raw)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				var lockDuration time.Duration
				for i := 0; i < b.N; i++ {
					err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
						start := time.Now()
						defer func() { lockDuration += time.Since(start) }()
						_, err := acquireReservationsForAttempt(ctx, tx, prepared, inputs, otherIssue, otherAttempt, nil, now)
						return err
					})
					if !errors.Is(err, &domain.Error{Code: domain.CodeResourceReservationConflict}) {
						b.Fatalf("expected conflict, got %v", err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(lockDuration.Nanoseconds())/float64(b.N), "lock-ns/op")
			})
		}
	}
}
