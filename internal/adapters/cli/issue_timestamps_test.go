package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestMaintenanceNormalizeIssueTimestamps(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			adapter := New(Services{}, &stdout, &stderr, nil, nil)
			adapter.SetIssueTimestampNormalizationHandler(func(_ context.Context, path string) (IssueTimestampNormalizationReport, error) {
				if path != "/backup.db" {
					t.Fatalf("backup path = %q", path)
				}
				return IssueTimestampNormalizationReport{BackupPath: path, NormalizedCount: 4}, nil
			})
			if err := adapter.Run(context.Background(), []string{"maintenance", "normalize-issue-timestamps", "--backup", "/backup.db", "--format", format}); err != nil {
				t.Fatal(err)
			}
			if format == "json" {
				var result IssueTimestampNormalizationReport
				if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.NormalizedCount != 4 || result.BackupPath != "/backup.db" {
					t.Fatalf("output = %s, %v", stdout.String(), err)
				}
			} else if !strings.Contains(stdout.String(), "backup_path\tnormalized_count\n/backup.db\t4") {
				t.Fatalf("output = %q", stdout.String())
			}
		})
	}
	for _, args := range [][]string{
		{}, {"--backup", ""}, {"--backup", "  "}, {"--backup", "/backup.db", "extra"},
		{"--backup", "/backup.db", "--format", "xml"}, {"--unsupported"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout bytes.Buffer
			adapter := New(Services{}, &stdout, &bytes.Buffer{}, nil, nil)
			adapter.SetIssueTimestampNormalizationHandler(func(context.Context, string) (IssueTimestampNormalizationReport, error) {
				t.Fatal("handler called for invalid input")
				return IssueTimestampNormalizationReport{}, nil
			})
			err := adapter.Run(context.Background(), append([]string{"maintenance", "normalize-issue-timestamps"}, args...))
			wantExit := 2
			if len(args) == 1 && args[0] == "--unsupported" {
				wantExit = 1
			}
			if err == nil || ExitCodeFor(err) != wantExit || stdout.Len() != 0 {
				t.Fatalf("invalid input = %v, output %q", err, stdout.String())
			}
		})
	}
}

func TestMaintenanceNormalizeIssueTimestampsReportsFailure(t *testing.T) {
	var stdout bytes.Buffer
	adapter := New(Services{}, &stdout, &bytes.Buffer{}, nil, nil)
	args := []string{"maintenance", "normalize-issue-timestamps", "--backup", "/backup.db"}
	if err := adapter.Run(context.Background(), args); err == nil {
		t.Fatal("missing handler accepted")
	}
	failure := domain.NewError(domain.CodeStorageCorrupt, "repair refused", false,
		domain.Detail{Field: "updated_at", Code: "INVALID_TIMESTAMP", Message: "ISSUE-1: updated_at=\"bad\"; restore a trusted backup"})
	adapter.SetIssueTimestampNormalizationHandler(func(context.Context, string) (IssueTimestampNormalizationReport, error) {
		return IssueTimestampNormalizationReport{}, failure
	})
	err := adapter.Run(context.Background(), args)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), failure.Details[0].Message) || stdout.Len() != 0 {
		t.Fatalf("failure = %v, stdout = %q", err, stdout.String())
	}
}
