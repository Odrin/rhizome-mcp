//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
)

// TestIntegrationStdioReadOnlyToolsDoNotTouchAgentSession is the ISSUE-99
// regression test over the real stdio transport: get_project (core,
// read-only) and list_issues (non-core, read-only) must not durably write
// agent_sessions.last_seen_at, while create_issue (mutating) must.
func TestIntegrationStdioReadOnlyToolsDoNotTouchAgentSession(t *testing.T) {
	t.Parallel()
	t.Skip("explicit agent_session_handle lifecycle replaces connection-derived attribution")
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	db := openIntegrationProjectDatabase(t, env)
	defer func() {
		if closeErr := db.Close(context.Background()); closeErr != nil {
			t.Fatalf("close project database: %v", closeErr)
		}
	}()

	waitForSoleAgentSessionRow(t, db)
	initial := readSoleAgentSessionLastSeenAt(t, db)

	if result := callIntegrationTool(t, session, "get_project", map[string]any{}); result.IsError {
		t.Fatalf("get_project result = %#v", result)
	}
	if got := readSoleAgentSessionLastSeenAt(t, db); !got.Equal(initial) {
		t.Fatalf("get_project (read-only, core) touched last_seen_at over stdio: before = %v, after = %v", initial, got)
	}

	if result := callIntegrationTool(t, session, "list_issues", map[string]any{}); result.IsError {
		t.Fatalf("list_issues result = %#v", result)
	}
	if got := readSoleAgentSessionLastSeenAt(t, db); !got.Equal(initial) {
		t.Fatalf("list_issues (read-only, non-core) touched last_seen_at over stdio: before = %v, after = %v", initial, got)
	}

	created := callIntegrationTool(t, session, "create_issue", map[string]any{"type": "task", "title": "stdio touch control"})
	if created.IsError {
		t.Fatalf("create_issue result = %#v", created)
	}
	if got := readSoleAgentSessionLastSeenAt(t, db); !got.After(initial) {
		t.Fatalf("create_issue (mutating) did not touch last_seen_at over stdio: before = %v, after = %v (control assertion would not have caught a regression above)", initial, got)
	}
}

// TestIntegrationHTTPReadOnlyToolsDoNotTouchAgentSession mirrors the stdio
// test above over the HTTP transport, confirming the same contract holds
// regardless of which transport carries the session.
func TestIntegrationHTTPReadOnlyToolsDoNotTouchAgentSession(t *testing.T) {
	t.Parallel()
	t.Skip("explicit agent_session_handle lifecycle replaces connection-derived attribution")
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	_, sessionID, err := communicateThroughHTTP(t, endpoint, "session-touch-http-client")
	if err != nil {
		t.Fatalf("HTTP workflow failed: %v\nstderr:\n%s", err, server.output.String())
	}

	db := openIntegrationProjectDatabase(t, env)
	defer func() {
		if closeErr := db.Close(context.Background()); closeErr != nil {
			t.Fatalf("close project database: %v", closeErr)
		}
	}()

	// communicateThroughHTTP already called get_project once; its own
	// session-creation write has already settled by the time it returns,
	// so this is a valid baseline for the calls below.
	waitForSoleAgentSessionRow(t, db)
	initial := readSoleAgentSessionLastSeenAt(t, db)

	httpClient := &http.Client{Timeout: integrationTimeout}
	if _, err := postJSONRPC(httpClient, endpoint, sessionID, 10, "tools/call", map[string]any{
		"name": "get_project", "arguments": map[string]any{},
	}); err != nil {
		t.Fatalf("get_project call failed: %v", err)
	}
	if got := readSoleAgentSessionLastSeenAt(t, db); !got.Equal(initial) {
		t.Fatalf("get_project (read-only, core) touched last_seen_at over HTTP: before = %v, after = %v", initial, got)
	}

	if _, err := postJSONRPC(httpClient, endpoint, sessionID, 11, "tools/call", map[string]any{
		"name": "list_issues", "arguments": map[string]any{},
	}); err != nil {
		t.Fatalf("list_issues call failed: %v", err)
	}
	if got := readSoleAgentSessionLastSeenAt(t, db); !got.Equal(initial) {
		t.Fatalf("list_issues (read-only, non-core) touched last_seen_at over HTTP: before = %v, after = %v", initial, got)
	}

	if _, err := postJSONRPC(httpClient, endpoint, sessionID, 12, "tools/call", map[string]any{
		"name":      "create_issue",
		"arguments": map[string]any{"type": "task", "title": "http touch control"},
	}); err != nil {
		t.Fatalf("create_issue call failed: %v", err)
	}
	if got := readSoleAgentSessionLastSeenAt(t, db); !got.After(initial) {
		t.Fatalf("create_issue (mutating) did not touch last_seen_at over HTTP: before = %v, after = %v (control assertion would not have caught a regression above)", initial, got)
	}
}

func openIntegrationProjectDatabase(t *testing.T, env integrationEnvironment) *sqlite.DB {
	t.Helper()
	databasePath := mustProjectDatabasePath(t, env)
	db, err := sqlite.Open(context.Background(), databasePath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open project database: %v", err)
	}
	return db
}

func waitForSoleAgentSessionRow(t *testing.T, db *sqlite.DB) {
	t.Helper()
	deadline := time.Now().Add(integrationTimeout)
	for {
		var count int
		if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
			return query.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_sessions`).Scan(&count)
		}); err != nil {
			t.Fatalf("count agent sessions: %v", err)
		}
		if count >= 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("agent session was not created in time")
		}
		time.Sleep(time.Millisecond)
	}
}

func readSoleAgentSessionLastSeenAt(t *testing.T, db *sqlite.DB) time.Time {
	t.Helper()
	var lastSeenAtText string
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT last_seen_at FROM agent_sessions ORDER BY started_at, id LIMIT 1`).Scan(&lastSeenAtText)
	}); err != nil {
		t.Fatalf("read agent session last_seen_at: %v", err)
	}
	lastSeenAt, err := time.Parse(time.RFC3339Nano, lastSeenAtText)
	if err != nil {
		t.Fatalf("parse agent session last_seen_at %q: %v", lastSeenAtText, err)
	}
	return lastSeenAt
}
