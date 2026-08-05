//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

// Fixed, deterministic bounds for the sustained write traffic driven against
// the background server while a concurrent backup runs. A count, not a
// timer, keeps runtime predictable across CI platforms.
const (
	backupTrafficBaselineIssues  = 5
	backupTrafficWriteIterations = 180
	backupTrafficStartSignalAt   = 8
)

// TestIntegrationBackupConsistencyDuringConcurrentWrites is the ISSUE-108
// proof test: the `backup` CLI subcommand runs as a separate OS process
// while a background server process drives sustained MCP write traffic
// (create_issue/add_comment/update_issue) against the same shared data
// root. internal/adapters/sqlite/sqlite.go's Backup performs a controlled
// WAL checkpoint before VACUUM INTO; this test exercises whether that
// checkpoint actually yields a point-in-time consistent snapshot when it
// races a live writer, which the existing in-process, quiet-database backup
// tests (main_test.go:TestBackupCommandCreatesValidatedBackup,
// internal/adapters/cli/cli_test.go:TestRunBackup) cannot answer.
func TestIntegrationBackupConsistencyDuringConcurrentWrites(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	// Baseline issues are created and committed before write traffic (and
	// the backup) start, and traffic never touches them afterward. Their
	// presence and exact shape in the backup are therefore deterministic,
	// unlike anything created during the traffic phase.
	baselineDisplayIDs := make([]string, 0, backupTrafficBaselineIssues)
	for i := 0; i < backupTrafficBaselineIssues; i++ {
		baselineDisplayIDs = append(baselineDisplayIDs, createBackupTrafficIssue(t, session, fmt.Sprintf("baseline issue %d", i)))
	}

	startedC := make(chan struct{})
	writeErrC := make(chan error, 1)
	go func() {
		writeErrC <- driveSustainedWriteTraffic(session, backupTrafficWriteIterations, backupTrafficStartSignalAt, startedC)
	}()

	// Block until write traffic has actually begun so the backup below is
	// guaranteed to race a live writer instead of an idle database.
	<-startedC

	backupOutput := filepath.Join(t.TempDir(), "concurrent-backup.db")
	backupEnv := env.attach()
	backupStdout := runIntegrationCommand(t, backupEnv, "--data-root", backupEnv.dataRoot, "backup", "--output", backupOutput, "--format", "json")

	if err := <-writeErrC; err != nil {
		t.Fatalf("sustained write traffic failed: %v", err)
	}

	var backupResponse struct {
		Output        string `json:"output"`
		SchemaVersion int    `json:"schema_version"`
		Validated     bool   `json:"validated"`
	}
	if err := json.Unmarshal(backupStdout, &backupResponse); err != nil {
		t.Fatalf("decode backup command output %s: %v", backupStdout, err)
	}
	if !backupResponse.Validated {
		t.Fatalf("backup command reported validated = false: %s", backupStdout)
	}
	if backupResponse.Output != backupOutput {
		t.Fatalf("backup command output path = %q, want %q", backupResponse.Output, backupOutput)
	}
	if backupResponse.SchemaVersion == 0 {
		t.Fatalf("backup command reported schema_version = 0: %s", backupStdout)
	}

	assertBackupIsConsistentSnapshot(t, backupOutput, baselineDisplayIDs)
	restoreBackupAndAssertQueryable(t, env, backupOutput)
}

// createBackupTrafficIssue creates one issue over session and returns its
// ISSUE-N display ID.
func createBackupTrafficIssue(t *testing.T, session *mcp.ClientSession, title string) string {
	t.Helper()
	result := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":     "task",
		"title":    title,
		"status":   "open",
		"priority": "medium",
	})
	var created struct {
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, result, &created)
	if result.IsError || created.DisplayID == "" {
		t.Fatalf("create_issue result = %#v", result)
	}
	return created.DisplayID
}

// driveSustainedWriteTraffic issues a fixed, deterministic number of
// create_issue/add_comment/update_issue calls over session to keep the WAL
// active while a concurrent backup runs. It only ever targets issues it
// creates itself, so it can never disturb the caller's baseline issues.
// startedC is closed once startAfter operations have completed (or
// unconditionally once the loop ends, whichever comes first), letting the
// caller synchronize a backup to start only after write traffic is actually
// flowing. It runs off the test goroutine, so it reports failures as a
// returned error instead of calling t.Fatalf.
func driveSustainedWriteTraffic(session *mcp.ClientSession, iterations, startAfter int, startedC chan<- struct{}) error {
	type trafficIssue struct {
		id      string
		version int64
	}
	priorities := []string{"low", "medium", "high", "critical"}
	created := make([]trafficIssue, 0, iterations)
	startSignaled := false
	signalStart := func() {
		if !startSignaled {
			startSignaled = true
			close(startedC)
		}
	}

	for i := 0; i < iterations; i++ {
		if i == startAfter {
			signalStart()
		}
		switch i % 3 {
		case 0:
			id, version, err := createTrafficIssueNoFatal(session, fmt.Sprintf("traffic issue %d", i))
			if err != nil {
				signalStart()
				return err
			}
			created = append(created, trafficIssue{id: id, version: version})
		case 1:
			if len(created) == 0 {
				continue
			}
			target := created[i%len(created)]
			if err := addTrafficCommentNoFatal(session, target.id, i); err != nil {
				signalStart()
				return err
			}
		case 2:
			if len(created) == 0 {
				continue
			}
			index := i % len(created)
			target := created[index]
			newVersion, err := updateTrafficIssueNoFatal(session, target.id, target.version, priorities[i%len(priorities)])
			if err != nil {
				signalStart()
				return err
			}
			created[index].version = newVersion
		}
	}
	signalStart()
	return nil
}

func createTrafficIssueNoFatal(session *mcp.ClientSession, title string) (string, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "create_issue",
		Arguments: map[string]any{"type": "task", "title": title, "status": "open", "priority": "medium"},
	})
	if err != nil {
		return "", 0, fmt.Errorf("create_issue protocol error: %w", err)
	}
	if result.IsError {
		return "", 0, fmt.Errorf("create_issue tool error: %#v", result)
	}
	var created struct {
		DisplayID string `json:"display_id"`
		Version   int64  `json:"version"`
	}
	if err := decodeToolResultNoFatal(result, &created); err != nil {
		return "", 0, err
	}
	if created.DisplayID == "" {
		return "", 0, fmt.Errorf("create_issue returned no display_id")
	}
	return created.DisplayID, created.Version, nil
}

func addTrafficCommentNoFatal(session *mcp.ClientSession, issueID string, index int) error {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "add_comment",
		Arguments: map[string]any{"issue_id": issueID, "content": fmt.Sprintf("sustained write traffic comment %d", index)},
	})
	if err != nil {
		return fmt.Errorf("add_comment protocol error: %w", err)
	}
	if result.IsError {
		return fmt.Errorf("add_comment tool error: %#v", result)
	}
	return nil
}

func updateTrafficIssueNoFatal(session *mcp.ClientSession, issueID string, expectedVersion int64, priority string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "update_issue",
		Arguments: map[string]any{
			"issue_id":         issueID,
			"expected_version": expectedVersion,
			"changes":          map[string]any{"priority": priority},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("update_issue protocol error: %w", err)
	}
	if result.IsError {
		return 0, fmt.Errorf("update_issue tool error: %#v", result)
	}
	var updated struct {
		Issue struct {
			Version int64 `json:"version"`
		} `json:"issue"`
	}
	if err := decodeToolResultNoFatal(result, &updated); err != nil {
		return 0, err
	}
	return updated.Issue.Version, nil
}

// decodeToolResultNoFatal mirrors decodeIntegrationResult without requiring
// a *testing.T, for use from the background write-traffic goroutine.
func decodeToolResultNoFatal(result *mcp.CallToolResult, destination any) error {
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("marshal structured result: %w", err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		return fmt.Errorf("decode structured result %s: %w", data, err)
	}
	return nil
}

// assertBackupIsConsistentSnapshot opens the backup database directly,
// confirms it passes SQLite's own integrity check, and then confirms it
// represents a point-in-time snapshot: every issue present has exactly the
// rows a completed create_issue/add_comment/update_issue transaction would
// have written alongside it, never a partial subset.
func assertBackupIsConsistentSnapshot(t *testing.T, backupPath string, baselineDisplayIDs []string) {
	t.Helper()
	ctx := context.Background()
	backupDB, err := sqlite.Open(ctx, backupPath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open backup database: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()
		if err := backupDB.Close(closeCtx); err != nil {
			t.Errorf("close backup database: %v", err)
		}
	}()

	if err := backupDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		rows, err := query.QueryContext(ctx, "PRAGMA integrity_check")
		if err != nil {
			return err
		}
		defer rows.Close()
		var results []string
		for rows.Next() {
			var result string
			if err := rows.Scan(&result); err != nil {
				return err
			}
			results = append(results, result)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(results) != 1 || results[0] != "ok" {
			return fmt.Errorf("PRAGMA integrity_check = %v, want [ok]", results)
		}
		return nil
	}); err != nil {
		t.Fatalf("backup integrity check failed: %v", err)
	}

	var allDisplayIDs []string
	if err := backupDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		rows, err := query.QueryContext(ctx, "SELECT sequence_no FROM issues ORDER BY sequence_no")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sequenceNo int64
			if err := rows.Scan(&sequenceNo); err != nil {
				return err
			}
			allDisplayIDs = append(allDisplayIDs, fmt.Sprintf("ISSUE-%d", sequenceNo))
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("list issues in backup: %v", err)
	}

	// The exact issue count depends on when VACUUM INTO's snapshot landed
	// relative to the traffic goroutine and is not asserted. What must
	// always hold is that nothing already committed before the backup ran
	// (the baseline) went missing.
	if len(allDisplayIDs) < len(baselineDisplayIDs) {
		t.Fatalf("backup contains fewer issues (%d) than the committed baseline (%d)", len(allDisplayIDs), len(baselineDisplayIDs))
	}

	baselineSet := make(map[string]bool, len(baselineDisplayIDs))
	for _, displayID := range baselineDisplayIDs {
		baselineSet[displayID] = true
	}
	for _, displayID := range baselineDisplayIDs {
		assertIssueConsistentInBackup(t, backupDB, displayID, true)
	}
	for _, displayID := range allDisplayIDs {
		if baselineSet[displayID] {
			continue
		}
		assertIssueConsistentInBackup(t, backupDB, displayID, false)
	}
}

// assertIssueConsistentInBackup checks one issue's rows for referential
// completeness: exactly one creation event, an update-event count that
// matches its version history exactly, and a comment count that matches its
// comment_added event count exactly. create_issue, add_comment, and
// update_issue each write their issue/event/comment rows inside a single
// transaction (see internal/adapters/sqlite/issues.go and comments.go), so
// any mismatch here means VACUUM INTO captured a torn write instead of a
// point-in-time snapshot. When isBaseline is true the issue is additionally
// known, deterministically, to be untouched by write traffic.
func assertIssueConsistentInBackup(t *testing.T, backupDB *sqlite.DB, displayID string, isBaseline bool) {
	t.Helper()
	identifier, err := domain.ParseIssueIdentifier(displayID)
	if err != nil {
		t.Fatalf("parse issue identifier %q: %v", displayID, err)
	}

	var (
		internalID     string
		version        int64
		creationEvents int
		updateEvents   int
		commentCount   int
		commentEvents  int
	)
	ctx := context.Background()
	err = backupDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT id, version FROM issues WHERE sequence_no = ?", identifier.SequenceNo).Scan(&internalID, &version); err != nil {
			return fmt.Errorf("load issue: %w", err)
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ? AND event_type = 'issue_created'", internalID).Scan(&creationEvents); err != nil {
			return err
		}
		// update_issue is the only path that increments version, and it
		// always logs exactly one of these three event types alongside the
		// increment in the same transaction (see UpdateIssue in
		// internal/adapters/sqlite/issues.go). comment_added events are
		// deliberately excluded here: add_comment never touches version.
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ? AND event_type IN ('issue_updated', 'status_changed', 'labels_changed')", internalID).Scan(&updateEvents); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM comments WHERE issue_id = ?", internalID).Scan(&commentCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ? AND event_type = 'comment_added'", internalID).Scan(&commentEvents); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("query issue %s in backup: %v", displayID, err)
	}

	if creationEvents != 1 {
		t.Fatalf("torn backup snapshot: issue %s has %d issue_created events, want exactly 1", displayID, creationEvents)
	}
	if int64(updateEvents) != version-1 {
		t.Fatalf("torn backup snapshot: issue %s version=%d but has %d update events, want %d", displayID, version, updateEvents, version-1)
	}
	if commentCount != commentEvents {
		t.Fatalf("torn backup snapshot: issue %s has %d comments but %d comment_added events, want equal counts", displayID, commentCount, commentEvents)
	}
	if isBaseline {
		if version != 1 {
			t.Fatalf("baseline issue %s version = %d, want 1: nothing should have updated it", displayID, version)
		}
		if commentCount != 0 {
			t.Fatalf("baseline issue %s has %d comments, want 0: nothing should have commented on it", displayID, commentCount)
		}
	}
}

// restoreBackupAndAssertQueryable places the backup at the project database
// path a fresh data root would compute for sourceEnv's repository (there is
// no dedicated "restore" subcommand: a backup is a self-contained SQLite
// file, and restoring it is simply making it the project database a new
// data root opens), then confirms the restored project is queryable end to
// end: doctor reports healthy, and export_project returns a document that
// parses.
func restoreBackupAndAssertQueryable(t *testing.T, sourceEnv integrationEnvironment, backupPath string) {
	t.Helper()
	restoredDataRoot := filepath.Join(t.TempDir(), "restored-data")
	if err := os.MkdirAll(restoredDataRoot, 0o755); err != nil {
		t.Fatalf("create restored data root: %v", err)
	}
	restoredEnv := integrationEnvironment{repository: sourceEnv.repository, dataRoot: restoredDataRoot}

	restoredDatabasePath := mustProjectDatabasePath(t, restoredEnv)
	if err := os.MkdirAll(filepath.Dir(restoredDatabasePath), 0o755); err != nil {
		t.Fatalf("create restored project data directory: %v", err)
	}
	copyFileForRestore(t, backupPath, restoredDatabasePath)

	doctorOutput := runIntegrationCommand(t, restoredEnv, "--data-root", restoredEnv.dataRoot, "doctor", "--full", "--format", "json")
	var doctorReport struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.Unmarshal(doctorOutput, &doctorReport); err != nil {
		t.Fatalf("decode doctor output %s: %v", doctorOutput, err)
	}
	if !doctorReport.Healthy {
		t.Fatalf("restored backup failed doctor: %s", doctorOutput)
	}

	restoredSession := restoredEnv.connect(t)
	exportResult := callIntegrationTool(t, restoredSession, "export_project", map[string]any{"delivery": "inline"})
	if exportResult.IsError {
		t.Fatalf("export_project on restored backup result = %#v", exportResult)
	}
	var document domain.LogicalProjectDocument
	decodeIntegrationResult(t, exportResult, &document)
	if document.Format == "" || len(document.Issues) == 0 {
		t.Fatalf("export_project on restored backup returned an unparseable or empty document: format=%q issues=%d", document.Format, len(document.Issues))
	}
}

func copyFileForRestore(t *testing.T, src, dst string) {
	t.Helper()
	source, err := os.Open(src)
	if err != nil {
		t.Fatalf("open backup file %s: %v", src, err)
	}
	defer source.Close()
	destination, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatalf("create restored database file %s: %v", dst, err)
	}
	defer destination.Close()
	if _, err := io.Copy(destination, source); err != nil {
		t.Fatalf("copy backup into restored data root: %v", err)
	}
}
