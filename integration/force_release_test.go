//go:build integration

package integration_test

import (
	"strings"
	"testing"
)

func TestIntegrationForceReleaseAttemptObservedByRunningServer(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "task",
		"title":                 "Force release test issue",
		"description":           "Verify force release is observed by running server",
		"status":                "ready",
		"labels":                []string{"integration"},
		"create_missing_labels": true,
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, created, &issue)
	if created.IsError || issue.ID == "" || issue.DisplayID == "" {
		t.Fatalf("create_issue result = %#v, decoded = %#v", created, issue)
	}

	claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      issue.DisplayID,
		"lease_seconds": 60,
	})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimed, &claim)
	if claimed.IsError || claim.Attempt.ID == "" || claim.LeaseToken == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimed, claim)
	}
	attemptID := claim.Attempt.ID
	originalLeaseToken := claim.LeaseToken

	note := callIntegrationTool(t, session, "save_attempt_note", map[string]any{
		"attempt_id":  attemptID,
		"lease_token": originalLeaseToken,
		"kind":        "checkpoint",
		"content":     "Test checkpoint before force release",
	})
	if note.IsError {
		t.Fatalf("save_attempt_note before release failed: %#v", note)
	}

	releaseOutput := runIntegrationCommand(t, env, "--data-root", env.dataRoot, "maintenance", "release-attempt", attemptID)
	releaseOutputStr := string(releaseOutput)
	if !strings.Contains(releaseOutputStr, "attempt_id") || !strings.Contains(releaseOutputStr, attemptID) {
		t.Fatalf("maintenance release-attempt output missing expected fields, got: %s", releaseOutputStr)
	}

	saveNoteWithInvalidToken := callIntegrationTool(t, session, "save_attempt_note", map[string]any{
		"attempt_id":  attemptID,
		"lease_token": originalLeaseToken,
		"kind":        "progress",
		"content":     "Should fail with invalid lease token",
	})
	if !saveNoteWithInvalidToken.IsError {
		t.Fatalf("save_attempt_note with old lease_token should have failed but succeeded: %#v", saveNoteWithInvalidToken)
	}
	saveErrorMap, ok := saveNoteWithInvalidToken.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("save_attempt_note error structured content should be a map, got %T", saveNoteWithInvalidToken.StructuredContent)
	}
	saveCode, ok := saveErrorMap["code"].(string)
	if !ok || saveCode == "" {
		t.Fatalf("save_attempt_note error should include a non-empty 'code' field, got %#v", saveErrorMap)
	}
	if saveCode != "ATTEMPT_NOT_ACTIVE" {
		t.Fatalf("save_attempt_note structured domain error code = %q, want ATTEMPT_NOT_ACTIVE", saveCode)
	}

	finishWithInvalidToken := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":          attemptID,
		"lease_token":         originalLeaseToken,
		"outcome":             "completed",
		"result_summary":      "Should fail with invalid lease token",
		"target_issue_status": "done",
	})
	if !finishWithInvalidToken.IsError {
		t.Fatalf("finish_attempt with old lease_token should have failed but succeeded: %#v", finishWithInvalidToken)
	}
	finishErrorMap, ok := finishWithInvalidToken.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("finish_attempt error structured content should be a map, got %T", finishWithInvalidToken.StructuredContent)
	}
	finishCode, ok := finishErrorMap["code"].(string)
	if !ok || finishCode == "" {
		t.Fatalf("finish_attempt error should include a non-empty 'code' field, got %#v", finishErrorMap)
	}
	if finishCode != "ATTEMPT_NOT_ACTIVE" {
		t.Fatalf("finish_attempt structured domain error code = %q, want ATTEMPT_NOT_ACTIVE", finishCode)
	}

	reClaimed := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      issue.DisplayID,
		"lease_seconds": 60,
	})
	var reClaim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, reClaimed, &reClaim)
	if reClaimed.IsError || reClaim.Attempt.ID == "" || reClaim.LeaseToken == "" {
		t.Fatalf("reclaim_issue result = %#v, decoded = %#v", reClaimed, reClaim)
	}

	newAttemptID := reClaim.Attempt.ID
	newLeaseToken := reClaim.LeaseToken

	if newLeaseToken == originalLeaseToken {
		t.Fatalf("new lease_token should differ from original: old=%s new=%s", originalLeaseToken, newLeaseToken)
	}

	if newAttemptID == attemptID {
		t.Fatalf("new attempt_id should differ from original: old=%s new=%s", attemptID, newAttemptID)
	}

	finishNewAttempt := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":          newAttemptID,
		"lease_token":         newLeaseToken,
		"outcome":             "completed",
		"result_summary":      "Force release test completed",
		"target_issue_status": "done",
	})
	var finishNewOutput struct {
		Attempt struct {
			Status string `json:"status"`
		} `json:"attempt"`
	}
	decodeIntegrationResult(t, finishNewAttempt, &finishNewOutput)
	if finishNewAttempt.IsError || finishNewOutput.Attempt.Status != "completed" {
		t.Fatalf("finish new attempt failed: %#v, decoded = %#v", finishNewAttempt, finishNewOutput)
	}
}

func TestIntegrationForceReleaseAttemptErrorCases(t *testing.T) {
	env := newIntegrationEnvironment(t)

	t.Run("missing attempt", func(t *testing.T) {
		stdout, stderr, _ := runIntegrationCommandExpectingFailure(t, env.repository, "--data-root", env.dataRoot, "maintenance", "release-attempt", "01ARZ3NDEKTSV4RRFFQ69G5FAY")

		output := stdout + stderr
		if !strings.Contains(output, "ATTEMPT_NOT_FOUND") && !strings.Contains(output, "not found") && !strings.Contains(output, "no such") {
			t.Fatalf("expected ATTEMPT_NOT_FOUND error in output, got stdout: %s stderr: %s", stdout, stderr)
		}
	})

	t.Run("inactive attempt", func(t *testing.T) {
		session := env.connect(t)

		created := callIntegrationTool(t, session, "create_issue", map[string]any{
			"type":                  "task",
			"title":                 "Issue for inactive test",
			"status":                "ready",
			"labels":                []string{"integration"},
			"create_missing_labels": true,
		})
		var issue struct {
			ID        string `json:"id"`
			DisplayID string `json:"display_id"`
		}
		decodeIntegrationResult(t, created, &issue)
		if created.IsError || issue.ID == "" || issue.DisplayID == "" {
			t.Fatalf("create_issue failed: %#v", created)
		}

		claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{
			"issue_id":      issue.DisplayID,
			"lease_seconds": 60,
		})
		var claim struct {
			Attempt struct {
				ID string `json:"id"`
			} `json:"attempt"`
			LeaseToken string `json:"lease_token"`
		}
		decodeIntegrationResult(t, claimed, &claim)
		if claimed.IsError || claim.Attempt.ID == "" || claim.LeaseToken == "" {
			t.Fatalf("claim_issue failed: %#v", claimed)
		}

		finished := callIntegrationTool(t, session, "finish_attempt", map[string]any{
			"attempt_id":          claim.Attempt.ID,
			"lease_token":         claim.LeaseToken,
			"outcome":             "completed",
			"result_summary":      "Finished to test release of inactive",
			"target_issue_status": "done",
		})
		if finished.IsError {
			t.Fatalf("finish_attempt failed: %#v", finished)
		}

		stdout, stderr, _ := runIntegrationCommandExpectingFailure(t, env.repository, "--data-root", env.dataRoot, "maintenance", "release-attempt", claim.Attempt.ID)

		output := stdout + stderr
		if !strings.Contains(output, "ATTEMPT_NOT_ACTIVE") && !strings.Contains(output, "inactive") && !strings.Contains(output, "not active") {
			t.Fatalf("expected ATTEMPT_NOT_ACTIVE error in output, got stdout: %s stderr: %s", stdout, stderr)
		}
	})
}
