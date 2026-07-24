//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestIntegrationStdioToolProfileFiltering asserts that --profile read-only
// filters the advertised catalog over the real stdio transport, that a
// mutating tool is genuinely uncallable (not just unlisted) once excluded,
// and that get_project reports the active profile.
func TestIntegrationStdioToolProfileFiltering(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connectWithServeArgs(t, "--profile", "read-only")

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if hasTool(tools.Tools, "create_issue") {
		t.Fatal("read-only profile unexpectedly advertised create_issue")
	}
	if !hasTool(tools.Tools, "get_project") || !hasTool(tools.Tools, "get_issue") {
		t.Fatalf("read-only profile is missing expected read-only tools: %v", toolNames(tools.Tools))
	}

	// create_issue is not registered under the read-only profile at all, so
	// calling it fails as an unknown tool (a protocol-level error), not a
	// domain-level result.IsError — unlike a normal validation rejection,
	// this call never reaches any handler.
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "create_issue",
		Arguments: map[string]any{"type": "task", "title": "should be rejected under read-only profile"},
	}); err == nil {
		t.Fatal("create_issue unexpectedly callable under the read-only profile")
	}

	project := callIntegrationTool(t, session, "get_project", map[string]any{})
	var output struct {
		ToolProfile string `json:"tool_profile"`
	}
	decodeIntegrationResult(t, project, &output)
	if output.ToolProfile != "read-only" {
		t.Fatalf("get_project tool_profile = %q, want %q", output.ToolProfile, "read-only")
	}
}

// TestIntegrationHTTPToolProfileFiltering mirrors the stdio test above over
// the HTTP transport, and additionally asserts both transports agree on
// exactly the same advertised tool set for the same profile.
func TestIntegrationHTTPToolProfileFiltering(t *testing.T) {
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0", "--profile", "read-only")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	httpProject, sessionID, err := communicateThroughHTTP(t, endpoint, "profile-http-client")
	if err != nil {
		t.Fatalf("HTTP workflow failed: %v\nstderr:\n%s", err, server.output.String())
	}
	if toolProfile, _ := httpProject["tool_profile"].(string); toolProfile != "read-only" {
		t.Fatalf("get_project tool_profile over HTTP = %v, want %q", httpProject["tool_profile"], "read-only")
	}

	httpClient := &http.Client{Timeout: integrationTimeout}
	listResult, err := postJSONRPC(httpClient, endpoint, sessionID, 4, "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("HTTP tools/list failed: %v", err)
	}
	var listResponse struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listResult.result, &listResponse); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}
	httpNames := make([]string, len(listResponse.Tools))
	for index, tool := range listResponse.Tools {
		httpNames[index] = tool.Name
	}
	if containsString(httpNames, "create_issue") {
		t.Fatal("read-only profile unexpectedly advertised create_issue over HTTP")
	}

	if _, err := postJSONRPC(httpClient, endpoint, sessionID, 5, "tools/call", map[string]any{
		"name":      "create_issue",
		"arguments": map[string]any{"type": "task", "title": "should be rejected under read-only profile"},
	}); err == nil {
		t.Fatal("create_issue unexpectedly succeeded under the HTTP read-only profile")
	}

	stdioSession := env.connectWithServeArgs(t, "--profile", "read-only")
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	stdioTools, err := stdioSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools over stdio: %v", err)
	}
	stdioNames := toolNames(stdioTools.Tools)
	sort.Strings(stdioNames)
	sort.Strings(httpNames)
	if strings.Join(stdioNames, ",") != strings.Join(httpNames, ",") {
		t.Fatalf("stdio and HTTP disagree on the read-only tool set:\nstdio = %v\nhttp  = %v", stdioNames, httpNames)
	}
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Name
	}
	return names
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
