package mcp_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
)

const projectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestRelationToolsLifecycleAndContracts(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "project.db")
	db, source := openDatabase(t, databasePath)
	client, stop := newClient(t, composeServices(t, db, source))

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, len(tools.Tools))
	for i, tool := range tools.Tools {
		names[i] = tool.Name
	}
	// The SDK's feature-set protocol listing is explicitly lexical; registration
	// itself is kept in Phase 2 order in adapter.register.
	wantNames := []string{"add_comment", "apply_issue_plan", "archive_issue", "claim_issue", "create_issue", "finish_attempt", "get_issue", "get_issue_graph", "get_planning_graph", "get_project", "list_issues", "list_labels", "manage_issue_relation", "renew_attempt", "save_attempt_note", "update_issue", "validate_issue_plan"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
	assertRequired(t, toolNamed(t, tools.Tools, "create_issue"), "type", "title")
	assertRequired(t, toolNamed(t, tools.Tools, "add_comment"), "issue_id", "content")
	assertRequired(t, toolNamed(t, tools.Tools, "update_issue"), "issue_id", "expected_version", "changes")
	assertUpdateLabelsSchema(t, toolNamed(t, tools.Tools, "update_issue"))
	assertRequired(t, toolNamed(t, tools.Tools, "get_issue"), "issue_id")
	assertRequired(t, toolNamed(t, tools.Tools, "archive_issue"), "issue_id", "expected_version")
	assertRequired(t, toolNamed(t, tools.Tools, "manage_issue_relation"), "action", "source_issue_id", "target_issue_id", "relation_type")
	assertRequired(t, toolNamed(t, tools.Tools, "get_issue_graph"), "root_issue_id")
	assertRequired(t, toolNamed(t, tools.Tools, "save_attempt_note"), "attempt_id", "lease_token", "kind", "content")
	assertRequired(t, toolNamed(t, tools.Tools, "finish_attempt"), "attempt_id", "lease_token", "outcome", "result_summary")

	project := call(t, client, "get_project", map[string]any{})
	if project.IsError {
		t.Fatalf("get_project result = %#v", project)
	}

	var projectOutput struct {
		Session                any      `json:"session"`
		AppVersion             string   `json:"app_version"`
		ConfigVersion          int      `json:"config_version"`
		SupportedRelationTypes []string `json:"supported_relation_types"`
		Project                struct {
			Instructions *string `json:"instructions"`
		} `json:"project"`
	}
	decodeStructured(t, project, &projectOutput)
	if projectOutput.Session != nil || projectOutput.AppVersion != "test-version" || projectOutput.ConfigVersion != 1 ||
		projectOutput.Project.Instructions != nil ||
		!reflect.DeepEqual(projectOutput.SupportedRelationTypes, []string{"blocks", "related_to", "duplicates"}) {
		t.Fatalf("project output = %#v", projectOutput)
	}
	projectWithInstructions := call(t, client, "get_project", map[string]any{"include_instructions": true})
	decodeStructured(t, projectWithInstructions, &projectOutput)
	if projectOutput.Project.Instructions == nil || *projectOutput.Project.Instructions != "Use focused changes." {
		t.Fatalf("instructions = %#v", projectOutput.Project.Instructions)
	}

	created := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "MCP lifecycle", "description": "clear me", "status": "open",
		"priority": "high", "labels": []string{"Database"}, "create_missing_labels": true,
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Version   int64  `json:"version"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Description *string `json:"description"`
	}
	decodeStructured(t, created, &issue)
	if created.IsError || issue.DisplayID != "ISSUE-1" || issue.Version != 1 || issue.Description == nil ||
		len(issue.Labels) != 1 || issue.Labels[0].Name != "Database" {
		t.Fatalf("create result = %#v, issue = %#v", created, issue)
	}
	comment := call(t, client, "add_comment", map[string]any{
		"issue_id": issue.DisplayID, "content": "  preserved comment  ",
	})
	var commentOutput struct {
		Comment struct {
			ID                 string  `json:"id"`
			IssueID            string  `json:"issue_id"`
			Content            string  `json:"content"`
			CreatedBySessionID *string `json:"created_by_session_id"`
			AuthorLabel        *string `json:"author_label"`
		} `json:"comment"`
	}
	decodeStructured(t, comment, &commentOutput)
	if comment.IsError || commentOutput.Comment.ID == "" || commentOutput.Comment.IssueID != issue.ID ||
		commentOutput.Comment.Content != "  preserved comment  " || commentOutput.Comment.CreatedBySessionID == nil ||
		commentOutput.Comment.AuthorLabel != nil {
		t.Fatalf("comment result = %#v", commentOutput)
	}
	unsupportedComment := call(t, client, "add_comment", map[string]any{
		"issue_id": issue.ID, "content": "not stored", "idempotency_key": "unsupported",
	})
	if !unsupportedComment.IsError {
		t.Fatal("add_comment accepted unsupported idempotency key")
	}
	invalidComment := call(t, client, "add_comment", map[string]any{"issue_id": "bad", "content": "not stored"})
	if !invalidComment.IsError {
		t.Fatal("add_comment accepted invalid issue identifier")
	}

	labels := call(t, client, "list_labels", map[string]any{})
	var labelPage struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	decodeStructured(t, labels, &labelPage)
	if labels.IsError || len(labelPage.Items) != 1 || labelPage.Items[0].Name != "Database" {
		t.Fatalf("labels = %#v", labelPage)
	}

	updated := call(t, client, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 1,
		"changes": map[string]any{"status": "ready", "description": nil},
	})
	var updatedOutput struct {
		Issue struct {
			Version     int64   `json:"version"`
			Description *string `json:"description"`
			Status      string  `json:"status"`
			Labels      []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"issue"`
		ChangedFields []string `json:"changed_fields"`
	}
	decodeStructured(t, updated, &updatedOutput)
	if updated.IsError || updatedOutput.Issue.Version != 2 || updatedOutput.Issue.Description != nil ||
		updatedOutput.Issue.Status != "ready" || !reflect.DeepEqual(updatedOutput.ChangedFields, []string{"description", "status"}) {
		t.Fatalf("update output = %#v", updatedOutput)
	}
	if len(updatedOutput.Issue.Labels) != 1 || updatedOutput.Issue.Labels[0].Name != "Database" {
		t.Fatalf("absent labels did not preserve assignments: %#v", updatedOutput.Issue.Labels)
	}

	got := call(t, client, "get_issue", map[string]any{"issue_id": issue.ID, "view": "full"})
	var gotIssue struct {
		DisplayID string `json:"display_id"`
		Version   int64  `json:"version"`
	}
	decodeStructured(t, got, &gotIssue)
	if got.IsError || gotIssue.DisplayID != issue.DisplayID || gotIssue.Version != 2 {
		t.Fatalf("get output = %#v", gotIssue)
	}

	listed := call(t, client, "list_issues", map[string]any{"view": "compact"})
	var issuePage struct {
		Items []struct {
			DisplayID       string `json:"display_id"`
			EffectiveStatus string `json:"effective_status"`
			IsClaimable     bool   `json:"is_claimable"`
		} `json:"items"`
	}
	decodeStructured(t, listed, &issuePage)
	if listed.IsError || len(issuePage.Items) != 1 || issuePage.Items[0].DisplayID != issue.DisplayID ||
		issuePage.Items[0].EffectiveStatus != "ready" || !issuePage.Items[0].IsClaimable {
		t.Fatalf("list output = %#v", issuePage)
	}

	relatedIssue := call(t, client, "create_issue", map[string]any{"type": "task", "title": "Related"})
	var related struct {
		ID string `json:"id"`
	}
	decodeStructured(t, relatedIssue, &related)
	relation := call(t, client, "manage_issue_relation", map[string]any{
		"action": "add", "source_issue_id": related.ID, "target_issue_id": issue.ID, "relation_type": "related_to",
	})
	var relationOutput struct {
		Relation struct {
			ID            string `json:"id"`
			SourceIssueID string `json:"source_issue_id"`
			TargetIssueID string `json:"target_issue_id"`
			Type          string `json:"type"`
		} `json:"relation"`
		AffectedIssues []struct {
			ID                     string `json:"id"`
			UnresolvedBlockerCount int64  `json:"unresolved_blocker_count"`
			IsBlocked              bool   `json:"is_blocked"`
			IsClaimable            bool   `json:"is_claimable"`
		} `json:"affected_issues"`
		Changed bool `json:"changed"`
	}
	decodeStructured(t, relation, &relationOutput)
	if relation.IsError || !relationOutput.Changed || relationOutput.Relation.Type != "related_to" ||
		relationOutput.Relation.SourceIssueID > relationOutput.Relation.TargetIssueID ||
		len(relationOutput.AffectedIssues) != 2 {
		t.Fatalf("relation output = %#v", relationOutput)
	}
	duplicateRelation := call(t, client, "manage_issue_relation", map[string]any{
		"action": "add", "source_issue_id": issue.ID, "target_issue_id": related.ID, "relation_type": "related_to",
	})
	var duplicateRelationOutput struct {
		Relation struct {
			ID string `json:"id"`
		} `json:"relation"`
		AffectedIssues []struct {
			ID string `json:"id"`
		} `json:"affected_issues"`
		Changed bool `json:"changed"`
	}
	decodeStructured(t, duplicateRelation, &duplicateRelationOutput)
	if duplicateRelation.IsError || duplicateRelationOutput.Changed ||
		duplicateRelationOutput.Relation.ID != relationOutput.Relation.ID ||
		len(duplicateRelationOutput.AffectedIssues) != 2 {
		t.Fatalf("duplicate relation output = %#v", duplicateRelationOutput)
	}
	removedRelation := call(t, client, "manage_issue_relation", map[string]any{
		"action": "remove", "source_issue_id": issue.ID, "target_issue_id": related.ID, "relation_type": "related_to",
	})
	decodeStructured(t, removedRelation, &relationOutput)
	if removedRelation.IsError || !relationOutput.Changed {
		t.Fatalf("remove relation output = %#v", relationOutput)
	}

	conflict := call(t, client, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 1, "changes": map[string]any{"title": "stale"},
	})
	assertDomainError(t, conflict, "VERSION_CONFLICT", true)
	invalidParent := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "invalid parent", "parent_issue_id": "ISSUE-999",
	})
	assertDomainError(t, invalidParent, "INVALID_EPIC_PARENT", false)
	invalidStatus := call(t, client, "create_issue", map[string]any{"type": "task", "title": "invalid status", "status": "in_progress"})
	assertDomainError(t, invalidStatus, "INVALID_ARGUMENT", false)
	invalidTransition := call(t, client, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 2, "changes": map[string]any{"status": "open"},
	})
	assertDomainError(t, invalidTransition, "INVALID_STATUS_TRANSITION", false)
	labelsNull := call(t, client, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 2, "changes": map[string]any{"labels": nil},
	})
	if !labelsNull.IsError {
		t.Fatalf("labels null should be rejected by the advertised schema: %#v", labelsNull)
	}
	unsupported := call(t, client, "get_issue", map[string]any{"issue_id": issue.DisplayID, "include": []string{"labels"}})
	assertDomainError(t, unsupported, "INVALID_ARGUMENT", false)
	unsupportedLimits := call(t, client, "get_issue", map[string]any{"issue_id": issue.DisplayID, "limits": map[string]any{"comments": 1}})
	assertDomainError(t, unsupportedLimits, "INVALID_ARGUMENT", false)
	unsupportedListView := call(t, client, "list_issues", map[string]any{"view": "standard"})
	assertDomainError(t, unsupportedListView, "INVALID_ARGUMENT", false)
	unsupportedIdempotency := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "unsupported idempotency", "idempotency_key": "key",
	})
	assertDomainError(t, unsupportedIdempotency, "INVALID_ARGUMENT", false)

	archived := call(t, client, "archive_issue", map[string]any{"issue_id": issue.DisplayID, "expected_version": 2})
	var archivedIssue struct {
		Version    int64      `json:"version"`
		ArchivedAt *time.Time `json:"archived_at"`
	}
	decodeStructured(t, archived, &archivedIssue)
	if archived.IsError || archivedIssue.Version != 3 || archivedIssue.ArchivedAt == nil {
		t.Fatalf("archive output = %#v", archivedIssue)
	}
	hidden := call(t, client, "list_issues", map[string]any{})
	decodeStructured(t, hidden, &issuePage)
	if hidden.IsError || len(issuePage.Items) != 1 {
		t.Fatalf("default archive visibility = %#v", issuePage)
	}
	visible := call(t, client, "list_issues", map[string]any{"include_archived": true})
	decodeStructured(t, visible, &issuePage)
	if visible.IsError || len(issuePage.Items) != 2 {
		t.Fatalf("archived list visibility = %#v", issuePage)
	}

	stop()
	if err := db.Close(ctx); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	db, source = reopenDatabase(t, databasePath, source)
	restartedClient, restartedStop := newClient(t, composeServices(t, db, source))
	restarted := call(t, restartedClient, "get_issue", map[string]any{"issue_id": issue.DisplayID})
	decodeStructured(t, restarted, &archivedIssue)
	if restarted.IsError || archivedIssue.Version != 3 || archivedIssue.ArchivedAt == nil {
		t.Fatalf("restarted get = %#v", archivedIssue)
	}
	restartedStop()
	if err := db.Close(ctx); err != nil {
		t.Fatalf("close after restart: %v", err)
	}
}

func TestGraphToolsLifecycleAndValidation(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "project.db"))
	defer func() {
		if err := db.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()

	root := call(t, client, "create_issue", map[string]any{"type": "task", "title": "Root", "status": "ready"})
	var issue struct {
		ID string `json:"id"`
	}
	decodeStructured(t, root, &issue)
	graph := call(t, client, "get_issue_graph", map[string]any{"root_issue_id": issue.ID, "depth": 0, "max_nodes": 1})
	var output struct {
		RootIssueID string `json:"root_issue_id"`
		Nodes       []struct {
			ID string `json:"id"`
		} `json:"nodes"`
		Truncated        bool    `json:"truncated"`
		TruncationReason *string `json:"truncation_reason"`
	}
	decodeStructured(t, graph, &output)
	if graph.IsError || output.RootIssueID != issue.ID || len(output.Nodes) != 1 || output.Truncated || output.TruncationReason != nil {
		t.Fatalf("issue graph = %#v", output)
	}
	planning := call(t, client, "get_planning_graph", map[string]any{"max_nodes": 1})
	if planning.IsError {
		t.Fatalf("planning graph = %#v", planning)
	}
	invalid := call(t, client, "get_issue_graph", map[string]any{"root_issue_id": issue.ID, "max_nodes": 0})
	if !invalid.IsError {
		t.Fatalf("schema accepted invalid graph limit: %#v", invalid)
	}
}

func TestNewServerRequiresRelationService(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "project.db")
	db, source := openDatabase(t, databasePath)
	defer func() {
		if err := db.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	options := composeServices(t, db, source)
	options.RelationService = nil
	_, err := mcpadapter.NewServer(options)
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("NewServer() error = %v, want missing relation service error", err)
	}
}

func TestNewServerRequiresSessionService(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "project.db"))
	defer func() {
		if err := db.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	options := composeServices(t, db, source)
	options.SessionService = nil
	_, err := mcpadapter.NewServer(options)
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("NewServer() error = %v, want missing session service error", err)
	}
}

func TestAgentSessionLifecyclePersistence(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "sessions.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))

	waitForAgentSession(t, db)
	session := readAgentSession(t, db)
	if session.Count != 1 || session.ClientName != "test-client" || session.ClientVersion != "test" ||
		!session.StartedAt.Equal(source.Now()) || !session.LastSeenAt.Equal(source.Now()) || session.EndedAt != nil {
		t.Fatalf("initial agent session = %#v", session)
	}

	source.Advance(time.Minute)
	result := call(t, client, "get_project", map[string]any{})
	if result.IsError {
		t.Fatalf("get_project result = %#v", result)
	}
	session = readAgentSession(t, db)
	if !session.LastSeenAt.Equal(source.Now()) {
		t.Fatalf("touched LastSeenAt = %v, want %v", session.LastSeenAt, source.Now())
	}

	stop()
	stop()
	session = readAgentSession(t, db)
	if session.Count != 1 || session.EndedAt == nil || !session.EndedAt.Equal(session.LastSeenAt) {
		t.Fatalf("ended agent session = %#v", session)
	}
	endedAt := *session.EndedAt
	stop()
	session = readAgentSession(t, db)
	if session.Count != 1 || session.EndedAt == nil || !session.EndedAt.Equal(endedAt) {
		t.Fatalf("repeated shutdown changed agent session = %#v", session)
	}
}

func TestAttemptEventsFollowCurrentMCPConnectionSession(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "attempt-sessions.db"))
	defer db.Close(ctx)
	clientA, stopA := newClient(t, composeServices(t, db, source))
	created := call(t, clientA, "create_issue", map[string]any{"type": "task", "title": "handoff", "status": "ready"})
	var issue struct {
		ID string `json:"id"`
	}
	decodeStructured(t, created, &issue)
	claimed := call(t, clientA, "claim_issue", map[string]any{"issue_id": issue.ID})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, claimed, &claim)
	if created.IsError || claimed.IsError || claim.Attempt.ID == "" || claim.LeaseToken == "" {
		t.Fatalf("connection A outputs = created %#v claimed %#v", created, claimed)
	}
	stopA()
	clientB, stopB := newClient(t, composeServices(t, db, source))
	defer stopB()
	note := call(t, clientB, "save_attempt_note", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken, "kind": "checkpoint", "content": "handoff checkpoint",
	})
	if note.IsError {
		t.Fatalf("connection B note = %#v", note)
	}
	finished := call(t, clientB, "finish_attempt", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken,
		"outcome": "completed", "result_summary": "done", "target_issue_status": "done",
	})
	if finished.IsError {
		t.Fatalf("connection B finish = %#v", finished)
	}
	var attemptSession string
	var events []struct {
		Type    string
		Session sql.NullString
	}
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT session_id FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&attemptSession); err != nil {
			return err
		}
		rows, err := query.QueryContext(ctx, `SELECT event_type, session_id FROM issue_events WHERE attempt_id = ? ORDER BY id`, claim.Attempt.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event struct {
				Type    string
				Session sql.NullString
			}
			if err := rows.Scan(&event.Type, &event.Session); err != nil {
				return err
			}
			events = append(events, event)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if attemptSession == "" || len(events) != 3 || !events[0].Session.Valid || !events[1].Session.Valid || !events[2].Session.Valid ||
		events[0].Type != "attempt_started" || events[1].Type != "checkpoint_saved" || events[2].Type != "attempt_completed" ||
		events[0].Session.String == events[1].Session.String || events[1].Session.String != events[2].Session.String ||
		attemptSession != events[0].Session.String {
		t.Fatalf("attempt session attribution = attempt %q events %#v", attemptSession, events)
	}
}

func waitForAgentSession(t *testing.T, db *sqlite.DB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if readAgentSession(t, db).Count == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("agent session was not created")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAgentSessionCreationFailureDoesNotChangeToolLifecycle(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "failed-session.db"))
	defer db.Close(ctx)
	options := composeServices(t, db, source)
	repository, err := sqlite.NewAgentSessionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	options.SessionService, err = application.NewAgentSessionService(repository, source, failingSessionIDGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	client, stop := newClient(t, options)
	result := call(t, client, "get_project", map[string]any{})
	if result.IsError {
		t.Fatalf("get_project result after session creation failure = %#v", result)
	}
	created := call(t, client, "create_issue", map[string]any{"type": "task", "title": "unmapped", "status": "ready"})
	var issue struct {
		ID string `json:"id"`
	}
	decodeStructured(t, created, &issue)
	claimed := call(t, client, "claim_issue", map[string]any{"issue_id": issue.ID})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
	}
	decodeStructured(t, claimed, &claim)
	if claimed.IsError || claim.Attempt.ID == "" {
		t.Fatalf("claim after session creation failure = %#v", claimed)
	}
	stop()
	if session := readAgentSession(t, db); session.Count != 0 {
		t.Fatalf("agent sessions after creation failure = %#v", session)
	}
	var attemptSession, eventSession sql.NullString
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT session_id FROM work_attempts WHERE id = ?`, claim.Attempt.ID).Scan(&attemptSession); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT session_id FROM issue_events WHERE attempt_id = ? AND event_type = 'attempt_started'`, claim.Attempt.ID).Scan(&eventSession)
	}); err != nil {
		t.Fatal(err)
	}
	if attemptSession.Valid || eventSession.Valid {
		t.Fatalf("unmapped claim sessions = attempt %#v event %#v", attemptSession, eventSession)
	}
}

type failingSessionIDGenerator struct{}

func (failingSessionIDGenerator) New() (string, error) {
	return "", errors.New("session ID generation failed")
}

type agentSessionRow struct {
	Count         int
	ClientName    string
	ClientVersion string
	StartedAt     time.Time
	LastSeenAt    time.Time
	EndedAt       *time.Time
}

func readAgentSession(t *testing.T, db *sqlite.DB) agentSessionRow {
	t.Helper()
	var result agentSessionRow
	err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_sessions").Scan(&result.Count); err != nil {
			return err
		}
		if result.Count == 0 {
			return nil
		}
		var (
			startedAt, lastSeenAt  string
			clientVersion, endedAt sql.NullString
		)
		if err := query.QueryRowContext(ctx, `SELECT client_name, client_version, started_at, last_seen_at, ended_at
			FROM agent_sessions ORDER BY started_at, id LIMIT 1`).
			Scan(&result.ClientName, &clientVersion, &startedAt, &lastSeenAt, &endedAt); err != nil {
			return err
		}
		result.ClientVersion = clientVersion.String
		var err error
		if result.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt); err != nil {
			return err
		}
		if result.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenAt); err != nil {
			return err
		}
		if endedAt.Valid {
			ended, err := time.Parse(time.RFC3339Nano, endedAt.String)
			if err != nil {
				return err
			}
			result.EndedAt = &ended
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAttemptToolsLifecycle(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "project.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()
	created := call(t, client, "create_issue", map[string]any{"type": "task", "title": "leased", "status": "ready"})
	var issue struct {
		ID string `json:"id"`
	}
	decodeStructured(t, created, &issue)
	claimed := call(t, client, "claim_issue", map[string]any{"issue_id": issue.ID, "lease_seconds": 60})
	var output struct {
		Issue struct {
			EffectiveStatus string  `json:"effective_status"`
			ActiveAttemptID *string `json:"active_attempt_id"`
		} `json:"issue"`
		Attempt struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, claimed, &output)
	if claimed.IsError || output.Issue.EffectiveStatus != "in_progress" || output.Issue.ActiveAttemptID == nil ||
		*output.Issue.ActiveAttemptID != output.Attempt.ID || output.Attempt.Kind != "work" || output.LeaseToken == "" {
		t.Fatalf("claim metadata = status %q active ID present %t kind %q token present %t",
			output.Issue.EffectiveStatus, output.Issue.ActiveAttemptID != nil, output.Attempt.Kind, output.LeaseToken != "")
	}
	saved := call(t, client, "save_attempt_note", map[string]any{
		"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "kind": "checkpoint",
		"content": "durable checkpoint", "next_steps": []string{"resume"}, "important": true, "artifacts": []any{},
	})
	var noteOutput struct {
		AttemptNote struct {
			ID        string    `json:"id"`
			AttemptID string    `json:"attempt_id"`
			Kind      string    `json:"kind"`
			Content   string    `json:"content"`
			NextSteps []string  `json:"next_steps"`
			Important bool      `json:"important"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"attempt_note"`
		Artifacts []struct {
			ID        string          `json:"id"`
			IssueID   string          `json:"issue_id"`
			AttemptID *string         `json:"attempt_id"`
			Type      string          `json:"type"`
			URI       string          `json:"uri"`
			Title     *string         `json:"title"`
			Metadata  json.RawMessage `json:"metadata"`
			CreatedAt time.Time       `json:"created_at"`
		} `json:"artifacts"`
	}
	decodeStructured(t, saved, &noteOutput)
	if saved.IsError || noteOutput.AttemptNote.ID == "" || noteOutput.AttemptNote.AttemptID != output.Attempt.ID ||
		noteOutput.AttemptNote.Kind != "checkpoint" || noteOutput.AttemptNote.Content != "durable checkpoint" ||
		!reflect.DeepEqual(noteOutput.AttemptNote.NextSteps, []string{"resume"}) || !noteOutput.AttemptNote.Important ||
		noteOutput.AttemptNote.CreatedAt.IsZero() || len(noteOutput.Artifacts) != 0 {
		t.Fatalf("save note output = %#v", noteOutput)
	}
	savedWithArtifacts := call(t, client, "save_attempt_note", map[string]any{
		"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "kind": "progress",
		"content": "note", "artifacts": []any{
			map[string]any{"type": "file", "uri": "internal/application/attempt_service.go", "title": "service", "metadata": map[string]any{"language": "go"}},
			map[string]any{"type": "url", "uri": "https://example.invalid/build/42"},
		},
	})
	var artifactOutput struct {
		Artifacts []struct {
			ID        string         `json:"id"`
			IssueID   string         `json:"issue_id"`
			AttemptID *string        `json:"attempt_id"`
			Type      string         `json:"type"`
			URI       string         `json:"uri"`
			Title     *string        `json:"title"`
			Metadata  map[string]any `json:"metadata"`
			CreatedAt time.Time      `json:"created_at"`
		} `json:"artifacts"`
	}
	decodeStructured(t, savedWithArtifacts, &artifactOutput)
	if savedWithArtifacts.IsError || len(artifactOutput.Artifacts) != 2 ||
		artifactOutput.Artifacts[0].ID == "" || artifactOutput.Artifacts[0].IssueID != issue.ID ||
		artifactOutput.Artifacts[0].AttemptID == nil || *artifactOutput.Artifacts[0].AttemptID != output.Attempt.ID ||
		artifactOutput.Artifacts[0].Type != "file" || artifactOutput.Artifacts[0].URI != "internal/application/attempt_service.go" ||
		artifactOutput.Artifacts[0].Title == nil || *artifactOutput.Artifacts[0].Title != "service" ||
		artifactOutput.Artifacts[0].Metadata["language"] != "go" || artifactOutput.Artifacts[0].CreatedAt.IsZero() {
		t.Fatalf("save note artifacts output = %#v", artifactOutput)
	}
	unsafeArtifacts := call(t, client, "save_attempt_note", map[string]any{
		"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "kind": "progress",
		"content": "note", "artifacts": []any{map[string]any{"type": "file", "uri": "../outside"}},
	})
	assertDomainError(t, unsafeArtifacts, "INVALID_ARGUMENT", false)
	unsupportedIdempotency := call(t, client, "save_attempt_note", map[string]any{
		"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "kind": "progress",
		"content": "note", "idempotency_key": "unsupported",
	})
	assertDomainError(t, unsupportedIdempotency, "INVALID_ARGUMENT", false)
	listed := call(t, client, "list_issues", map[string]any{"effective_statuses": []string{"in_progress"}})
	var listedOutput struct {
		Items []struct {
			EffectiveStatus string  `json:"effective_status"`
			ActiveAttemptID *string `json:"active_attempt_id"`
		} `json:"items"`
	}
	decodeStructured(t, listed, &listedOutput)
	if listed.IsError || len(listedOutput.Items) != 1 || listedOutput.Items[0].EffectiveStatus != "in_progress" ||
		listedOutput.Items[0].ActiveAttemptID == nil || *listedOutput.Items[0].ActiveAttemptID != output.Attempt.ID {
		t.Fatalf("listed output = %#v", listedOutput)
	}
	renewed := call(t, client, "renew_attempt", map[string]any{"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "lease_seconds": 60})
	if renewed.IsError {
		t.Fatalf("renew result = %#v", renewed)
	}
	invalid := call(t, client, "renew_attempt", map[string]any{"attempt_id": output.Attempt.ID, "lease_token": "bad"})
	assertDomainError(t, invalid, "INVALID_LEASE_TOKEN", false)
	invalidFinishArtifacts := call(t, client, "finish_attempt", map[string]any{
		"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "outcome": "failed",
		"result_summary": "done", "failure_reason_code": "other", "artifacts": []any{map[string]any{"type": "file", "uri": "../outside"}},
	})
	assertDomainError(t, invalidFinishArtifacts, "INVALID_ARGUMENT", false)
	finished := call(t, client, "finish_attempt", map[string]any{
		"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "outcome": "completed",
		"result_summary": "implemented", "target_issue_status": "done", "verification": []string{"tests"},
		"idempotency_key": "finish-retry",
		"artifacts": []any{
			map[string]any{"type": "file", "uri": "internal/application/attempt_service.go", "title": "service", "metadata": map[string]any{"language": "go"}},
			map[string]any{"type": "url", "uri": "https://example.invalid/build/42"},
		},
	})
	replayed := call(t, client, "finish_attempt", map[string]any{
		"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "outcome": "completed",
		"result_summary": "implemented", "target_issue_status": "done", "verification": []string{"tests"},
		"idempotency_key": "finish-retry",
		"artifacts": []any{
			map[string]any{"type": "file", "uri": "internal/application/attempt_service.go", "title": "service", "metadata": map[string]any{"language": "go"}},
			map[string]any{"type": "url", "uri": "https://example.invalid/build/42"},
		},
	})
	if replayed.IsError || !reflect.DeepEqual(finished.StructuredContent, replayed.StructuredContent) {
		t.Fatalf("finish replay = %#v, first = %#v", replayed, finished)
	}
	conflict := call(t, client, "finish_attempt", map[string]any{
		"attempt_id": output.Attempt.ID, "lease_token": output.LeaseToken, "outcome": "completed",
		"result_summary": "changed", "target_issue_status": "done", "idempotency_key": "finish-retry",
	})
	assertDomainError(t, conflict, "IDEMPOTENCY_CONFLICT", false)
	var finishOutput struct {
		Attempt struct {
			Status        string   `json:"status"`
			ResultSummary *string  `json:"result_summary"`
			Verification  []string `json:"verification"`
		} `json:"attempt"`
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
		LatestEventID int64    `json:"latest_event_id"`
		Warnings      []string `json:"warnings"`
		Artifacts     []struct {
			ID        string         `json:"id"`
			IssueID   string         `json:"issue_id"`
			AttemptID *string        `json:"attempt_id"`
			Type      string         `json:"type"`
			URI       string         `json:"uri"`
			Title     *string        `json:"title"`
			Metadata  map[string]any `json:"metadata"`
			CreatedAt time.Time      `json:"created_at"`
		} `json:"artifacts"`
	}
	decodeStructured(t, finished, &finishOutput)
	if finished.IsError || finishOutput.Attempt.Status != "completed" || finishOutput.Issue.Status != "done" ||
		finishOutput.Attempt.ResultSummary == nil || *finishOutput.Attempt.ResultSummary != "implemented" ||
		!reflect.DeepEqual(finishOutput.Attempt.Verification, []string{"tests"}) || finishOutput.LatestEventID <= 0 ||
		finishOutput.Warnings == nil || len(finishOutput.Artifacts) != 2 ||
		finishOutput.Artifacts[0].ID == "" || finishOutput.Artifacts[0].IssueID != issue.ID ||
		finishOutput.Artifacts[0].AttemptID == nil || *finishOutput.Artifacts[0].AttemptID != output.Attempt.ID ||
		finishOutput.Artifacts[0].Title == nil || *finishOutput.Artifacts[0].Title != "service" ||
		finishOutput.Artifacts[0].Metadata["language"] != "go" {
		t.Fatalf("finish output = %#v", finishOutput)
	}
	emptyCreated := call(t, client, "create_issue", map[string]any{"type": "task", "title": "empty final artifacts", "status": "ready"})
	var emptyIssue struct {
		ID string `json:"id"`
	}
	decodeStructured(t, emptyCreated, &emptyIssue)
	emptyClaim := call(t, client, "claim_issue", map[string]any{"issue_id": emptyIssue.ID})
	var emptyClaimOutput struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, emptyClaim, &emptyClaimOutput)
	emptyFinished := call(t, client, "finish_attempt", map[string]any{
		"attempt_id": emptyClaimOutput.Attempt.ID, "lease_token": emptyClaimOutput.LeaseToken,
		"outcome": "failed", "result_summary": "no artifacts", "failure_reason_code": "other",
	})
	var emptyFinishOutput struct {
		Artifacts []json.RawMessage `json:"artifacts"`
	}
	decodeStructured(t, emptyFinished, &emptyFinishOutput)
	if emptyFinished.IsError || emptyFinishOutput.Artifacts == nil || len(emptyFinishOutput.Artifacts) != 0 {
		t.Fatalf("empty finish artifacts = %#v", emptyFinishOutput)
	}
}

func TestRelationToolsExposeDerivedBlockersAndArchivedEndpointErrors(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "project.db"))
	client, stop := newClient(t, composeServices(t, db, source))
	defer func() {
		stop()
		if err := db.Close(ctx); err != nil {
			t.Error(err)
		}

	}()

	type issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Version   int64  `json:"version"`
	}
	create := func(title string) issue {
		result := call(t, client, "create_issue", map[string]any{
			"type": "task", "title": title, "status": "ready",
		})
		var created issue
		decodeStructured(t, result, &created)
		if result.IsError {
			t.Fatalf("create %s = %#v", title, result)
		}
		return created
	}
	target, blocker := create("Target"), create("Blocker")
	add := call(t, client, "manage_issue_relation", map[string]any{
		"action": "add", "source_issue_id": blocker.ID, "target_issue_id": target.ID, "relation_type": "blocks",
	})
	var relation struct {
		Relation struct {
			ID string `json:"id"`
		} `json:"relation"`
		AffectedIssues []struct {
			ID                     string `json:"id"`
			UnresolvedBlockerCount int64  `json:"unresolved_blocker_count"`
			IsBlocked              bool   `json:"is_blocked"`
			IsClaimable            bool   `json:"is_claimable"`
		} `json:"affected_issues"`
		Changed bool `json:"changed"`
	}
	decodeStructured(t, add, &relation)
	if add.IsError || !relation.Changed || relation.Relation.ID == "" ||
		len(relation.AffectedIssues) != 2 ||
		relation.AffectedIssues[1].ID != target.ID ||
		relation.AffectedIssues[1].UnresolvedBlockerCount != 1 ||
		!relation.AffectedIssues[1].IsBlocked || relation.AffectedIssues[1].IsClaimable {
		t.Fatalf("add blocker output = %#v", relation)
	}

	repeat := call(t, client, "manage_issue_relation", map[string]any{
		"action": "add", "source_issue_id": blocker.DisplayID, "target_issue_id": target.DisplayID, "relation_type": "blocks",
	})
	var repeated struct {
		Relation struct {
			ID string `json:"id"`
		} `json:"relation"`
		Changed bool `json:"changed"`
	}
	decodeStructured(t, repeat, &repeated)
	if repeat.IsError || repeated.Changed || repeated.Relation.ID != relation.Relation.ID {
		t.Fatalf("repeated add output = %#v", repeated)
	}

	done := call(t, client, "update_issue", map[string]any{
		"issue_id": blocker.ID, "expected_version": blocker.Version, "changes": map[string]any{"status": "done"},
	})
	if done.IsError {
		t.Fatalf("complete blocker = %#v", done)
	}
	listed := call(t, client, "list_issues", map[string]any{"is_claimable": true})
	var page struct {
		Items []struct {
			ID                     string `json:"id"`
			UnresolvedBlockerCount int64  `json:"unresolved_blocker_count"`
			IsBlocked              bool   `json:"is_blocked"`
			IsClaimable            bool   `json:"is_claimable"`
		} `json:"items"`
	}
	decodeStructured(t, listed, &page)
	foundTarget := false
	for _, item := range page.Items {
		if item.ID == target.ID {
			foundTarget = item.UnresolvedBlockerCount == 0 && !item.IsBlocked && item.IsClaimable
		}
	}
	if listed.IsError || !foundTarget {
		t.Fatalf("claimable target after blocker completion = %#v", page)
	}

	archivedTarget := call(t, client, "archive_issue", map[string]any{
		"issue_id": target.ID, "expected_version": target.Version,
	})
	if archivedTarget.IsError {
		t.Fatalf("archive target = %#v", archivedTarget)
	}
	archivedTargetAdd := call(t, client, "manage_issue_relation", map[string]any{
		"action": "add", "source_issue_id": blocker.ID, "target_issue_id": target.ID, "relation_type": "related_to",
	})
	assertDomainError(t, archivedTargetAdd, "ISSUE_ARCHIVED", false)

	secondTarget, secondSource := create("Second target"), create("Second source")
	if result := call(t, client, "manage_issue_relation", map[string]any{
		"action": "add", "source_issue_id": secondSource.ID, "target_issue_id": secondTarget.ID, "relation_type": "blocks",
	}); result.IsError {
		t.Fatalf("prepare source archive relation = %#v", result)
	}
	archivedSource := call(t, client, "archive_issue", map[string]any{
		"issue_id": secondSource.ID, "expected_version": secondSource.Version,
	})
	if archivedSource.IsError {
		t.Fatalf("archive source = %#v", archivedSource)
	}
	archivedSourceRemove := call(t, client, "manage_issue_relation", map[string]any{
		"action": "remove", "source_issue_id": secondSource.ID, "target_issue_id": secondTarget.ID, "relation_type": "blocks",
	})
	assertDomainError(t, archivedSourceRemove, "ISSUE_ARCHIVED", false)

	missing := call(t, client, "manage_issue_relation", map[string]any{
		"action": "remove", "source_issue_id": blocker.ID, "target_issue_id": secondTarget.ID, "relation_type": "duplicates",
	})
	var missingOutput struct {
		Changed bool `json:"changed"`
	}
	decodeStructured(t, missing, &missingOutput)
	if missing.IsError || missingOutput.Changed {
		t.Fatalf("missing remove output = %#v", missingOutput)
	}
}

func TestIssuePlanToolsValidateAndApply(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "plan.db"))
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()
	plan := map[string]any{
		"issues": []any{
			map[string]any{"ref": "epic", "type": "epic", "title": "Plan epic"},
			map[string]any{"ref": "task", "type": "task", "title": "Plan task", "parent_ref": "epic"},
		},
		"relations": []any{map[string]any{"source_ref": "epic", "target_ref": "task", "type": "blocks"}},
		"decisions": []any{map[string]any{"issue_ref": "task", "title": "Choice", "summary": "short", "content": "long"}},
	}
	validation := call(t, client, "validate_issue_plan", plan)
	var checked struct {
		Valid          bool `json:"valid"`
		NormalizedPlan struct {
			Issues []struct {
				Status string `json:"status"`
			} `json:"issues"`
		} `json:"normalized_plan"`
	}
	decodeStructured(t, validation, &checked)
	if validation.IsError || !checked.Valid || checked.NormalizedPlan.Issues[0].Status != "open" {
		t.Fatalf("validation = %#v, output = %#v", validation, checked)
	}
	plan["idempotency_key"] = "mcp-plan-key"
	applied := call(t, client, "apply_issue_plan", plan)
	var result struct {
		CreatedIssues []struct {
			Ref string `json:"ref"`
		} `json:"created_issues"`
		CreatedRelations []struct {
			Type string `json:"type"`
		} `json:"created_relations"`
		CreatedDecisions []struct {
			ID string `json:"id"`
		} `json:"created_decisions"`
		LatestEventID int64 `json:"latest_event_id"`
	}
	decodeStructured(t, applied, &result)
	if applied.IsError || len(result.CreatedIssues) != 2 || result.CreatedIssues[1].Ref != "task" ||
		len(result.CreatedRelations) != 1 || len(result.CreatedDecisions) != 1 || result.LatestEventID == 0 {
		t.Fatalf("apply = %#v, output = %#v", applied, result)
	}
	replay := call(t, client, "apply_issue_plan", plan)
	if replay.IsError {
		t.Fatalf("replay = %#v", replay)
	}
	_ = ctx
}

func openDatabase(t *testing.T, path string) (*sqlite.DB, *clock.FakeClock) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	source := clock.NewFakeClock(time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC))
	if _, err := migrations.Migrate(context.Background(), db, source); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, instructions, next_issue_number, created_at, updated_at)
			VALUES (?, ?, 1, ?, ?)`, projectID, "Use focused changes.", source.Now().Format(time.RFC3339Nano), source.Now().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return db, source
}

func reopenDatabase(t *testing.T, path string, source *clock.FakeClock) (*sqlite.DB, *clock.FakeClock) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(context.Background(), db, source); err != nil {
		t.Fatal(err)
	}
	return db, source
}

func composeServices(t *testing.T, db *sqlite.DB, source *clock.FakeClock) mcpadapter.Options {
	t.Helper()
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	projectRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	relationRepository, err := sqlite.NewRelationRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	graphRepository, err := sqlite.NewGraphRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	planningRepository, err := sqlite.NewPlanningRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	commentRepository, err := sqlite.NewCommentRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	attemptRepository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := application.NewIssueService(issueRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := application.NewProjectService(projectRepository)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := application.NewRelationService(relationRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	graphs, err := application.NewGraphService(graphRepository, source)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := application.NewPlanningService(planningRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	comments, err := application.NewCommentService(commentRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := application.NewAttemptService(attemptRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	sessionRepository, err := sqlite.NewAgentSessionRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := application.NewAgentSessionService(sessionRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	return mcpadapter.Options{
		IssueService: issues, ProjectService: projects, RelationService: relations, GraphService: graphs, PlanningService: plans, CommentService: comments, AttemptService: attempts, SessionService: sessions,
		ServerName: "test-server", ServerVersion: "test-version", ConfigVersion: 1,
	}
}

func newClient(t *testing.T, options mcpadapter.Options) (*sdkmcp.ClientSession, func()) {
	t.Helper()
	server, err := mcpadapter.NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var once bool
	return session, func() {
		if once {
			return
		}
		once = true
		_ = session.Close()
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("server.Run() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}
}

func call(t *testing.T, client *sdkmcp.ClientSession, name string, arguments map[string]any) *sdkmcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s protocol error: %v", name, err)
	}
	return result
}

func decodeStructured(t *testing.T, result *sdkmcp.CallToolResult, destination any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode structured content %s: %v", data, err)
	}
}

func assertDomainError(t *testing.T, result *sdkmcp.CallToolResult, code string, retryable bool) {
	t.Helper()
	var output struct {
		Code      string            `json:"code"`
		Message   string            `json:"message"`
		Details   []json.RawMessage `json:"details"`
		Retryable bool              `json:"retryable"`
	}
	decodeStructured(t, result, &output)
	if !result.IsError || output.Code != code || output.Message == "" || output.Details == nil || output.Retryable != retryable {
		t.Fatalf("error result = %#v, output = %#v", result, output)
	}
}

func assertRequired(t *testing.T, tool *sdkmcp.Tool, fields ...string) {
	t.Helper()
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}

	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		if !contains(schema.Required, field) {
			t.Fatalf("%s required fields %v do not contain %q", tool.Name, schema.Required, field)
		}
	}
}

func assertUpdateLabelsSchema(t *testing.T, tool *sdkmcp.Tool) {
	t.Helper()
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}

	var schema struct {
		Properties struct {
			Changes struct {
				Properties struct {
					Labels struct {
						Type  string   `json:"type"`
						Types []string `json:"types"`
					} `json:"labels"`
				} `json:"properties"`
			} `json:"changes"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties.Changes.Properties.Labels.Type != "array" {
		t.Fatalf("update_issue changes.labels type = %q, want array", schema.Properties.Changes.Properties.Labels.Type)
	}
	if contains(schema.Properties.Changes.Properties.Labels.Types, "null") {
		t.Fatal("update_issue changes.labels schema must not permit null")
	}
}

func toolNamed(t *testing.T, tools []*sdkmcp.Tool, name string) *sdkmcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("missing tool %q", name)
	return nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
