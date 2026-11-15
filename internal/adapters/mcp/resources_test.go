package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectrouting"
)

type spyProjectRouter struct {
	inner                projectrouting.ProjectRouter
	acquireCount         int
	successfulLeaseCount int
	lastExplicitRef      *string
	openCount            int
	releaseCount         int
	acquireErrOverride   error
}

type spyProjectLease struct {
	inner     projectrouting.ProjectLease
	onRelease func()
	rootPath  string
}

func (r *spyProjectRouter) Acquire(ctx context.Context, explicitRef *string) (projectrouting.ProjectLease, error) {
	r.acquireCount++
	if explicitRef == nil {
		r.lastExplicitRef = nil
	} else {
		copied := *explicitRef
		r.lastExplicitRef = &copied
	}
	if r.acquireErrOverride != nil {
		return nil, r.acquireErrOverride
	}
	lease, err := r.inner.Acquire(ctx, explicitRef)
	if err != nil {
		return nil, err
	}
	r.successfulLeaseCount++
	return &spyProjectLease{
		inner: lease,
		onRelease: func() {
			r.releaseCount++
		},
	}, nil
}

func (r *spyProjectRouter) OpenProject(ctx context.Context, absoluteRoot string) (projectrouting.ProjectLease, error) {
	r.openCount++
	lease, err := r.inner.OpenProject(ctx, absoluteRoot)
	if err != nil {
		return nil, err
	}
	return &spyProjectLease{
		inner: lease,
		onRelease: func() {
			r.releaseCount++
		},
		rootPath: absoluteRoot,
	}, nil
}

func (l *spyProjectLease) ProjectRef() string          { return l.inner.ProjectRef() }
func (l *spyProjectLease) Services() application.Bundle { return l.inner.Services() }
func (l *spyProjectLease) Release() error {
	if l.onRelease != nil {
		l.onRelease()
	}
	return l.inner.Release()
}
func (l *spyProjectLease) RootPath() string { return l.rootPath }

type issueBriefDoc struct {
	ProjectRef string `json:"project_ref"`
	Issue      struct {
		ID                 string  `json:"id"`
		DisplayID          string  `json:"display_id"`
		SequenceNo         int64   `json:"sequence_no"`
		Type               string  `json:"type"`
		Title              string  `json:"title"`
		Status             string  `json:"status"`
		EffectiveStatus    string  `json:"effective_status"`
		Priority           string  `json:"priority"`
		ParentIssueID      *string `json:"parent_issue_id"`
		BlockedReason      *string `json:"blocked_reason"`
		Version            int64   `json:"version"`
		Description        *string `json:"description"`
		AcceptanceCriteria *string `json:"acceptance_criteria"`
	} `json:"issue"`
	Gates struct {
		EnforcementPoint string `json:"enforcement_point"`
		Passed           bool   `json:"passed"`
		RequirementCount int64  `json:"requirement_count"`
		SatisfiedCount   int64  `json:"satisfied_count"`
		Unmet            []struct {
			PolicyID       string `json:"policy_id"`
			RequirementKey string `json:"requirement_key"`
			Reason         string `json:"reason"`
		} `json:"unmet"`
	} `json:"gates"`
	Blockers []struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
	} `json:"blockers"`
	Decisions []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Status  string `json:"status"`
	} `json:"decisions"`
	Reviews []struct {
		ID                 string     `json:"id"`
		Status             string     `json:"status"`
		Purposes           []string   `json:"purposes"`
		TargetIssueVersion int64      `json:"target_issue_version"`
		CreatedAt          time.Time  `json:"created_at"`
		ResolvedAt         *time.Time `json:"resolved_at"`
	} `json:"reviews"`
	Warnings         []string `json:"warnings"`
	Truncated        bool     `json:"truncated"`
	TruncationReason *string  `json:"truncation_reason"`
}

type projectBriefDoc struct {
	Project struct {
		ProjectRef string  `json:"project_ref"`
		ID         string  `json:"id"`
		Name       *string `json:"name"`
		RootPath   *string `json:"root_path"`
	} `json:"project"`
	Summary struct {
		StatusCounts struct {
			Open       int64 `json:"open"`
			Ready      int64 `json:"ready"`
			InProgress int64 `json:"in_progress"`
			Blocked    int64 `json:"blocked"`
			Review     int64 `json:"review"`
			Done       int64 `json:"done"`
			Cancelled  int64 `json:"cancelled"`
		} `json:"status_counts"`
		ActiveAttemptCount int64 `json:"active_attempt_count"`
		BlockedCount       int64 `json:"blocked_count"`
		OpenReviewCount    int64 `json:"open_review_count"`
	} `json:"summary"`
	ReadyIssues []struct {
		ID          string `json:"id"`
		DisplayID   string `json:"display_id"`
		Type        string `json:"type"`
		Title       string `json:"title"`
		Status      string `json:"status"`
		Priority    string `json:"priority"`
		IsClaimable bool   `json:"is_claimable"`
	} `json:"ready_issues"`
	BlockedIssues []struct {
		ID                     string  `json:"id"`
		DisplayID              string  `json:"display_id"`
		Type                   string  `json:"type"`
		Title                  string  `json:"title"`
		Status                 string  `json:"status"`
		UnresolvedBlockerCount int64   `json:"unresolved_blocker_count"`
		BlockedReason          *string `json:"blocked_reason"`
	} `json:"blocked_issues"`
	Warnings         []string `json:"warnings"`
	Truncated        bool     `json:"truncated"`
	TruncationReason *string  `json:"truncation_reason"`
}

func readLatestEventID(t *testing.T, db *sqlite.DB) int64 {
	t.Helper()
	var latest int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latest)
	}); err != nil {
		t.Fatalf("read latest event id: %v", err)
	}
	return latest
}

func assertNoCredentialLeak(t *testing.T, raw string, secrets ...string) {
	t.Helper()
	for _, key := range []string{"lease_token", "token_hash", "agent_session_handle"} {
		if strings.Contains(raw, key) {
			t.Fatalf("resource payload leaked forbidden field %q: %s", key, raw)
		}
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(raw, secret) {
			t.Fatalf("resource payload leaked secret value %q", secret)
		}
	}
}

func TestResourcesListUntouchedAndTemplatesRegistered(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "resources-list.db"))
	options := composeServices(t, db, source)
	client, cleanup := newClient(t, options)
	defer cleanup()

	ctx := context.Background()
	initResult := client.InitializeResult()
	if initResult == nil || initResult.Capabilities == nil || initResult.Capabilities.Resources == nil {
		t.Fatalf("InitializeResult().Capabilities.Resources = %#v, want non-nil", initResult)
	}

	listedResources, err := client.ListResources(ctx, &sdkmcp.ListResourcesParams{})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(listedResources.Resources) != 3 {
		t.Fatalf("ListResources() returned %d resources, want 3 static guides only", len(listedResources.Resources))
	}

	listedTemplates, err := client.ListResourceTemplates(ctx, &sdkmcp.ListResourceTemplatesParams{})
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}
	if len(listedTemplates.ResourceTemplates) != 3 {
		t.Fatalf("ListResourceTemplates() returned %d templates, want 3", len(listedTemplates.ResourceTemplates))
	}

	byURI := make(map[string]*sdkmcp.ResourceTemplate, len(listedTemplates.ResourceTemplates))
	for _, tmpl := range listedTemplates.ResourceTemplates {
		byURI[tmpl.URITemplate] = tmpl
	}
	wantTemplates := []struct {
		uri   string
		name  string
		title string
	}{
		{"rhizome://projects/{project_ref}/issues/{issue_id}/brief", "issue-brief", "Issue Brief Context Pack"},
		{"rhizome://issues/{issue_id}/brief", "default-project-issue-brief", "Default Project Issue Brief Context Pack"},
		{"rhizome://projects/{project_ref}/brief", "project-brief", "Project Brief Context Pack"},
	}
	for _, want := range wantTemplates {
		got, ok := byURI[want.uri]
		if !ok {
			t.Fatalf("missing template URI %q in %#v", want.uri, listedTemplates.ResourceTemplates)
		}
		if got.Name != want.name || got.Title != want.title || got.MIMEType != "application/json" {
			t.Fatalf("template %q = %+v, want Name=%q Title=%q MIMEType=application/json", want.uri, got, want.name, want.title)
		}
	}
}

func TestIssueBriefAndProjectBriefResourcesExplicitDefaultAndReadOnly(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "resources-briefs.db"))
	options := composeServices(t, db, source)
	services := servicesFromOptions(t, options)
	project, err := services.ProjectService.GetProject(context.Background())
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	projectRef := project.ID

	client, cleanup := newClient(t, options)
	defer cleanup()

	var sessionOut struct {
		Handle string `json:"agent_session_handle"`
	}
	sessionRes := call(t, client, "create_agent_session", map[string]any{"client_name": "resource-test-client"})
	decodeStructured(t, sessionRes, &sessionOut)
	if sessionOut.Handle == "" {
		t.Fatal("expected non-empty agent_session_handle")
	}

	// Enforce an acceptance_criteria policy so unmet gate requirements show up cleanly.
	policyRes := call(t, client, "manage_workflow_policy", map[string]any{
		"action": "create",
		"requirements": []map[string]any{
			{"key": "acceptance", "kind": "issue_field_nonblank", "field": "acceptance_criteria"},
		},
	})
	if policyRes.IsError {
		t.Fatalf("manage_workflow_policy failed: %#v", policyRes)
	}

	epicRes := call(t, client, "create_issue", map[string]any{
		"type":  "epic",
		"title": "Context Pack Epic",
	})
	var epicOut struct {
		ID string `json:"id"`
	}
	decodeStructured(t, epicRes, &epicOut)

	blockerRes := call(t, client, "create_issue", map[string]any{
		"type":                "task",
		"title":               "Blocker prerequisite",
		"status":              "ready",
		"priority":            "high",
		"acceptance_criteria": "Blocker passes checks",
	})
	var blockerOut struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeStructured(t, blockerRes, &blockerOut)

	targetRes := call(t, client, "create_issue", map[string]any{
		"type":            "task",
		"title":           "Target brief issue",
		"description":     "Full target description for brief",
		"status":          "ready",
		"priority":        "critical",
		"parent_issue_id": epicOut.ID,
	})
	var targetOut struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeStructured(t, targetRes, &targetOut)

	// Add ready unblocked task, stored-blocked task, and an active attempt + review request for project brief.
	readyRes := call(t, client, "create_issue", map[string]any{
		"type":                "task",
		"title":               "Claimable ready work",
		"status":              "ready",
		"priority":            "high",
		"acceptance_criteria": "Ready criteria",
	})
	var readyOut struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeStructured(t, readyRes, &readyOut)

	storedBlockedRes := call(t, client, "create_issue", map[string]any{
		"type":           "bug",
		"title":          "Manually blocked bug",
		"status":         "blocked",
		"blocked_reason": "Waiting on upstream decision",
	})
	if storedBlockedRes.IsError {
		t.Fatalf("create blocked issue failed: %#v", storedBlockedRes)
	}

	claimedIssueRes := call(t, client, "create_issue", map[string]any{
		"type":                "task",
		"title":               "In-progress claimed work",
		"status":              "ready",
		"acceptance_criteria": "Must succeed",
	})
	var claimedIssueOut struct {
		ID string `json:"id"`
	}
	decodeStructured(t, claimedIssueRes, &claimedIssueOut)

	claimRes := call(t, client, "claim_issue", map[string]any{
		"issue_id":             claimedIssueOut.ID,
		"agent_session_handle": sessionOut.Handle,
	})
	var claimOut struct {
		LeaseToken string `json:"lease_token"`
	}
	decodeStructured(t, claimRes, &claimOut)
	if claimOut.LeaseToken == "" {
		t.Fatal("expected non-empty lease_token")
	}

	relRes := call(t, client, "manage_issue_relation", map[string]any{
		"action":          "add",
		"source_issue_id": blockerOut.ID,
		"target_issue_id": targetOut.ID,
		"relation_type":   "blocks",
	})
	if relRes.IsError {
		t.Fatalf("manage_issue_relation failed: %#v", relRes)
	}

	decRes := call(t, client, "record_decision", map[string]any{
		"issue_id": targetOut.ID,
		"title":    "Keep context packs bounded",
		"summary":  "Use strict 16 KiB limit",
		"content":  "Detailed rationale omitted from brief summary",
		"status":   "active",
	})
	if decRes.IsError {
		t.Fatalf("record_decision failed: %#v", decRes)
	}

	targetVer, targetEvt := currentReviewTargetPosition(t, db, targetOut.ID)
	revRes := call(t, client, "create_review_request", map[string]any{
		"issue_id":             targetOut.ID,
		"target_issue_version": targetVer,
		"target_event_id":      targetEvt,
		"purposes":             []string{"code_review"},
	})
	if revRes.IsError {
		t.Fatalf("create_review_request failed: %#v", revRes)
	}

	// Capture durable DB state before reading resources; advance fake clock so any accidental touch changes timestamps.
	beforeEventID := readLatestEventID(t, db)
	beforeSession := readAgentSession(t, db)
	source.Advance(5 * time.Minute)

	ctx := context.Background()
	for _, uri := range []string{
		"rhizome://projects/" + projectRef + "/issues/" + targetOut.DisplayID + "/brief",
		"rhizome://projects/" + projectRef + "/issues/" + targetOut.ID + "/brief",
		"rhizome://projects/" + projectRef + "/issues/" + strings.ReplaceAll(targetOut.DisplayID, "-", "%2D") + "/brief",
		"rhizome://issues/" + targetOut.DisplayID + "/brief",
	} {
		readRes, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("ReadResource(%q) error = %v", uri, err)
		}
		if len(readRes.Contents) != 1 || readRes.Contents[0].MIMEType != "application/json" {
			t.Fatalf("ReadResource(%q) contents = %#v, want 1 application/json entry", uri, readRes.Contents)
		}
		raw := readRes.Contents[0].Text
		assertNoCredentialLeak(t, raw, sessionOut.Handle, claimOut.LeaseToken)
		if len(raw) > mcpadapter.MaxContextPackBytes {
			t.Fatalf("ReadResource(%q) exceeded 16 KiB budget: %d", uri, len(raw))
		}

		var doc issueBriefDoc
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatalf("unmarshal %q: %v\nraw=%s", uri, err, raw)
		}
		if doc.ProjectRef != projectRef {
			t.Fatalf("doc.ProjectRef = %q, want %q", doc.ProjectRef, projectRef)
		}
		if doc.Issue.ID != targetOut.ID || doc.Issue.DisplayID != targetOut.DisplayID {
			t.Fatalf("doc.Issue = %+v, want %s/%s", doc.Issue, targetOut.ID, targetOut.DisplayID)
		}
		if doc.Issue.Type != "task" || doc.Issue.Status != "ready" || doc.Issue.Priority != "critical" {
			t.Fatalf("doc.Issue metadata unexpected: %+v", doc.Issue)
		}
		if doc.Issue.ParentIssueID == nil || *doc.Issue.ParentIssueID != epicOut.ID {
			t.Fatalf("doc.Issue.ParentIssueID = %v, want %q", doc.Issue.ParentIssueID, epicOut.ID)
		}
		if doc.Gates.Passed || doc.Gates.RequirementCount != 1 || doc.Gates.SatisfiedCount != 0 || len(doc.Gates.Unmet) != 1 {
			t.Fatalf("doc.Gates = %+v, want 1 unmet requirement", doc.Gates)
		}
		if len(doc.Blockers) != 1 || doc.Blockers[0].ID != blockerOut.ID {
			t.Fatalf("doc.Blockers = %+v, want blocker %q", doc.Blockers, blockerOut.ID)
		}
		if len(doc.Decisions) != 1 || doc.Decisions[0].Title != "Keep context packs bounded" {
			t.Fatalf("doc.Decisions = %+v, want 1 decision", doc.Decisions)
		}
		if len(doc.Reviews) != 1 || doc.Reviews[0].Status != "open" {
			t.Fatalf("doc.Reviews = %+v, want 1 open review", doc.Reviews)
		}
		if doc.Truncated || doc.TruncationReason != nil {
			t.Fatalf("unexpected truncation on normal brief: truncated=%v reason=%v", doc.Truncated, doc.TruncationReason)
		}
	}

	projRead, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{
		URI: "rhizome://projects/" + projectRef + "/brief",
	})
	if err != nil {
		t.Fatalf("ReadResource(project-brief) error = %v", err)
	}
	if len(projRead.Contents) != 1 || projRead.Contents[0].MIMEType != "application/json" {
		t.Fatalf("project brief contents = %#v", projRead.Contents)
	}
	assertNoCredentialLeak(t, projRead.Contents[0].Text, sessionOut.Handle, claimOut.LeaseToken)
	var projDoc projectBriefDoc
	if err := json.Unmarshal([]byte(projRead.Contents[0].Text), &projDoc); err != nil {
		t.Fatalf("unmarshal project brief: %v", err)
	}
	if projDoc.Project.ProjectRef != projectRef || projDoc.Project.ID != project.ID {
		t.Fatalf("projDoc.Project = %+v, want projectRef/ID=%q", projDoc.Project, projectRef)
	}
	if projDoc.Summary.ActiveAttemptCount != 1 || projDoc.Summary.BlockedCount != 2 || projDoc.Summary.OpenReviewCount != 1 {
		t.Fatalf("projDoc.Summary = %+v, want active=1 blocked=2 openReviews=1", projDoc.Summary)
	}
	if projDoc.Summary.StatusCounts.Open != 1 || projDoc.Summary.StatusCounts.InProgress != 1 ||
		projDoc.Summary.StatusCounts.Ready != 3 || projDoc.Summary.StatusCounts.Blocked != 1 {
		t.Fatalf("projDoc.Summary.StatusCounts = %+v, want open=1 in_progress=1 ready=3 blocked=1", projDoc.Summary.StatusCounts)
	}
	if len(projDoc.ReadyIssues) != 2 || projDoc.ReadyIssues[0].ID != readyOut.ID && projDoc.ReadyIssues[1].ID != readyOut.ID {
		t.Fatalf("projDoc.ReadyIssues = %+v, want 2 unblocked ready issues including %q", projDoc.ReadyIssues, readyOut.ID)
	}
	if len(projDoc.BlockedIssues) != 2 {
		t.Fatalf("projDoc.BlockedIssues len = %d, want 2 (%+v)", len(projDoc.BlockedIssues), projDoc.BlockedIssues)
	}

	afterEventID := readLatestEventID(t, db)
	afterSession := readAgentSession(t, db)
	if afterEventID != beforeEventID {
		t.Fatalf("resource reads wrote events: before=%d after=%d", beforeEventID, afterEventID)
	}
	if !afterSession.LastSeenAt.Equal(beforeSession.LastSeenAt) {
		t.Fatalf("resource reads touched session last_seen_at: before=%s after=%s", beforeSession.LastSeenAt, afterSession.LastSeenAt)
	}
}

func TestContextPackResourcesValidationRoutingAndErrors(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "resources-errors.db"))
	options := composeServices(t, db, source)
	services := servicesFromOptions(t, options)
	project, err := services.ProjectService.GetProject(context.Background())
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	projectRef := project.ID

	spy := &spyProjectRouter{inner: options.ProjectRouter}
	options.ProjectRouter = spy
	client, cleanup := newClient(t, options)
	defer cleanup()

	ctx := context.Background()
	archivedRes := call(t, client, "create_issue", map[string]any{
		"type":  "task",
		"title": "To be archived",
	})
	var archivedIssue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Version   int64  `json:"version"`
	}
	decodeStructured(t, archivedRes, &archivedIssue)
	if res := call(t, client, "archive_issue", map[string]any{
		"issue_id":         archivedIssue.ID,
		"expected_version": archivedIssue.Version,
	}); res.IsError {
		t.Fatalf("archive_issue failed: %#v", res)
	}

	// Reset spy counts after fixture mutations so we measure only resource calls.
	spy.acquireCount = 0
	spy.successfulLeaseCount = 0
	spy.releaseCount = 0

	// 1. Invalid project_ref must be rejected with -32602 BEFORE router.Acquire is called.
	for _, badURI := range []string{
		"rhizome://projects/not-a-ulid/issues/" + archivedIssue.DisplayID + "/brief",
		"rhizome://projects/" + strings.ToLower(projectRef) + "/brief",
		"rhizome://projects/01ARZ3NDEKTSV4RRFFQ69G5FA/brief",
		"rhizome://projects/" + projectRef + "/issues/%ZZ/brief",
		"rhizome://projects/" + projectRef + "/issues/bad%2Fissue/brief",
	} {
		_, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: badURI})
		if err == nil {
			t.Fatalf("ReadResource(%q) unexpectedly succeeded", badURI)
		}
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("ReadResource(%q) error = %#v, want *jsonrpc.Error with code %d", badURI, err, jsonrpc.CodeInvalidParams)
		}
	}
	if spy.acquireCount != 0 {
		t.Fatalf("router.Acquire called %d times on invalid URIs, want 0", spy.acquireCount)
	}

	// 2. Archived issue must fail with structured -32602 mentioning archived and issue_id.
	for _, archivedURI := range []string{
		"rhizome://projects/" + projectRef + "/issues/" + archivedIssue.ID + "/brief",
		"rhizome://issues/" + archivedIssue.DisplayID + "/brief",
	} {
		_, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: archivedURI})
		if err == nil {
			t.Fatalf("ReadResource(%q) returned archived issue brief instead of error", archivedURI)
		}
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("archived ReadResource(%q) error = %#v, want code %d", archivedURI, err, jsonrpc.CodeInvalidParams)
		}
		lowerMsg := strings.ToLower(rpcErr.Message)
		if !strings.Contains(lowerMsg, "archived") || !strings.Contains(lowerMsg, "issue_id") {
			t.Fatalf("archived error message %q must mention archived and issue_id", rpcErr.Message)
		}
		var data struct {
			Code    string `json:"code"`
			IssueID string `json:"issue_id"`
		}
		if jsonErr := json.Unmarshal(rpcErr.Data, &data); jsonErr != nil || data.Code != domain.CodeIssueArchived || data.IssueID != archivedIssue.ID {
			t.Fatalf("archived error data = %s, want code=%s issue_id=%s", string(rpcErr.Data), domain.CodeIssueArchived, archivedIssue.ID)
		}
	}

	// 3. Unknown issue returns ResourceNotFoundError.
	missingIssueURI := "rhizome://projects/" + projectRef + "/issues/ISSUE-99999/brief"
	if _, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: missingIssueURI}); err == nil {
		t.Fatalf("ReadResource(%q) unexpectedly succeeded", missingIssueURI)
	} else {
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != sdkmcp.CodeResourceNotFound {
			t.Fatalf("unknown issue error = %#v, want code %d", err, sdkmcp.CodeResourceNotFound)
		}
	}

	// 4. Unknown project returns ResourceNotFoundError.
	unknownProjectRef := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	if unknownProjectRef == projectRef {
		unknownProjectRef = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	}
	unknownProjectURI := "rhizome://projects/" + unknownProjectRef + "/brief"
	if _, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: unknownProjectURI}); err == nil {
		t.Fatalf("ReadResource(%q) unexpectedly succeeded", unknownProjectURI)
	} else {
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != sdkmcp.CodeResourceNotFound {
			t.Fatalf("unknown project error = %#v, want code %d", err, sdkmcp.CodeResourceNotFound)
		}
	}

	// 5. Missing default project returns structured error on default fallback URI.
	spy.acquireErrOverride = projectrouting.NewProjectRequiredError()
	if _, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: "rhizome://issues/ISSUE-1/brief"}); err == nil {
		t.Fatal("default fallback without default project unexpectedly succeeded")
	} else {
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != sdkmcp.CodeResourceNotFound {
			t.Fatalf("missing default project error = %#v, want code %d", err, sdkmcp.CodeResourceNotFound)
		}
	}
	spy.acquireErrOverride = nil

	if spy.successfulLeaseCount != spy.releaseCount || spy.successfulLeaseCount == 0 {
		t.Fatalf("lease leak: successfulLeaseCount=%d releaseCount=%d", spy.successfulLeaseCount, spy.releaseCount)
	}
}

func TestContextPackResources16KiBBudgetTruncation(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "resources-budget.db"))
	options := composeServices(t, db, source)
	services := servicesFromOptions(t, options)
	project, err := services.ProjectService.GetProject(context.Background())
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	projectRef := project.ID

	client, cleanup := newClient(t, options)
	defer cleanup()

	// Huge issue brief: > 30 KiB description + > 20 KiB acceptance criteria + escaped/UTF-8 text + blockers/decisions/reviews.
	descChunk := strings.Repeat(`multi-byte UTF-8 café 🚀 <tag> "quote" `, 800)
	acChunk := strings.Repeat(`acceptance ✅ check & verify "clause" `, 600)
	hugeRes := call(t, client, "create_issue", map[string]any{
		"type":                "task",
		"title":               "Oversized issue brief",
		"description":         descChunk,
		"acceptance_criteria": acChunk,
		"status":              "ready",
	})
	var hugeIssue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeStructured(t, hugeRes, &hugeIssue)

	for i := 0; i < 6; i++ {
		blkRes := call(t, client, "create_issue", map[string]any{
			"type":   "task",
			"title":  "Blocker task with long summary title " + strings.Repeat("x", 120),
			"status": "ready",
		})
		var blk struct {
			ID string `json:"id"`
		}
		decodeStructured(t, blkRes, &blk)
		if res := call(t, client, "manage_issue_relation", map[string]any{
			"action":          "add",
			"source_issue_id": blk.ID,
			"target_issue_id": hugeIssue.ID,
			"relation_type":   "blocks",
		}); res.IsError {
			t.Fatalf("manage_issue_relation failed: %#v", res)
		}
		if res := call(t, client, "record_decision", map[string]any{
			"issue_id": hugeIssue.ID,
			"title":    "Decision title",
			"summary":  "Long decision summary " + strings.Repeat("y", 180),
			"content":  "Body",
		}); res.IsError {
			t.Fatalf("record_decision failed: %#v", res)
		}
	}

	targetVer, targetEvt := currentReviewTargetPosition(t, db, hugeIssue.ID)
	if res := call(t, client, "create_review_request", map[string]any{
		"issue_id":             hugeIssue.ID,
		"target_issue_version": targetVer,
		"target_event_id":      targetEvt,
		"purposes":             []string{"code_review"},
	}); res.IsError {
		t.Fatalf("create_review_request failed: %#v", res)
	}

	ctx := context.Background()
	issueRead, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{
		URI: "rhizome://issues/" + hugeIssue.DisplayID + "/brief",
	})
	if err != nil {
		t.Fatalf("ReadResource(oversized issue) error = %v", err)
	}
	rawIssue := issueRead.Contents[0].Text
	if len(rawIssue) > mcpadapter.MaxContextPackBytes {
		t.Fatalf("issue brief length = %d bytes, exceeds 16 KiB limit (%d)", len(rawIssue), mcpadapter.MaxContextPackBytes)
	}
	if !utf8.ValidString(rawIssue) {
		t.Fatal("truncated issue brief is not valid UTF-8")
	}
	var issueDoc issueBriefDoc
	if err := json.Unmarshal([]byte(rawIssue), &issueDoc); err != nil {
		t.Fatalf("truncated issue brief invalid JSON: %v", err)
	}
	if !issueDoc.Truncated || issueDoc.TruncationReason == nil || *issueDoc.TruncationReason != "byte_budget" {
		t.Fatalf("issue brief truncation = (%v, %v), want (true, byte_budget)", issueDoc.Truncated, issueDoc.TruncationReason)
	}
	if issueDoc.Issue.Description == nil || *issueDoc.Issue.Description == "" ||
		issueDoc.Issue.AcceptanceCriteria == nil || *issueDoc.Issue.AcceptanceCriteria == "" {
		t.Fatalf("expected both oversized description and acceptance_criteria to preserve trimmed prefixes")
	}

	// Huge project brief: 100 ready issues + 20 blocked issues exceeding 16 KiB before trimming.
	longTitle := strings.Repeat("Long issue title segment café 🚀 ", 5)
	for i := 0; i < 85; i++ {
		res := call(t, client, "create_issue", map[string]any{
			"type":   "task",
			"title":  longTitle,
			"status": "ready",
		})
		if res.IsError {
			t.Fatalf("create_issue ready #%d failed: %#v", i, res)
		}
	}
	for i := 0; i < 20; i++ {
		res := call(t, client, "create_issue", map[string]any{
			"type":           "task",
			"title":          longTitle,
			"status":         "blocked",
			"blocked_reason": strings.Repeat("blocked reason detail ", 5),
		})
		if res.IsError {
			t.Fatalf("create_issue blocked #%d failed: %#v", i, res)
		}
	}

	projRead, err := client.ReadResource(ctx, &sdkmcp.ReadResourceParams{
		URI: "rhizome://projects/" + projectRef + "/brief",
	})
	if err != nil {
		t.Fatalf("ReadResource(oversized project) error = %v", err)
	}
	rawProj := projRead.Contents[0].Text
	if len(rawProj) > mcpadapter.MaxContextPackBytes {
		t.Fatalf("project brief length = %d bytes, exceeds 16 KiB limit (%d)", len(rawProj), mcpadapter.MaxContextPackBytes)
	}
	if !utf8.ValidString(rawProj) {
		t.Fatal("truncated project brief is not valid UTF-8")
	}
	var projDoc projectBriefDoc
	if err := json.Unmarshal([]byte(rawProj), &projDoc); err != nil {
		t.Fatalf("truncated project brief invalid JSON: %v", err)
	}
	if !projDoc.Truncated || projDoc.TruncationReason == nil || *projDoc.TruncationReason != "byte_budget" {
		t.Fatalf("project brief truncation = (%v, %v), want (true, byte_budget)", projDoc.Truncated, projDoc.TruncationReason)
	}
	if len(projDoc.ReadyIssues) == 0 || len(projDoc.BlockedIssues) == 0 {
		t.Fatalf("expected balanced slices to keep both ready (%d) and blocked (%d) issues", len(projDoc.ReadyIssues), len(projDoc.BlockedIssues))
	}
}

func TestContextPackResourcesOverStreamableHTTP(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "resources-http.db"))
	options := composeServices(t, db, source)
	services := servicesFromOptions(t, options)
	project, err := services.ProjectService.GetProject(context.Background())
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	projectRef := project.ID

	server, err := mcpadapter.NewServer(options)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server.SDKServer()
	}, &sdkmcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	ctx := context.Background()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "http-resource-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatalf("connect StreamableHTTP client: %v", err)
	}
	defer func() { _ = session.Close() }()

	if init := session.InitializeResult(); init == nil || init.Capabilities == nil || init.Capabilities.Resources == nil {
		t.Fatalf("HTTP InitializeResult.Capabilities.Resources = %#v, want non-nil", init)
	}

	templates, err := session.ListResourceTemplates(ctx, &sdkmcp.ListResourceTemplatesParams{})
	if err != nil {
		t.Fatalf("HTTP ListResourceTemplates() error = %v", err)
	}
	if len(templates.ResourceTemplates) != 3 {
		t.Fatalf("HTTP ListResourceTemplates() count = %d, want 3", len(templates.ResourceTemplates))
	}

	created := call(t, session, "create_issue", map[string]any{
		"type":   "task",
		"title":  "HTTP context pack issue",
		"status": "ready",
	})
	var createdOut struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeStructured(t, created, &createdOut)

	issueRead, err := session.ReadResource(ctx, &sdkmcp.ReadResourceParams{
		URI: "rhizome://projects/" + projectRef + "/issues/" + createdOut.DisplayID + "/brief",
	})
	if err != nil {
		t.Fatalf("HTTP ReadResource(issue-brief) error = %v", err)
	}
	var issueDoc issueBriefDoc
	if err := json.Unmarshal([]byte(issueRead.Contents[0].Text), &issueDoc); err != nil {
		t.Fatalf("HTTP issue brief unmarshal: %v", err)
	}
	if issueDoc.Issue.ID != createdOut.ID || issueDoc.ProjectRef != projectRef {
		t.Fatalf("HTTP issueDoc = %+v", issueDoc)
	}

	projRead, err := session.ReadResource(ctx, &sdkmcp.ReadResourceParams{
		URI: "rhizome://projects/" + projectRef + "/brief",
	})
	if err != nil {
		t.Fatalf("HTTP ReadResource(project-brief) error = %v", err)
	}
	var projDoc projectBriefDoc
	if err := json.Unmarshal([]byte(projRead.Contents[0].Text), &projDoc); err != nil {
		t.Fatalf("HTTP project brief unmarshal: %v", err)
	}
	if projDoc.Project.ProjectRef != projectRef || projDoc.Summary.StatusCounts.Ready != 1 {
		t.Fatalf("HTTP projDoc = %+v", projDoc)
	}
}
