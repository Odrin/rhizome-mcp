//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

func TestIntegrationIssueTimestampRecovery(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)
	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type": "task", "title": "GitHub #23 reproduction", "status": "ready",
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, created, &issue)
	if created.IsError || issue.ID == "" {
		t.Fatalf("create issue = %#v", created)
	}
	db, err := sqlite.Open(context.Background(), mustProjectDatabasePath(t, env), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(context.Background()) }()
	const legacy = "2026-10-13 03:42:33"
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET updated_at = ? WHERE id = ?`, legacy, issue.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"get_issue", "list_issues", "claim_issue"} {
		args := map[string]any{}
		if name != "list_issues" {
			args["issue_id"] = issue.DisplayID
		}
		result := callIntegrationTool(t, session, name, args)
		if !result.IsError {
			t.Fatalf("%s unexpectedly succeeded", name)
		}
		var failure domain.Error
		decodeIntegrationResult(t, result, &failure)
		if failure.Code != domain.CodeStorageCorrupt || failure.Retryable || len(failure.Details) != 1 ||
			failure.Details[0].Field != "updated_at" || failure.Details[0].Code != "INVALID_TIMESTAMP" {
			t.Fatalf("%s = %+v", name, failure)
		}
		for _, text := range []string{issue.ID, issue.DisplayID, legacy, "normalize-issue-timestamps --backup PATH"} {
			if !strings.Contains(failure.Details[0].Message, text) {
				t.Fatalf("%s missing %q in %+v", name, text, failure)
			}
		}
	}
	for _, args := range [][]string{{"issue", "show", issue.DisplayID}, {"issue", "list"}} {
		_, stderr, _ := runIntegrationCommandExpectingFailure(t, env.repository, append([]string{"--data-root", env.dataRoot}, args...)...)
		if !strings.Contains(stderr, issue.ID) || !strings.Contains(stderr, legacy) || !strings.Contains(stderr, "normalize-issue-timestamps") {
			t.Fatalf("CLI error lacks diagnostics: %s", stderr)
		}
	}
	for _, flags := range [][]string{{}, {"--full"}} {
		args := append([]string{"--data-root", env.dataRoot, "doctor", "--format", "json"}, flags...)
		stdout, _, _ := runIntegrationCommandExpectingFailure(t, env.repository, args...)
		var report struct {
			Healthy bool `json:"healthy"`
			Checks  []struct {
				Check   string `json:"check"`
				Healthy bool   `json:"healthy"`
				Message string `json:"message"`
			} `json:"checks"`
		}
		if err := json.Unmarshal([]byte(stdout), &report); err != nil || report.Healthy {
			t.Fatalf("doctor = %s, %v", stdout, err)
		}
		found := false
		for _, check := range report.Checks {
			if check.Check == "issue_timestamps" && !check.Healthy && strings.Contains(check.Message, issue.ID) {
				found = true
			}
		}
		if !found {
			t.Fatalf("doctor lacks timestamp check: %s", stdout)
		}
	}
	backupPath := filepath.Join(t.TempDir(), "before-repair.db")
	output := runIntegrationCommand(t, env, "--data-root", env.dataRoot, "maintenance", "normalize-issue-timestamps", "--backup", backupPath, "--format", "json")
	var normalized struct {
		BackupPath      string `json:"backup_path"`
		NormalizedCount int    `json:"normalized_count"`
	}
	if err := json.Unmarshal(output, &normalized); err != nil || normalized.BackupPath != backupPath || normalized.NormalizedCount != 1 {
		t.Fatalf("normalization = %s, %v", output, err)
	}
	backup, err := sqlite.Open(context.Background(), backupPath, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close(context.Background()) }()
	if err := backup.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		var value string
		if err := query.QueryRowContext(ctx, `SELECT updated_at FROM issues WHERE id = ?`, issue.ID).Scan(&value); err != nil {
			return err
		}
		if value != legacy {
			t.Fatalf("backup did not preserve original value: %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"get_issue", "list_issues"} {
		args := map[string]any{}
		if name == "get_issue" {
			args["issue_id"] = issue.DisplayID
		}
		if result := callIntegrationTool(t, session, name, args); result.IsError {
			t.Fatalf("%s after repair = %#v", name, result)
		}
	}
	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "doctor", "--full")
	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "issue", "show", issue.DisplayID)
	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "issue", "list")
	claim := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID})
	var attempt struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claim, &attempt)
	if claim.IsError || attempt.Attempt.ID == "" || attempt.LeaseToken == "" {
		t.Fatalf("claim after repair = %#v", claim)
	}
	finished := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id": attempt.Attempt.ID, "lease_token": attempt.LeaseToken,
		"outcome": "interrupted", "interruption_reason_code": "user_request", "result_summary": "Recovery verified",
	})
	if finished.IsError {
		t.Fatalf("finish scratch attempt = %#v", finished)
	}
}

func TestIntegrationIssueTimestampRecoveryRefusesMalformedData(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)
	callIntegrationTool(t, session, "create_issue", map[string]any{"type": "task", "title": "malformed value"})
	db, err := sqlite.Open(context.Background(), mustProjectDatabasePath(t, env), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(context.Background()) }()
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET created_at = '2026-10-13 03:42:33', updated_at = 'invalid'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "refused-repair.db")
	stdout, stderr, _ := runIntegrationCommandExpectingFailure(t, env.repository,
		"--data-root", env.dataRoot, "maintenance", "normalize-issue-timestamps", "--backup", backupPath, "--format", "json")
	if stdout != "" || !strings.Contains(stderr, backupPath) || !strings.Contains(stderr, "invalid") {
		t.Fatalf("failure stdout=%s stderr=%s", stdout, stderr)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("failed recovery lost backup: %v", err)
	}
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		var created, updated string
		if err := query.QueryRowContext(ctx, `SELECT created_at, updated_at FROM issues`).Scan(&created, &updated); err != nil {
			return err
		}
		if created != "2026-10-13 03:42:33" || updated != "invalid" {
			t.Fatalf("partially repaired data: %q, %q", created, updated)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
