package mcp_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
)

func queryNullColumn(t *testing.T, db *sqlite.DB, query string, args ...any) sql.NullString {
	t.Helper()
	var value sql.NullString
	if err := db.Read(context.Background(), func(ctx context.Context, q sqlite.Queryer) error {
		return q.QueryRowContext(ctx, query, args...).Scan(&value)
	}); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

func queryCount(t *testing.T, db *sqlite.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.Read(context.Background(), func(ctx context.Context, q sqlite.Queryer) error {
		return q.QueryRowContext(ctx, query, args...).Scan(&count)
	}); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return count
}

func requireNullString(t *testing.T, got sql.NullString, want string) {
	t.Helper()
	if !got.Valid || got.String != want {
		t.Fatalf("got %#v, want valid %q", got, want)
	}
}

func TestMCPSessionAttributionAcrossMutationsAndReplay(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "mcp-attribution.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()

	createSession := func(name string) (string, string) {
		res := call(t, client, "create_agent_session", map[string]any{"client_name": name})
		var out struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
			Handle string `json:"agent_session_handle"`
		}
		decodeStructured(t, res, &out)
		if res.IsError || out.Session.ID == "" || out.Handle == "" {
			t.Fatalf("create_agent_session(%s) = %#v", name, res)
		}
		return out.Session.ID, out.Handle
	}

	sessionA, handleA := createSession("agent-a")
	source.Advance(time.Second)
	sessionB, handleB := createSession("agent-b")

	// 1. apply_issue_plan with handleA + replay with handleB.
	planArgs := func(handle string) map[string]any {
		return map[string]any{
			"issues": []map[string]any{
				{"ref": "p1", "type": "task", "title": "Plan 1", "priority": "medium", "status": "ready"},
				{"ref": "p2", "type": "task", "title": "Plan 2", "priority": "low", "status": "ready"},
			},
			"relations": []map[string]any{
				{"source_ref": "p1", "target_ref": "p2", "type": "blocks"},
			},
			"decisions": []map[string]any{
				{"issue_ref": "p1", "title": "Plan Dec", "summary": "S", "content": "C"},
			},
			"idempotency_key":      "mcp-plan-1",
			"agent_session_handle": handle,
		}
	}
	applied := call(t, client, "apply_issue_plan", planArgs(handleA))
	if applied.IsError {
		t.Fatalf("apply_issue_plan = %#v", applied)
	}
	var planOut struct {
		CreatedIssues []struct {
			Ref   string `json:"ref"`
			Issue struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"created_issues"`
		CreatedRelations []struct {
			ID string `json:"id"`
		} `json:"created_relations"`
		CreatedDecisions []struct {
			ID string `json:"id"`
		} `json:"created_decisions"`
	}
	decodeStructured(t, applied, &planOut)
	if len(planOut.CreatedIssues) != 2 || len(planOut.CreatedRelations) != 1 || len(planOut.CreatedDecisions) != 1 {
		t.Fatalf("unexpected apply_issue_plan output = %#v", planOut)
	}
	for _, ci := range planOut.CreatedIssues {
		requireNullString(t, queryNullColumn(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, ci.Issue.ID), sessionA)
		requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_created'`, ci.Issue.ID), sessionA)
		requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'relation_added'`, ci.Issue.ID), sessionA)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT created_by_session_id FROM issue_relations WHERE id = ?`, planOut.CreatedRelations[0].ID), sessionA)
	requireNullString(t, queryNullColumn(t, db, `SELECT created_by_session_id FROM decisions WHERE id = ?`, planOut.CreatedDecisions[0].ID), sessionA)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'decision_recorded'`, planOut.CreatedIssues[0].Issue.ID), sessionA)

	planReplay := call(t, client, "apply_issue_plan", planArgs(handleB))
	if planReplay.IsError {
		t.Fatalf("replay apply_issue_plan with different handle failed: %#v", planReplay)
	}
	var planReplayOut struct {
		CreatedDecisions []struct {
			ID string `json:"id"`
		} `json:"created_decisions"`
	}
	decodeStructured(t, planReplay, &planReplayOut)
	if len(planReplayOut.CreatedDecisions) != 1 || planReplayOut.CreatedDecisions[0].ID != planOut.CreatedDecisions[0].ID {
		t.Fatalf("replay decisions = %#v, want %q", planReplayOut.CreatedDecisions, planOut.CreatedDecisions[0].ID)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, planOut.CreatedIssues[0].Issue.ID), sessionA)

	// 2. create_issue with handleB + replay with handleA + unattributed create_issue.
	created := call(t, client, "create_issue", map[string]any{
		"type":                 "task",
		"title":                "MCP attributed issue",
		"idempotency_key":      "mcp-create-1",
		"agent_session_handle": handleB,
	})
	if created.IsError {
		t.Fatalf("create_issue = %#v", created)
	}
	var issueOut struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	decodeStructured(t, created, &issueOut)
	requireNullString(t, queryNullColumn(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, issueOut.ID), sessionB)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_created'`, issueOut.ID), sessionB)

	createReplay := call(t, client, "create_issue", map[string]any{
		"type":                 "task",
		"title":                "MCP attributed issue",
		"idempotency_key":      "mcp-create-1",
		"agent_session_handle": handleA,
	})
	if createReplay.IsError {
		t.Fatalf("replay create_issue = %#v", createReplay)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, issueOut.ID), sessionB)

	unattributed := call(t, client, "create_issue", map[string]any{"type": "task", "title": "Unattributed"})
	if unattributed.IsError {
		t.Fatalf("unattributed create_issue = %#v", unattributed)
	}
	var unattrOut struct {
		ID string `json:"id"`
	}
	decodeStructured(t, unattributed, &unattrOut)
	if got := queryNullColumn(t, db, `SELECT created_by_session_id FROM issues WHERE id = ?`, unattrOut.ID); got.Valid {
		t.Fatalf("unattributed issues.created_by_session_id = %#v, want NULL", got)
	}
	if got := queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_created'`, unattrOut.ID); got.Valid {
		t.Fatalf("unattributed issue_created.session_id = %#v, want NULL", got)
	}

	// 3. update_issue with handleA + replay with handleB.
	updated := call(t, client, "update_issue", map[string]any{
		"issue_id":             issueOut.ID,
		"expected_version":     issueOut.Version,
		"changes":              map[string]any{"title": "Updated MCP title"},
		"idempotency_key":      "mcp-update-1",
		"agent_session_handle": handleA,
	})
	if updated.IsError {
		t.Fatalf("update_issue = %#v", updated)
	}
	decodeStructured(t, updated, &issueOut)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_updated' ORDER BY id DESC LIMIT 1`, issueOut.ID), sessionA)
	if replayUpd := call(t, client, "update_issue", map[string]any{
		"issue_id":             issueOut.ID,
		"expected_version":     int64(1),
		"changes":              map[string]any{"title": "Updated MCP title"},
		"idempotency_key":      "mcp-update-1",
		"agent_session_handle": handleB,
	}); replayUpd.IsError {
		t.Fatalf("replay update_issue = %#v", replayUpd)
	}
	if count := queryCount(t, db, `SELECT COUNT(*) FROM issue_events WHERE issue_id = ? AND event_type = 'issue_updated'`, issueOut.ID); count != 1 {
		t.Fatalf("issue_updated count after replay = %d, want 1", count)
	}

	// 4. manage_issue_relation (add with handleA, remove with handleB).
	relAdd := call(t, client, "manage_issue_relation", map[string]any{
		"action":               "add",
		"source_issue_id":      issueOut.ID,
		"target_issue_id":      unattrOut.ID,
		"relation_type":        "blocks",
		"idempotency_key":      "mcp-rel-add-1",
		"agent_session_handle": handleA,
	})
	if relAdd.IsError {
		t.Fatalf("manage_issue_relation(add) = %#v", relAdd)
	}
	var relOut struct {
		Relation struct {
			ID string `json:"id"`
		} `json:"relation"`
	}
	decodeStructured(t, relAdd, &relOut)
	requireNullString(t, queryNullColumn(t, db, `SELECT created_by_session_id FROM issue_relations WHERE id = ?`, relOut.Relation.ID), sessionA)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'relation_added' ORDER BY id DESC LIMIT 1`, issueOut.ID), sessionA)

	if relReplay := call(t, client, "manage_issue_relation", map[string]any{
		"action":               "add",
		"source_issue_id":      issueOut.ID,
		"target_issue_id":      unattrOut.ID,
		"relation_type":        "blocks",
		"idempotency_key":      "mcp-rel-add-1",
		"agent_session_handle": handleB,
	}); relReplay.IsError {
		t.Fatalf("replay manage_issue_relation(add) = %#v", relReplay)
	}
	if relRem := call(t, client, "manage_issue_relation", map[string]any{
		"action":               "remove",
		"source_issue_id":      issueOut.ID,
		"target_issue_id":      unattrOut.ID,
		"relation_type":        "blocks",
		"agent_session_handle": handleB,
	}); relRem.IsError {
		t.Fatalf("manage_issue_relation(remove) = %#v", relRem)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'relation_removed' ORDER BY id DESC LIMIT 1`, issueOut.ID), sessionB)

	// 5. archive_issue with handleB + replay with handleA.
	archived := call(t, client, "archive_issue", map[string]any{
		"issue_id":             unattrOut.ID,
		"expected_version":     int64(1),
		"idempotency_key":      "mcp-archive-1",
		"agent_session_handle": handleB,
	})
	if archived.IsError {
		t.Fatalf("archive_issue = %#v", archived)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT archived_by_session_id FROM issues WHERE id = ?`, unattrOut.ID), sessionB)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'issue_archived'`, unattrOut.ID), sessionB)
	if archReplay := call(t, client, "archive_issue", map[string]any{
		"issue_id":             unattrOut.ID,
		"expected_version":     int64(1),
		"idempotency_key":      "mcp-archive-1",
		"agent_session_handle": handleA,
	}); archReplay.IsError {
		t.Fatalf("replay archive_issue = %#v", archReplay)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT archived_by_session_id FROM issues WHERE id = ?`, unattrOut.ID), sessionB)

	// 6. review-request lifecycle: create_review_request (handleA), replace_review_request (handleB + replay handleA), cancel_review_request (handleA).
	targetVersion, targetEventID := currentReviewTargetPosition(t, db, issueOut.ID)
	createdRev := call(t, client, "create_review_request", map[string]any{
		"issue_id":             issueOut.ID,
		"target_issue_version": targetVersion,
		"target_event_id":      targetEventID,
		"purposes":             []string{"implementation"},
		"agent_session_handle": handleA,
	})
	if createdRev.IsError {
		t.Fatalf("create_review_request = %#v", createdRev)
	}
	var revOut struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	decodeStructured(t, createdRev, &revOut)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_requested' ORDER BY id DESC LIMIT 1`, issueOut.ID), sessionA)

	nextVersion, nextEventID := advanceReviewedIssueForTest(t, db, issueOut.ID, source.Now().UTC())
	replaceArgs := func(handle string) map[string]any {
		return map[string]any{
			"predecessor_request_id":       revOut.ID,
			"predecessor_expected_version": revOut.Version,
			"target_issue_version":         nextVersion,
			"target_event_id":              nextEventID,
			"artifact_ids":                 []string{"art-1"},
			"idempotency_key":              "mcp-replace-1",
			"agent_session_handle":         handle,
		}
	}
	replacedRev := call(t, client, "replace_review_request", replaceArgs(handleB))
	if replacedRev.IsError {
		t.Fatalf("replace_review_request = %#v", replacedRev)
	}
	var repOut struct {
		Successor struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"successor"`
	}
	decodeStructured(t, replacedRev, &repOut)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_superseded' ORDER BY id DESC LIMIT 1`, issueOut.ID), sessionB)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_requested' ORDER BY id DESC LIMIT 1`, issueOut.ID), sessionB)

	repReplay := call(t, client, "replace_review_request", replaceArgs(handleA))
	if repReplay.IsError {
		t.Fatalf("replay replace_review_request = %#v", repReplay)
	}
	var repReplayOut struct {
		Successor struct {
			ID string `json:"id"`
		} `json:"successor"`
	}
	decodeStructured(t, repReplay, &repReplayOut)
	if repReplayOut.Successor.ID != repOut.Successor.ID {
		t.Fatalf("replay successor = %q, want %q", repReplayOut.Successor.ID, repOut.Successor.ID)
	}
	if count := queryCount(t, db, `SELECT COUNT(*) FROM issue_events WHERE issue_id = ? AND event_type = 'review_superseded'`, issueOut.ID); count != 1 {
		t.Fatalf("review_superseded count after replay = %d, want 1", count)
	}

	cancelledRev := call(t, client, "cancel_review_request", map[string]any{
		"review_request_id":    repOut.Successor.ID,
		"expected_version":     repOut.Successor.Version,
		"agent_session_handle": handleA,
	})
	if cancelledRev.IsError {
		t.Fatalf("cancel_review_request = %#v", cancelledRev)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_cancelled' ORDER BY id DESC LIMIT 1`, issueOut.ID), sessionA)

	// 7. finish_attempt & cancel_review_request attribution on review/attempt events via MCP:
	revIssueRes := call(t, client, "create_issue", map[string]any{
		"type":                 "task",
		"title":                "MCP review attribution issue",
		"status":               "ready",
		"agent_session_handle": handleA,
	})
	if revIssueRes.IsError {
		t.Fatalf("create_issue(ready) = %#v", revIssueRes)
	}
	var revIssue struct {
		ID string `json:"id"`
	}
	decodeStructured(t, revIssueRes, &revIssue)
	wc0 := call(t, client, "claim_issue", map[string]any{"issue_id": revIssue.ID, "agent_session_handle": handleA})
	if wc0.IsError {
		t.Fatalf("claim_issue(initial work) = %#v", wc0)
	}
	var wc0Out struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, wc0, &wc0Out)
	source.Advance(time.Second)
	if wf0 := call(t, client, "finish_attempt", map[string]any{
		"attempt_id":           wc0Out.Attempt.ID,
		"lease_token":          wc0Out.LeaseToken,
		"outcome":              "completed",
		"target_issue_status":  "review",
		"result_summary":       "ready for review",
		"agent_session_handle": handleA,
	}); wf0.IsError {
		t.Fatalf("finish_attempt(initial work->review) = %#v", wf0)
	}

	// (a) claim review request and cancel_review_request under handleB -> attempt_cancelled has sessionB;
	vCancel, evtCancel := currentReviewTargetPosition(t, db, revIssue.ID)
	reqCancel := call(t, client, "create_review_request", map[string]any{
		"issue_id":             revIssue.ID,
		"target_issue_version": vCancel,
		"target_event_id":      evtCancel,
		"purposes":             []string{"implementation"},
		"agent_session_handle": handleA,
	})
	if reqCancel.IsError {
		t.Fatalf("create_review_request(cancel) = %#v", reqCancel)
	}
	var reqCancelOut struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	decodeStructured(t, reqCancel, &reqCancelOut)
	if clCancel := call(t, client, "claim_issue", map[string]any{"issue_id": revIssue.ID, "agent_session_handle": handleA}); clCancel.IsError {
		t.Fatalf("claim_issue(cancel) = %#v", clCancel)
	}
	if cancelClaimed := call(t, client, "cancel_review_request", map[string]any{
		"review_request_id":    reqCancelOut.ID,
		"expected_version":     reqCancelOut.Version + 1,
		"agent_session_handle": handleB,
	}); cancelClaimed.IsError {
		t.Fatalf("cancel_review_request(claimed) = %#v", cancelClaimed)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'attempt_cancelled' ORDER BY id DESC LIMIT 1`, revIssue.ID), sessionB)

	// (b) claim review request and finish with changes_requested (handleB) -> review_changes_requested has sessionB;
	v3, evt3 := currentReviewTargetPosition(t, db, revIssue.ID)
	req3 := call(t, client, "create_review_request", map[string]any{
		"issue_id":             revIssue.ID,
		"target_issue_version": v3,
		"target_event_id":      evt3,
		"purposes":             []string{"implementation"},
		"agent_session_handle": handleA,
	})
	if req3.IsError {
		t.Fatalf("create_review_request(3) = %#v", req3)
	}
	claim3 := call(t, client, "claim_issue", map[string]any{
		"issue_id":             revIssue.ID,
		"agent_session_handle": handleA,
	})
	if claim3.IsError {
		t.Fatalf("claim_issue(review 3) = %#v", claim3)
	}
	var claim3Out struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, claim3, &claim3Out)
	source.Advance(time.Second)
	rejectRes := call(t, client, "finish_attempt", map[string]any{
		"attempt_id":           claim3Out.Attempt.ID,
		"lease_token":          claim3Out.LeaseToken,
		"outcome":              "completed",
		"review_outcome":       "changes_requested",
		"result_summary":       "needs tests",
		"agent_session_handle": handleB,
	})
	if rejectRes.IsError {
		t.Fatalf("finish_attempt(changes_requested) = %#v", rejectRes)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_changes_requested' ORDER BY id DESC LIMIT 1`, revIssue.ID), sessionB)

	// (c) Reclaim work -> move to review -> create req4 -> make stale -> finish_attempt rejects with review_superseded + attempt_cancelled under handleB;
	workClaim := call(t, client, "claim_issue", map[string]any{"issue_id": revIssue.ID, "agent_session_handle": handleA})
	if workClaim.IsError {
		t.Fatalf("claim_issue(work) = %#v", workClaim)
	}
	var workClaimOut struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, workClaim, &workClaimOut)
	source.Advance(time.Second)
	workDone := call(t, client, "finish_attempt", map[string]any{
		"attempt_id":           workClaimOut.Attempt.ID,
		"lease_token":          workClaimOut.LeaseToken,
		"outcome":              "completed",
		"target_issue_status":  "review",
		"result_summary":       "fixed tests",
		"agent_session_handle": handleA,
	})
	if workDone.IsError {
		t.Fatalf("finish_attempt(work->review) = %#v", workDone)
	}
	v4, evt4 := currentReviewTargetPosition(t, db, revIssue.ID)
	req4 := call(t, client, "create_review_request", map[string]any{
		"issue_id":             revIssue.ID,
		"target_issue_version": v4,
		"target_event_id":      evt4,
		"purposes":             []string{"implementation"},
		"agent_session_handle": handleA,
	})
	if req4.IsError {
		t.Fatalf("create_review_request(4) = %#v", req4)
	}
	claim4 := call(t, client, "claim_issue", map[string]any{"issue_id": revIssue.ID, "agent_session_handle": handleB})
	if claim4.IsError {
		t.Fatalf("claim_issue(review 4) = %#v", claim4)
	}
	var claim4Out struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, claim4, &claim4Out)
	advanceReviewedIssueForTest(t, db, revIssue.ID, source.Now().UTC())
	source.Advance(time.Second)
	staleFinish := call(t, client, "finish_attempt", map[string]any{
		"attempt_id":           claim4Out.Attempt.ID,
		"lease_token":          claim4Out.LeaseToken,
		"outcome":              "completed",
		"review_outcome":       "approved",
		"result_summary":       "stale finish",
		"agent_session_handle": handleB,
	})
	if !staleFinish.IsError {
		t.Fatalf("expected stale finish error, got %#v", staleFinish)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_superseded' ORDER BY id DESC LIMIT 1`, revIssue.ID), sessionB)
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'attempt_cancelled' ORDER BY id DESC LIMIT 1`, revIssue.ID), sessionB)

	// (d) Fresh req5 -> claim -> approve under handleA -> review_approved has sessionA.
	v5, evt5 := currentReviewTargetPosition(t, db, revIssue.ID)
	if req5 := call(t, client, "create_review_request", map[string]any{
		"issue_id":             revIssue.ID,
		"target_issue_version": v5,
		"target_event_id":      evt5,
		"purposes":             []string{"implementation"},
		"agent_session_handle": handleB,
	}); req5.IsError {
		t.Fatalf("create_review_request(5) = %#v", req5)
	}
	claim5 := call(t, client, "claim_issue", map[string]any{"issue_id": revIssue.ID, "agent_session_handle": handleB})
	if claim5.IsError {
		t.Fatalf("claim_issue(review 5) = %#v", claim5)
	}
	var claim5Out struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, claim5, &claim5Out)
	source.Advance(time.Second)
	if approveRes := call(t, client, "finish_attempt", map[string]any{
		"attempt_id":           claim5Out.Attempt.ID,
		"lease_token":          claim5Out.LeaseToken,
		"outcome":              "completed",
		"review_outcome":       "approved",
		"result_summary":       "approved",
		"agent_session_handle": handleA,
	}); approveRes.IsError {
		t.Fatalf("finish_attempt(approved) = %#v", approveRes)
	}
	requireNullString(t, queryNullColumn(t, db, `SELECT session_id FROM issue_events WHERE issue_id = ? AND event_type = 'review_approved' ORDER BY id DESC LIMIT 1`, revIssue.ID), sessionA)
}
