//go:build integration

package integration_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestIntegrationEpicReachesTerminalStatus is the ISSUE-224 regression, driven
// through the real binary over a real transport because the bug was a rule
// collision across two layers and only showed up end to end: the patch path
// refused a direct move to done for every issue type, and claim_issue refused
// any type that is not a task or bug, so a finished epic had no route to a
// terminal status at all.
func TestIntegrationEpicReachesTerminalStatus(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	t.Run("an epic closes with a direct status patch", func(t *testing.T) {
		epic := mustCreateBoardIssue(t, session, map[string]any{
			"type": "epic", "title": "Closable epic",
		})

		result := callIntegrationTool(t, session, "update_issue", map[string]any{
			"issue_id":         epic.DisplayID,
			"expected_version": 1,
			"changes":          map[string]any{"status": "done"},
		})
		if result.IsError {
			t.Fatalf("update_issue(epic -> done) failed: %#v", result.StructuredContent)
		}

		var updated struct {
			Issue struct {
				Status string `json:"status"`
			} `json:"issue"`
		}
		decodeIntegrationResult(t, result, &updated)
		if updated.Issue.Status != "done" {
			t.Fatalf("epic status = %q, want done", updated.Issue.Status)
		}
	})

	t.Run("an epic parked in ready is not a trap", func(t *testing.T) {
		epic := mustCreateBoardIssue(t, session, map[string]any{
			"type": "epic", "title": "Epic parked in ready",
		})
		ready := callIntegrationTool(t, session, "update_issue", map[string]any{
			"issue_id":         epic.DisplayID,
			"expected_version": 1,
			"changes":          map[string]any{"status": "ready"},
		})
		if ready.IsError {
			t.Fatalf("update_issue(epic -> ready) failed: %#v", ready.StructuredContent)
		}

		closed := callIntegrationTool(t, session, "update_issue", map[string]any{
			"issue_id":         epic.DisplayID,
			"expected_version": 2,
			"changes":          map[string]any{"status": "done"},
		})
		if closed.IsError {
			t.Fatalf("a ready epic must still be closable: %#v", closed.StructuredContent)
		}
	})

	// The other half of the contract: scoping the guard must not have opened a
	// bypass for work that is supposed to earn its terminal status.
	t.Run("a task still cannot be patched straight to done", func(t *testing.T) {
		task := mustCreateBoardIssue(t, session, map[string]any{
			"type": "task", "title": "Guarded task", "status": "ready",
		})

		result := callIntegrationTool(t, session, "update_issue", map[string]any{
			"issue_id":         task.DisplayID,
			"expected_version": 1,
			"changes":          map[string]any{"status": "done"},
		})
		if !result.IsError {
			t.Fatal("update_issue(task -> done) succeeded; the gated-status guard must still reject it")
		}
		assertDomainErrorCode(t, result, "INVALID_STATUS_TRANSITION")
	})

	t.Run("an epic still cannot be patched to review", func(t *testing.T) {
		epic := mustCreateBoardIssue(t, session, map[string]any{
			"type": "epic", "title": "Epic that cannot be reviewed",
		})

		result := callIntegrationTool(t, session, "update_issue", map[string]any{
			"issue_id":         epic.DisplayID,
			"expected_version": 1,
			"changes":          map[string]any{"status": "review"},
		})
		if !result.IsError {
			t.Fatal("update_issue(epic -> review) succeeded; an epic has no attempt to review")
		}
		assertDomainErrorCode(t, result, "INVALID_STATUS_TRANSITION")
	})

	t.Run("an epic still cannot be claimed", func(t *testing.T) {
		epic := mustCreateBoardIssue(t, session, map[string]any{
			"type": "epic", "title": "Unclaimable epic", "status": "ready",
		})

		result := callIntegrationTool(t, session, "claim_issue", map[string]any{
			"issue_id": epic.DisplayID,
		})
		if !result.IsError {
			t.Fatal("claim_issue on an epic succeeded; epics are not executable")
		}
	})
}

// assertDomainErrorCode checks that an IsError result carries the structured
// MCP error envelope with the expected domain code, rather than only checking
// that something failed.
func assertDomainErrorCode(t *testing.T, result *mcp.CallToolResult, wantCode string) {
	t.Helper()
	if result.StructuredContent == nil {
		t.Fatalf("error result has no structuredContent: %#v", result)
	}
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	decodeIntegrationResult(t, result, &envelope)
	if envelope.Code != wantCode {
		t.Fatalf("error code = %q, want %q (message: %s)", envelope.Code, wantCode, envelope.Message)
	}
}
