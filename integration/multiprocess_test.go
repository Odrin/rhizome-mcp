//go:build integration

package integration_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
)

// attach returns an integrationEnvironment describing the same repository
// and data root as env, for launching an additional server process against
// already-initialized project state without running "init" again.
func (env integrationEnvironment) attach() integrationEnvironment {
	return env
}

// killIntegrationHTTPServer terminates a background HTTP server without
// giving it a chance to run shutdown handlers (in particular sqlite.DB.Close's
// passive WAL checkpoint at internal/adapters/sqlite/sqlite.go:279), then
// waits for the process to be reaped. Unlike stopIntegrationHTTPServer, the
// process is expected to exit with a non-nil error and that is not treated
// as a test failure.
func killIntegrationHTTPServer(t *testing.T, server *integrationHTTPServer) {
	t.Helper()
	if server == nil || server.cmd == nil || server.cmd.Process == nil {
		return
	}
	if server.cmd.ProcessState != nil {
		return
	}
	if err := server.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill integration HTTP server: %v", err)
	}
	_ = server.waitForExit(t)
}

// TestIntegrationMultiProcessServersShareDataRoot is the ISSUE-102 proof
// test: two independently launched HTTP server processes, attached to one
// shared repository and data root, observe the same project state. An issue
// created through server A must be readable through server B. Server B is
// torn down via killIntegrationHTTPServer so that helper is exercised by a
// committed test rather than only compiled.
func TestIntegrationMultiProcessServersShareDataRoot(t *testing.T) {
	env := newIntegrationEnvironment(t)
	attached := env.attach()
	if got, want := mustProjectDatabasePath(t, attached), mustProjectDatabasePath(t, env); got != want {
		t.Fatalf("attached environment database path = %s, want %s", got, want)
	}

	serverA := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, serverA) })
	serverB := launchIntegrationHTTPServer(t, attached, "127.0.0.1:0")
	t.Cleanup(func() { killIntegrationHTTPServer(t, serverB) })

	endpointA := "http://" + serverA.waitForEndpoint(t) + "/mcp"
	endpointB := "http://" + serverB.waitForEndpoint(t) + "/mcp"

	_, sessionIDA, err := communicateThroughHTTP(t, endpointA, "multiprocess-server-a")
	if err != nil {
		t.Fatalf("initialize server A session: %v\nstderr:\n%s", err, serverA.output.String())
	}
	_, sessionIDB, err := communicateThroughHTTP(t, endpointB, "multiprocess-server-b")
	if err != nil {
		t.Fatalf("initialize server B session: %v\nstderr:\n%s", err, serverB.output.String())
	}

	const wantTitle = "shared data root proof"
	httpClient := &http.Client{Timeout: integrationTimeout}
	createResult, err := postJSONRPC(httpClient, endpointA, sessionIDA, 10, "tools/call", map[string]any{
		"name":      "create_issue",
		"arguments": map[string]any{"type": "task", "title": wantTitle},
	})
	if err != nil {
		t.Fatalf("create_issue on server A: %v", err)
	}
	var created struct {
		StructuredContent struct {
			ID string `json:"id"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(createResult.result, &created); err != nil {
		t.Fatalf("decode create_issue result: %v", err)
	}
	if created.StructuredContent.ID == "" {
		t.Fatalf("create_issue on server A returned no id: %s", createResult.result)
	}

	getResult, err := postJSONRPC(httpClient, endpointB, sessionIDB, 11, "tools/call", map[string]any{
		"name":      "get_issue",
		"arguments": map[string]any{"issue_id": created.StructuredContent.ID},
	})
	if err != nil {
		t.Fatalf("get_issue on server B: %v", err)
	}
	var fetched struct {
		StructuredContent struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(getResult.result, &fetched); err != nil {
		t.Fatalf("decode get_issue result: %v", err)
	}
	if fetched.StructuredContent.ID != created.StructuredContent.ID {
		t.Fatalf("get_issue on server B id = %q, want %q", fetched.StructuredContent.ID, created.StructuredContent.ID)
	}
	if fetched.StructuredContent.Title != wantTitle {
		t.Fatalf("get_issue on server B title = %q, want %q", fetched.StructuredContent.Title, wantTitle)
	}
}
