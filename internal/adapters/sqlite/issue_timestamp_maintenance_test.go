package sqlite_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

func TestIssueTimestampMaintenanceNormalizesRecognizedUTCForms(t *testing.T) {
	for _, input := range []string{
		"2026-10-13 03:42:33",
		"2026-10-13T03:42:33Z",
		"2026-10-13T03:42:33+00:00",
		"2026-10-13T03:42:33.123456789+00:00",
	} {
		t.Run(input, func(t *testing.T) {
			service, db, _ := openIssueService(t)
			ctx := context.Background()
			issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "repair", Status: domain.StatusReady})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
				_, err := tx.ExecContext(ctx, `UPDATE issues SET created_at = ?, updated_at = ?, closed_at = ?, archived_at = ? WHERE id = ?`,
					input, input, input, input, issue.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
				report, err := sqlite.CheckIssueTimestamps(ctx, query)
				if report.NonCanonicalCount != 4 || report.InvalidCount != 0 || len(report.Details) != 4 {
					t.Fatalf("report = %+v", report)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			count, err := db.NormalizeIssueTimestamps(ctx)
			if err != nil || count != 4 {
				t.Fatalf("normalize = %d, %v", count, err)
			}
			parseInput := input
			if input[10] == ' ' {
				parseInput = strings.Replace(input, " ", "T", 1) + "Z"
			}
			instant, err := time.Parse(time.RFC3339Nano, parseInput)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
				var created, updated, closed, archived string
				var version, events int
				if err := query.QueryRowContext(ctx, `SELECT created_at, updated_at, closed_at, archived_at, version FROM issues WHERE id = ?`, issue.ID).
					Scan(&created, &updated, &closed, &archived, &version); err != nil {
					return err
				}
				for _, value := range []string{created, updated, closed, archived} {
					if value != sqlite.FormatStorageTime(instant) {
						t.Fatalf("timestamp = %q", value)
					}
				}
				if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_events WHERE issue_id = ?`, issue.ID).Scan(&events); err != nil {
					return err
				}
				if version != 1 || events != 1 {
					t.Fatalf("version/events changed: %d/%d", version, events)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			count, err = db.NormalizeIssueTimestamps(ctx)
			if err != nil || count != 0 {
				t.Fatalf("repeat normalization = %d, %v", count, err)
			}
		})
	}
}

func TestIssueTimestampMaintenanceRejectsInvalidDataAtomically(t *testing.T) {
	for _, invalid := range []string{
		"not-a-timestamp", "2026-02-30 03:42:33", "2026-10-13 03:42:33.5",
		"2026-10-13T03:42:33+01:00", "2026-10-13T03:42:33.1234567891Z",
		"", " 2026-10-13 03:42:33",
	} {
		t.Run(invalid, func(t *testing.T) {
			service, db, _ := openIssueService(t)
			ctx := context.Background()
			first, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "valid candidate"})
			if err != nil {
				t.Fatal(err)
			}
			second, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "invalid candidate"})
			if err != nil {
				t.Fatal(err)
			}
			const legacy = "2026-10-13 03:42:33"
			if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
				if _, err := tx.ExecContext(ctx, `UPDATE issues SET updated_at = ? WHERE id = ?`, legacy, first.ID); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `UPDATE issues SET updated_at = ? WHERE id = ?`, invalid, second.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			count, err := db.NormalizeIssueTimestamps(ctx)
			var domainErr *domain.Error
			if count != 0 || !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStorageCorrupt {
				t.Fatalf("normalize = %d, %v", count, err)
			}
			if !strings.Contains(domainErr.Details[0].Message, second.ID) {
				t.Fatalf("missing invalid issue ID: %+v", domainErr.Details)
			}
			if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
				var value string
				if err := query.QueryRowContext(ctx, `SELECT updated_at FROM issues WHERE id = ?`, first.ID).Scan(&value); err != nil {
					return err
				}
				if value != legacy {
					t.Fatalf("partial normalization: %q", value)
				}
				report, err := sqlite.CheckIssueTimestamps(ctx, query)
				if report.NonCanonicalCount != 1 || report.InvalidCount != 1 {
					t.Fatalf("report = %+v", report)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIssueTimestampMaintenanceRollsBackWriteFailure(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "rollback"})
	if err != nil {
		t.Fatal(err)
	}
	const legacy = "2026-10-13 03:42:33"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET created_at = ?, updated_at = ? WHERE id = ?`, legacy, legacy, issue.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_timestamp_repair BEFORE UPDATE OF updated_at ON issues BEGIN SELECT RAISE(ABORT, 'test failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := db.NormalizeIssueTimestamps(ctx); err == nil || count != 0 {
		t.Fatalf("normalize = %d, %v", count, err)
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		var value string
		if err := query.QueryRowContext(ctx, `SELECT created_at FROM issues WHERE id = ?`, issue.ID).Scan(&value); err != nil {
			return err
		}
		if value != legacy {
			t.Fatalf("first update not rolled back: %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueTimestampMaintenanceBoundsDiagnosticsAndPreservesNulls(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "bounded diagnostics"})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `UPDATE issues SET updated_at = '2026-10-13 03:42:33' WHERE id = ?`, issue.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		report, err := sqlite.CheckIssueTimestamps(ctx, query)
		if report.NonCanonicalCount != 12 || report.InvalidCount != 0 || len(report.Details) != 10 {
			t.Fatalf("report = %+v", report)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := db.NormalizeIssueTimestamps(ctx); err != nil || count != 12 {
		t.Fatalf("normalize = %d, %v", count, err)
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		var nullCount int
		if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE closed_at IS NULL AND archived_at IS NULL`).Scan(&nullCount); err != nil {
			return err
		}
		if nullCount != 12 {
			t.Fatalf("nullable timestamps changed: %d", nullCount)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueTimestampMaintenanceRefusesIgnoredWrite(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ignored write"})
	if err != nil {
		t.Fatal(err)
	}
	const legacy = "2026-10-13 03:42:33"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET created_at = ?, updated_at = ? WHERE id = ?`, legacy, legacy, issue.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER ignore_timestamp_repair BEFORE UPDATE OF updated_at ON issues BEGIN SELECT RAISE(IGNORE); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := db.NormalizeIssueTimestamps(ctx); err == nil || count != 0 {
		t.Fatalf("normalize = %d, %v", count, err)
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		var value string
		if err := query.QueryRowContext(ctx, `SELECT created_at FROM issues WHERE id = ?`, issue.ID).Scan(&value); err != nil {
			return err
		}
		if value != legacy {
			t.Fatalf("first update not rolled back: %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
