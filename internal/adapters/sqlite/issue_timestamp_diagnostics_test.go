package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

func TestIssueTimestampDiagnostic(t *testing.T) {
	ctx := context.Background()
	service, db, _ := openIssueService(t)

	t.Run("legacy raw utc values fail with issue context", func(t *testing.T) {
		result, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ready", Status: domain.StatusReady})
		if err != nil {
			t.Fatal(err)
		}
		raw := "2026-10-13 03:42:33"
		setIssueTimestampValue(t, db, ctx, result.ID, "updated_at", raw)

		_, err = service.GetIssue(ctx, result.ID)
		assertIssueTimestampDiagnostic(t, err, result.Issue, "updated_at", raw, raw)

		_, err = service.ListIssues(ctx, domain.ListIssuesInput{Limit: 20})
		assertIssueTimestampDiagnostic(t, err, result.Issue, "updated_at", raw, raw)
	})

	for _, field := range []string{"created_at", "updated_at", "closed_at", "archived_at"} {
		t.Run("invalid "+field, func(t *testing.T) {
			result, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ready", Status: domain.StatusReady})
			if err != nil {
				t.Fatal(err)
			}
			raw := "2026-10-13 03:42:33"
			setIssueTimestampValue(t, db, ctx, result.ID, field, raw)

			_, err = service.GetIssue(ctx, result.ID)
			assertIssueTimestampDiagnostic(t, err, result.Issue, field, raw, raw)
		})
	}

	t.Run("nonzero offset rejects", func(t *testing.T) {
		result, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ready", Status: domain.StatusReady})
		if err != nil {
			t.Fatal(err)
		}
		raw := "2026-10-13T03:42:33+02:00"
		setIssueTimestampValue(t, db, ctx, result.ID, "updated_at", raw)

		_, err = service.GetIssue(ctx, result.ID)
		assertIssueTimestampDiagnostic(t, err, result.Issue, "updated_at", raw, raw)
	})

	t.Run("null nullable timestamps allowed", func(t *testing.T) {
		for _, field := range []string{"closed_at", "archived_at"} {
			result, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ready", Status: domain.StatusReady})
			if err != nil {
				t.Fatal(err)
			}
			setIssueTimestampNull(t, db, ctx, result.ID, field)
			got, err := service.GetIssue(ctx, result.ID)
			if err != nil {
				t.Fatalf("GetIssue(%s) with NULL %s error = %v", result.ID, field, err)
			}
			switch field {
			case "closed_at":
				if got.ClosedAt != nil {
					t.Fatalf("ClosedAt = %#v, want nil", got.ClosedAt)
				}
			case "archived_at":
				if got.ArchivedAt != nil {
					t.Fatalf("ArchivedAt = %#v, want nil", got.ArchivedAt)
				}
			}
		}
	})

	t.Run("+00:00 and canonical formats are accepted", func(t *testing.T) {
		for _, field := range []string{"created_at", "updated_at", "closed_at", "archived_at"} {
			result, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ready", Status: domain.StatusReady})
			if err != nil {
				t.Fatal(err)
			}
			raw := "2026-10-13T03:42:33+00:00"
			if field == "updated_at" || field == "archived_at" {
				raw = "2026-10-13T03:42:33.000000000Z"
			}
			setIssueTimestampValue(t, db, ctx, result.ID, field, raw)
			if _, err := service.GetIssue(ctx, result.ID); err != nil {
				t.Fatalf("GetIssue(%s,%s) with %s error = %v", result.ID, field, raw, err)
			}
		}
	})

	t.Run("long and control-character diagnostics are bounded", func(t *testing.T) {
		result, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "ready", Status: domain.StatusReady})
		if err != nil {
			t.Fatal(err)
		}
		raw := "\x00\n" + strings.Repeat("x", 200)
		setIssueTimestampValue(t, db, ctx, result.ID, "created_at", raw)

		_, err = service.GetIssue(ctx, result.ID)
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) {
			t.Fatalf("GetIssue() error = %v, want *domain.Error", err)
		}
		if domainErr.Code != domain.CodeStorageCorrupt {
			t.Fatalf("GetIssue() code = %s, want %s", domainErr.Code, domain.CodeStorageCorrupt)
		}
		var detail domain.Detail
		for _, candidate := range domainErr.Details {
			if candidate.Field == "created_at" && candidate.Code == "INVALID_TIMESTAMP" {
				detail = candidate
				break
			}
		}
		if detail.Field == "" {
			t.Fatalf("details = %#v, want INVALID_TIMESTAMP detail for created_at", domainErr.Details)
		}
		wantDisplay := raw[:160] + "..."
		if !strings.Contains(detail.Message, result.ID) || !strings.Contains(detail.Message, fmt.Sprintf("ISSUE-%d", result.SequenceNo)) {
			t.Fatalf("detail.Message = %q, want issue ID %s and display ID ISSUE-%d", detail.Message, result.ID, result.SequenceNo)
		}
		if !strings.Contains(detail.Message, fmt.Sprintf("created_at=%q", wantDisplay)) {
			t.Fatalf("detail.Message = %q, want display value %q", detail.Message, fmt.Sprintf("created_at=%q", wantDisplay))
		}
		if strings.Contains(detail.Message, fmt.Sprintf("created_at=%q", raw)) {
			t.Fatalf("detail.Message = %q should not contain full raw value", detail.Message)
		}
		if strings.ContainsAny(detail.Message, "\x00\n") {
			t.Fatalf("detail.Message = %q contains unescaped control characters", detail.Message)
		}
		if !strings.Contains(detail.Message, "normalize-issue-timestamps --backup PATH") {
			t.Fatalf("detail.Message = %q, want recovery command", detail.Message)
		}
	})
}

func setIssueTimestampValue(t *testing.T, db *sqlite.DB, ctx context.Context, issueID, field, raw string) {
	t.Helper()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET "+field+" = ? WHERE id = ?", raw, issueID)
		return err
	}); err != nil {
		t.Fatalf("set %s to %q for issue %s: %v", field, raw, issueID, err)
	}
}

func setIssueTimestampNull(t *testing.T, db *sqlite.DB, ctx context.Context, issueID, field string) {
	t.Helper()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET "+field+" = NULL WHERE id = ?", issueID)
		return err
	}); err != nil {
		t.Fatalf("set %s to NULL for issue %s: %v", field, issueID, err)
	}
}

func assertIssueTimestampDiagnostic(t *testing.T, err error, issue domain.Issue, field, raw, shown string) {
	t.Helper()
	if err == nil {
		t.Fatal("error is nil, want STORAGE_CORRUPT/INVALID_TIMESTAMP")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	if domainErr.Code != domain.CodeStorageCorrupt {
		t.Fatalf("error code = %s, want %s", domainErr.Code, domain.CodeStorageCorrupt)
	}
	var detail domain.Detail
	for _, candidate := range domainErr.Details {
		if candidate.Field == field && candidate.Code == "INVALID_TIMESTAMP" {
			detail = candidate
			break
		}
	}
	if detail.Field == "" {
		t.Fatalf("details = %#v, want INVALID_TIMESTAMP for field %s", domainErr.Details, field)
	}
	if !strings.Contains(detail.Message, issue.ID) {
		t.Fatalf("detail.Message = %q, want issue ID %s", detail.Message, issue.ID)
	}
	if !strings.Contains(detail.Message, fmt.Sprintf("ISSUE-%d", issue.SequenceNo)) {
		t.Fatalf("detail.Message = %q, want display ID ISSUE-%d", detail.Message, issue.SequenceNo)
	}
	if shown == "" {
		shown = raw
	}
	if !strings.Contains(detail.Message, fmt.Sprintf("%s=%q", field, shown)) {
		t.Fatalf("detail.Message = %q, want %s=%q", detail.Message, field, shown)
	}
	if !strings.Contains(detail.Message, "normalize-issue-timestamps --backup PATH") {
		t.Fatalf("detail.Message = %q, want recovery command", detail.Message)
	}
}
