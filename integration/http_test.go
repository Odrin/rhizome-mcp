//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestIntegrationHTTPAdversarialRequestsAreRejected(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer listener.Close()

	handler := projectruntime.WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), listener.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := &http.Server{Handler: handler}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	defer func() {
		if closeErr := server.Close(); closeErr != nil && closeErr != http.ErrServerClosed {
			t.Errorf("close loopback listener: %v", closeErr)
		}
		if err := <-serveDone; err != nil && err != http.ErrServerClosed {
			t.Errorf("serve loopback listener: %v", err)
		}
	}()

	client := &http.Client{Timeout: integrationTimeout}

	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/mcp", nil)
	if err != nil {
		t.Fatalf("construct request: %v", err)
	}
	request.Host = "example.com:8080"
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send hostile host request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("host mismatch status = %d, want %d", response.StatusCode, http.StatusMisdirectedRequest)
	}

	request, err = http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/mcp", nil)
	if err != nil {
		t.Fatalf("construct request: %v", err)
	}
	request.Host = listener.Addr().String()
	request.Header.Set("Origin", "http://127.0.0.1:9999")
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("send hostile origin request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin mismatch status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestIntegrationHTTPProjectRoutingUsesProjectRefArguments(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	startupDir := filepath.Join(tempDir, "outside")
	dataRoot := filepath.Join(tempDir, "data")
	for _, dir := range []string{repoRoot, startupDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	env := integrationEnvironment{repository: repoRoot, dataRoot: dataRoot}
	runIntegrationCommand(t, env, "--data-root", dataRoot, "init")

	server := launchIntegrationHTTPServerInDir(t, env, startupDir, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"

	for _, tc := range []struct {
		name            string
		protocolVersion string
		params          map[string]any
	}{
		{
			name:            "modern",
			protocolVersion: "2026-07-28",
			params: map[string]any{
				"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
			},
		},
		{
			name:            "legacy",
			protocolVersion: "2025-11-25",
			params:          map[string]any{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body, err := postJSONRPCRequest(t, endpoint, tc.protocolVersion, "", tc.name+"-catalog", "tools/list", map[string]any{})
			if err != nil {
				t.Fatalf("tools/list failed: %v", err)
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				t.Fatalf("tools/list status = %d, body = %s", status, body)
			}
			var envelope jsonRPCEnvelope
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode tools/list response: %v", err)
			}
			if envelope.Error != nil {
				t.Fatalf("tools/list rpc error = %#v", envelope.Error)
			}
			var toolsResponse struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(envelope.Result, &toolsResponse); err != nil {
				t.Fatalf("decode tools/list result: %v", err)
			}
			if len(toolsResponse.Tools) != 43 {
				t.Fatalf("tools/list tool count = %d, want 43", len(toolsResponse.Tools))
			}

			status, _, body, err = postJSONRPCRequest(t, endpoint, tc.protocolVersion, "ignored-session", tc.name+"-open", "tools/call", map[string]any{
				"name":      "open_project",
				"arguments": map[string]any{"project_root": repoRoot},
				"_meta":     tc.params["_meta"],
			})
			if err != nil {
				t.Fatalf("open_project failed: %v", err)
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				t.Fatalf("open_project status = %d, body = %s", status, body)
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode open_project response: %v", err)
			}
			if envelope.Error != nil {
				t.Fatalf("open_project rpc error = %#v", envelope.Error)
			}
			var openProjectResult struct {
				StructuredContent struct {
					ProjectRef string `json:"project_ref"`
				} `json:"structuredContent"`
			}
			if err := json.Unmarshal(envelope.Result, &openProjectResult); err != nil {
				t.Fatalf("decode open_project result: %v", err)
			}
			if openProjectResult.StructuredContent.ProjectRef == "" {
				t.Fatal("open_project returned an empty project_ref")
			}

			status, _, body, err = postJSONRPCRequest(t, endpoint, tc.protocolVersion, "ignored-session", tc.name+"-missing-ref", "tools/call", map[string]any{
				"name":      "create_issue",
				"arguments": map[string]any{"type": "task", "title": "missing-ref"},
				"_meta":     tc.params["_meta"],
			})
			if err != nil {
				t.Fatalf("create_issue without project_ref failed: %v", err)
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				t.Fatalf("create_issue without project_ref status = %d, body = %s", status, body)
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode missing-ref response: %v", err)
			}
			if envelope.Error != nil {
				t.Fatalf("missing-ref rpc error = %#v", envelope.Error)
			}
			var missingRefResult struct {
				IsError           bool `json:"isError"`
				StructuredContent struct {
					Code string `json:"code"`
				} `json:"structuredContent"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(envelope.Result, &missingRefResult); err != nil {
				t.Fatalf("decode missing-ref result: %v", err)
			}
			if !missingRefResult.IsError {
				t.Fatalf("create_issue without project_ref unexpectedly succeeded: %s", body)
			}
			if missingRefResult.StructuredContent.Code != "PROJECT_REQUIRED" {
				t.Fatalf("create_issue without project_ref code = %q, want PROJECT_REQUIRED", missingRefResult.StructuredContent.Code)
			}

			status, _, body, err = postJSONRPCRequest(t, endpoint, tc.protocolVersion, "ignored-session", tc.name+"-explicit-ref", "tools/call", map[string]any{
				"name": "create_issue",
				"arguments": map[string]any{
					"project_ref": openProjectResult.StructuredContent.ProjectRef,
					"type":        "task",
					"title":       tc.name + "-routed",
				},
				"_meta": tc.params["_meta"],
			})
			if err != nil {
				t.Fatalf("create_issue with explicit project_ref failed: %v", err)
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				t.Fatalf("create_issue with explicit project_ref status = %d, body = %s", status, body)
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode explicit-ref response: %v", err)
			}
			if envelope.Error != nil {
				t.Fatalf("explicit-ref rpc error = %#v", envelope.Error)
			}
			var explicitRefResult struct {
				IsError bool `json:"isError"`
			}
			if err := json.Unmarshal(envelope.Result, &explicitRefResult); err != nil {
				t.Fatalf("decode explicit-ref result: %v", err)
			}
			if explicitRefResult.IsError {
				t.Fatalf("create_issue with explicit project_ref returned an error: %s", body)
			}

			status, _, body, err = postJSONRPCRequest(t, endpoint, tc.protocolVersion, "ignored-session", tc.name+"-list", "tools/call", map[string]any{
				"name": "list_issues",
				"arguments": map[string]any{
					"project_ref": openProjectResult.StructuredContent.ProjectRef,
					"limit":       10,
				},
				"_meta": tc.params["_meta"],
			})
			if err != nil {
				t.Fatalf("list_issues failed: %v", err)
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				t.Fatalf("list_issues status = %d, body = %s", status, body)
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode list_issues response: %v", err)
			}
			if envelope.Error != nil {
				t.Fatalf("list_issues rpc error = %#v", envelope.Error)
			}
			var listResult struct {
				StructuredContent struct {
					Items []struct {
						Title string `json:"title"`
					} `json:"items"`
				} `json:"structuredContent"`
			}
			if err := json.Unmarshal(envelope.Result, &listResult); err != nil {
				t.Fatalf("decode list_issues result: %v", err)
			}
			if len(listResult.StructuredContent.Items) == 0 {
				t.Fatal("list_issues returned no items after explicit project_ref routing")
			}
			foundRoutedIssue := false
			for _, item := range listResult.StructuredContent.Items {
				if item.Title == tc.name+"-routed" {
					foundRoutedIssue = true
					break
				}
			}
			if !foundRoutedIssue {
				t.Fatalf("list_issues titles = %#v, want %q", listResult.StructuredContent.Items, tc.name+"-routed")
			}
		})
	}
}

func TestIntegrationHTTPServeEphemeralPortWorkflow(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	if _, _, err := communicateThroughHTTP(t, endpoint, "http-client"); err != nil {
		t.Fatalf("HTTP workflow failed: %v\nstderr:\n%s", err, server.output.String())
	}
}

func TestIntegrationHTTPServeConcurrentClientsOnEphemeralPort(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	results := make(chan error, 3)
	for _, clientName := range []string{"concurrent-a", "concurrent-b", "concurrent-c"} {
		clientName := clientName
		go func() {
			_, _, err := communicateThroughHTTP(t, endpoint, clientName)
			results <- err
		}()
	}
	for range 3 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent HTTP workflow failed: %v\nstderr:\n%s", err, server.output.String())
		}
	}

	if err := assertDistinctHTTPAgentSessions(t, env.repository, env.dataRoot, 0); err != nil {
		t.Fatalf("assert no automatic HTTP agent sessions: %v", err)
	}
}

func TestIntegrationHTTPServeStopsOnInterrupt(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	if _, _, err := communicateThroughHTTP(t, endpoint, "shutdown-client"); err != nil {
		t.Fatalf("HTTP workflow failed before shutdown: %v\nstderr:\n%s", err, server.output.String())
	}

	stopIntegrationHTTPServer(t, server)

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(endpoint)
	if err == nil {
		response.Body.Close()
		t.Fatalf("expected HTTP endpoint to be closed after shutdown")
	}
}

func TestIntegrationHTTPServeRejectsHostnameAddress(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "localhost:0")
	if err := server.waitForExit(t); err == nil {
		t.Fatalf("expected hostname address to fail startup")
	}
	if stderr := server.output.String(); !strings.Contains(stderr, "invalid http address") {
		t.Fatalf("expected invalid address error in stderr, got %s", stderr)
	}
}

func TestIntegrationMCPConformanceMatrix(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"

	t.Run("modern stateless discovery and tools", func(t *testing.T) {
		status, headers, body, err := postJSONRPCRequest(t, endpoint, "2026-07-28", "", "discover", "server/discover", map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"}})
		if err != nil {
			t.Fatalf("server/discover failed: %v", err)
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			t.Fatalf("server/discover status = %d, body = %s", status, body)
		}
		if got := headers.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("modern server/discover returned session header = %q, want empty", got)
		}
		var envelope jsonRPCEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode server/discover response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("server/discover rpc error = %#v", envelope.Error)
		}
		var result map[string]any
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatalf("decode server/discover result: %v", err)
		}
		serverInfoFound := false
		if _, ok := result["serverInfo"]; ok {
			serverInfoFound = true
		} else if meta, ok := result["_meta"].(map[string]any); ok {
			_, serverInfoFound = meta["io.modelcontextprotocol/serverInfo"]
		}
		if !serverInfoFound {
			t.Fatalf("server/discover result missing serverInfo: %#v", result)
		}
		if _, ok := result["protocolVersion"]; !ok {
			if _, ok := result["supportedVersions"]; !ok {
				t.Fatalf("server/discover result missing protocolVersion/supportedVersions: %#v", result)
			}
		}
		if _, ok := result["capabilities"]; !ok {
			t.Fatalf("server/discover result missing capabilities: %#v", result)
		}

		status, headers, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "tools-list", "tools/list", map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"}})
		if err != nil {
			t.Fatalf("tools/list failed: %v", err)
		}
		if headers.Get("Mcp-Session-Id") != "" {
			t.Fatalf("modern tools/list returned session header = %q, want empty", headers.Get("Mcp-Session-Id"))
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode tools/list response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("tools/list rpc error = %#v", envelope.Error)
		}
		var toolsResponse struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(envelope.Result, &toolsResponse); err != nil {
			t.Fatalf("decode tools/list result: %v", err)
		}
		if len(toolsResponse.Tools) == 0 {
			t.Fatal("tools/list returned no tools")
		}
		foundGetProject := false
		for _, tool := range toolsResponse.Tools {
			if tool.Name == "get_project" {
				foundGetProject = true
				break
			}
		}
		if !foundGetProject {
			t.Fatalf("tools/list tools = %#v, want get_project present", toolsResponse.Tools)
		}

		status, headers, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "get-project", "tools/call", map[string]any{
			"name":      "get_project",
			"arguments": map[string]any{},
			"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("tools/call failed: %v", err)
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			t.Fatalf("tools/call status = %d, body = %s", status, body)
		}
		if headers.Get("Mcp-Session-Id") != "" {
			t.Fatalf("modern tools/call returned session header = %q, want empty", headers.Get("Mcp-Session-Id"))
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode tools/call response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("tools/call rpc error = %#v", envelope.Error)
		}
		var callResult map[string]any
		if err := json.Unmarshal(envelope.Result, &callResult); err != nil {
			t.Fatalf("decode tools/call result: %v", err)
		}
		if _, ok := callResult["structuredContent"]; !ok {
			if _, ok := callResult["content"]; !ok {
				t.Fatalf("tools/call result missing structuredContent/content: %#v", callResult)
			}
		}
	})

	t.Run("legacy initialize fallback without a session header", func(t *testing.T) {
		status, headers, body, err := postJSONRPCRequest(t, endpoint, "2025-11-25", "", "legacy-init", "initialize", map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "legacy-client", "version": "1.0"},
		})
		if err != nil {
			t.Fatalf("initialize failed: %v", err)
		}
		if headers.Get("Mcp-Session-Id") != "" {
			t.Fatalf("legacy initialize returned session header = %q, want empty", headers.Get("Mcp-Session-Id"))
		}
		var envelope jsonRPCEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode initialize response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("initialize rpc error = %#v", envelope.Error)
		}

		status, headers, _, err = postNotificationRequest(t, endpoint, "2025-11-25", "", "notifications/initialized", map[string]any{})
		if err != nil {
			t.Fatalf("notifications/initialized failed: %v", err)
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			t.Fatalf("notifications/initialized status = %d, want 2xx", status)
		}
		if got := headers.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("legacy initialized notification returned session header = %q, want empty", got)
		}

		status, headers, body, err = postJSONRPCRequest(t, endpoint, "2025-11-25", "", "legacy-list", "tools/list", map[string]any{})
		if err != nil {
			t.Fatalf("legacy tools/list failed: %v", err)
		}
		if headers.Get("Mcp-Session-Id") != "" {
			t.Fatalf("legacy tools/list returned session header = %q, want empty", headers.Get("Mcp-Session-Id"))
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode legacy tools/list response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("legacy tools/list rpc error = %#v", envelope.Error)
		}
		status, headers, body, err = postJSONRPCRequest(t, endpoint, "2025-11-25", "", "legacy-call", "tools/call", map[string]any{
			"name":      "get_project",
			"arguments": map[string]any{},
		})
		if err != nil {
			t.Fatalf("legacy tools/call failed: %v", err)
		}
		if headers.Get("Mcp-Session-Id") != "" {
			t.Fatalf("legacy tools/call returned session header = %q, want empty", headers.Get("Mcp-Session-Id"))
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode legacy tools/call response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("legacy tools/call rpc error = %#v", envelope.Error)
		}
	})

	t.Run("protocol negotiation and invalid input failures", func(t *testing.T) {
		status, _, body, err := postJSONRPCRequest(t, endpoint, "2025-11-24", "", "unsupported-version", "tools/list", map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2025-11-24"}})
		if err != nil {
			t.Fatalf("unsupported version request failed: %v", err)
		}
		if status >= http.StatusOK && status < http.StatusMultipleChoices {
			t.Fatalf("unsupported version unexpectedly succeeded with status %d: %s", status, body)
		}
		var envelope jsonRPCEnvelope
		if len(body) > 0 {
			if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error == nil {
				t.Fatalf("unsupported version response missing error payload: %s", body)
			}
		}

		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "removed-method", "initialize", map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "modern-client", "version": "1.0"},
		})
		if err != nil {
			t.Fatalf("removed-method request failed: %v", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode removed-method response: %v", err)
		}
		if envelope.Error == nil {
			t.Fatalf("removed-method response missing error payload: %s", body)
		}

		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "unknown-method", "does/not/exist", map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"}})
		if err != nil {
			t.Fatalf("unknown method request failed: %v", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode unknown-method response: %v", err)
		}
		if envelope.Error == nil {
			t.Fatalf("unknown-method response missing error payload: %s", body)
		}

		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "invalid-params", "tools/call", map[string]any{
			"arguments": map[string]any{},
			"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("invalid params request failed: %v", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode invalid-params response: %v", err)
		}
		if envelope.Error == nil {
			t.Fatalf("invalid-params response missing error payload: %s", body)
		}
		if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
			t.Fatalf("invalid-params status = %d, want client error", status)
		}
	})

	t.Run("stateless delete does not end explicit handles", func(t *testing.T) {
		status, _, body, err := postJSONRPCRequest(t, endpoint, "2026-07-28", "", "create-session", "tools/call", map[string]any{
			"name":      "create_agent_session",
			"arguments": map[string]any{"client_name": "http-conformance"},
			"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("create_agent_session failed: %v", err)
		}
		var envelope jsonRPCEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode create_agent_session response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("create_agent_session rpc error = %#v", envelope.Error)
		}
		var sessionPayload struct {
			StructuredContent struct {
				AgentSessionHandle string `json:"agent_session_handle"`
			} `json:"structuredContent"`
		}
		if err := json.Unmarshal(envelope.Result, &sessionPayload); err != nil {
			t.Fatalf("decode create_agent_session result: %v", err)
		}
		if sessionPayload.StructuredContent.AgentSessionHandle == "" {
			t.Fatal("create_agent_session returned empty agent_session_handle")
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			t.Fatalf("create_agent_session status = %d, want 2xx", status)
		}

		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "omitted-handle", "tools/call", map[string]any{
			"name":      "create_issue",
			"arguments": map[string]any{"type": "task", "title": "omitted-handle"},
			"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("create_issue without handle failed: %v", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode omitted-handle response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("create_issue without handle rpc error = %#v", envelope.Error)
		}

		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "explicit-handle", "tools/call", map[string]any{
			"name": "create_issue",
			"arguments": map[string]any{
				"type":                 "task",
				"title":                "explicit-handle",
				"agent_session_handle": sessionPayload.StructuredContent.AgentSessionHandle,
			},
			"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("create_issue with explicit handle failed: %v", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode explicit-handle response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("create_issue with explicit handle rpc error = %#v", envelope.Error)
		}

		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "invalid-handle", "tools/call", map[string]any{
			"name": "create_issue",
			"arguments": map[string]any{
				"type":                 "task",
				"title":                "invalid-handle",
				"agent_session_handle": "does-not-exist",
			},
			"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("create_issue with invalid handle failed: %v", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode invalid-handle response: %v", err)
		}
		var invalidHandleResult struct {
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(envelope.Result, &invalidHandleResult); err != nil {
			t.Fatalf("decode invalid-handle result: %v", err)
		}
		if !invalidHandleResult.IsError {
			t.Fatalf("invalid handle unexpectedly succeeded: %s", body)
		}

		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "end-session", "tools/call", map[string]any{
			"name": "end_agent_session",
			"arguments": map[string]any{
				"agent_session_handle": sessionPayload.StructuredContent.AgentSessionHandle,
			},
			"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("end_agent_session failed: %v", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode end_agent_session response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("end_agent_session rpc error = %#v", envelope.Error)
		}

		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "ended-handle", "tools/call", map[string]any{
			"name": "create_issue",
			"arguments": map[string]any{
				"type":                 "task",
				"title":                "ended-handle",
				"agent_session_handle": sessionPayload.StructuredContent.AgentSessionHandle,
			},
			"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("create_issue with ended handle failed: %v", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode ended-handle response: %v", err)
		}
		var endedHandleResult struct {
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(envelope.Result, &endedHandleResult); err != nil {
			t.Fatalf("decode ended-handle result: %v", err)
		}
		if !endedHandleResult.IsError {
			t.Fatalf("ended handle unexpectedly succeeded: %s", body)
		}

		request, err := http.NewRequest(http.MethodDelete, endpoint, nil)
		if err != nil {
			t.Fatalf("construct DELETE request: %v", err)
		}
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("send DELETE request: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode >= http.StatusInternalServerError {
			t.Fatalf("DELETE status = %d, want non-5xx", response.StatusCode)
		}
		if got := response.Header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("DELETE returned session header = %q, want empty", got)
		}
		status, _, body, err = postJSONRPCRequest(t, endpoint, "2026-07-28", "", "reused-after-delete", "tools/call", map[string]any{
			"name": "create_issue",
			"arguments": map[string]any{
				"type":                 "task",
				"title":                "reused-after-delete",
				"agent_session_handle": sessionPayload.StructuredContent.AgentSessionHandle,
			},
			"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		})
		if err != nil {
			t.Fatalf("create_issue after DELETE failed: %v", err)
		}
		if status >= http.StatusBadRequest && status < http.StatusMultipleChoices {
			t.Fatalf("create_issue after DELETE failed unexpectedly with status %d: %s", status, body)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode create_issue after DELETE response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("create_issue after DELETE rpc error = %#v", envelope.Error)
		}
	})
}

type integrationHTTPServer struct {
	cmd       *exec.Cmd
	output    *capturedOutput
	endpoint  string
	endpointC chan string
	doneC     chan error

	exitedMu sync.Mutex
	exited   bool
}

// hasExited reports whether cmd.Wait has already returned, without reading
// cmd.ProcessState directly: that field is written by the goroutine running
// cmd.Wait in launchIntegrationHTTPServer, so reading it from another
// goroutine without synchronization is a data race under -race, even though
// it is benign in practice. exited is set under exitedMu by that same
// goroutine immediately after cmd.Wait returns, giving callers here a
// synchronized way to check "has this process already been reaped" before
// deciding whether to signal/kill it or wait on doneC again.
func (server *integrationHTTPServer) hasExited() bool {
	server.exitedMu.Lock()
	defer server.exitedMu.Unlock()
	return server.exited
}

type capturedOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (output *capturedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buf.Write(data)
}

func (output *capturedOutput) WriteString(value string) {
	output.mu.Lock()
	defer output.mu.Unlock()
	_, _ = output.buf.WriteString(value)
}

func (output *capturedOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buf.String()
}

func launchIntegrationHTTPServer(t *testing.T, env integrationEnvironment, httpAddress string, extraServeArgs ...string) *integrationHTTPServer {
	t.Helper()
	return launchIntegrationHTTPServerInDir(t, env, env.repository, httpAddress, extraServeArgs...)
}

func launchIntegrationHTTPServerInDir(t *testing.T, env integrationEnvironment, workingDir, httpAddress string, extraServeArgs ...string) *integrationHTTPServer {
	t.Helper()
	args := append([]string{"--data-root", env.dataRoot, "serve", "--http-address", httpAddress}, extraServeArgs...)
	cmd := exec.Command(integrationBinary, args...)
	cmd.Dir = workingDir

	stderrReader, stderrWriter := io.Pipe()
	output := &capturedOutput{}
	server := &integrationHTTPServer{
		cmd:       cmd,
		output:    output,
		endpointC: make(chan string, 1),
		doneC:     make(chan error, 1),
	}
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		t.Fatalf("start integration HTTP server: %v", err)
	}

	go func() {
		// Keep scanning for the process's full lifetime rather than
		// returning once the endpoint line is found: cmd.Wait() (below)
		// blocks until its internal stderr-copying goroutine finishes, and
		// that goroutine's Write into stderrWriter blocks forever once
		// nobody is left reading stderrReader. Every later request logs an
		// "http request completed" line (see WrapHTTPHandler), so an early
		// return here deadlocks cmd.Wait() as soon as the server handles a
		// request after startup — invisible under stopIntegrationHTTPServer's
		// silent timeout fallback, but fatal for killIntegrationHTTPServer's
		// waitForExit.
		scanner := bufio.NewScanner(stderrReader)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line + "\n")
			if endpoint := parseIntegrationHTTPListenerEndpoint(line); endpoint != "" {
				select {
				case server.endpointC <- endpoint:
				default:
				}
			}
		}
		_ = scanner.Err()
		_ = stderrReader.Close()
	}()
	go func() {
		err := cmd.Wait()
		_ = stderrWriter.Close()
		server.exitedMu.Lock()
		server.exited = true
		server.exitedMu.Unlock()
		server.doneC <- err
	}()
	return server
}

func (server *integrationHTTPServer) waitForEndpoint(t *testing.T) string {
	t.Helper()
	deadline := time.NewTimer(integrationTimeout)
	defer deadline.Stop()
	for {
		select {
		case endpoint := <-server.endpointC:
			server.endpoint = endpoint
			return endpoint
		case err := <-server.doneC:
			t.Fatalf("integration HTTP server exited before listening: %v\nstderr:\n%s", err, server.output.String())
		case <-deadline.C:
			t.Fatalf("timed out waiting for integration HTTP server endpoint\nstderr:\n%s", server.output.String())
		}
	}
}

func (server *integrationHTTPServer) waitForExit(t *testing.T) error {
	t.Helper()
	select {
	case err := <-server.doneC:
		return err
	case <-time.After(integrationTimeout):
		t.Fatalf("timed out waiting for integration HTTP server exit\nstderr:\n%s", server.output.String())
		return nil
	}
}

func stopIntegrationHTTPServer(t *testing.T, server *integrationHTTPServer) {
	t.Helper()
	if server == nil || server.cmd == nil || server.cmd.Process == nil {
		return
	}
	if server.hasExited() {
		return
	}
	if err := server.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = server.cmd.Process.Kill()
	}
	select {
	case err := <-server.doneC:
		_ = err
	case <-time.After(2 * time.Second):
		_ = server.cmd.Process.Kill()
		select {
		case err := <-server.doneC:
			_ = err
		case <-time.After(integrationTimeout):
		}
	}
}

func parseIntegrationHTTPListenerEndpoint(line string) string {
	prefix := "endpoint="
	start := strings.Index(line, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	if start >= len(line) {
		return ""
	}
	value := line[start:]
	if strings.HasPrefix(value, `"`) {
		value = strings.TrimPrefix(value, `"`)
		end := strings.Index(value, `"`)
		if end < 0 {
			return ""
		}
		return value[:end]
	}
	end := strings.IndexAny(value, " \t")
	if end < 0 {
		return value
	}
	return value[:end]
}

func postJSONRPCRequest(t *testing.T, endpoint, protocolVersion, sessionID string, id any, method string, params any) (int, http.Header, []byte, error) {
	t.Helper()
	payloadParams := normalizeMCPParams(protocolVersion, method, params)
	payload := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: payloadParams}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, nil, err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Method", method)
	request.Header.Set("Mcp-Protocol-Version", protocolVersion)
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	if toolName := mcpToolName(method, payloadParams); toolName != "" {
		request.Header.Set("Mcp-Name", toolName)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	return response.StatusCode, response.Header, body, nil
}

func postNotificationRequest(t *testing.T, endpoint, protocolVersion, sessionID, method string, params any) (int, http.Header, []byte, error) {
	t.Helper()
	payloadParams := normalizeMCPParams(protocolVersion, method, params)
	payload := jsonRPCRequest{JSONRPC: "2.0", Method: method, Params: payloadParams}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, nil, err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Method", method)
	request.Header.Set("Mcp-Protocol-Version", protocolVersion)
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	if toolName := mcpToolName(method, payloadParams); toolName != "" {
		request.Header.Set("Mcp-Name", toolName)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	return response.StatusCode, response.Header, body, nil
}

func normalizeMCPParams(protocolVersion, method string, params any) any {
	if protocolVersion != "2026-07-28" {
		return params
	}
	paramsMap, ok := params.(map[string]any)
	if !ok {
		if params == nil {
			paramsMap = map[string]any{}
		} else {
			return params
		}
	}
	meta, ok := paramsMap["_meta"].(map[string]any)
	if !ok {
		meta = map[string]any{}
	}
	if _, ok := meta["io.modelcontextprotocol/protocolVersion"]; !ok {
		meta["io.modelcontextprotocol/protocolVersion"] = protocolVersion
	}
	if _, ok := meta["io.modelcontextprotocol/clientCapabilities"]; !ok {
		meta["io.modelcontextprotocol/clientCapabilities"] = map[string]any{}
	}
	paramsMap["_meta"] = meta
	return paramsMap
}

func mcpToolName(method string, params any) string {
	if method != "tools/call" {
		return ""
	}
	paramsMap, ok := params.(map[string]any)
	if !ok {
		return ""
	}
	toolName, _ := paramsMap["name"].(string)
	return strings.TrimSpace(toolName)
}

func communicateThroughHTTP(t *testing.T, endpoint, clientName string) (map[string]any, string, error) {
	t.Helper()
	httpClient := &http.Client{Timeout: integrationTimeout}
	var sessionID string

	initializeResult, err := postJSONRPC(httpClient, endpoint, sessionID, 1, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName, "version": "1.0"},
	})
	if err != nil {
		return nil, "", err
	}
	sessionID = initializeResult.sessionID
	if _, err := postNotification(httpClient, endpoint, sessionID, "notifications/initialized", map[string]any{}); err != nil {
		return nil, "", err
	}
	listToolsResult, err := postJSONRPC(httpClient, endpoint, sessionID, 2, "tools/list", map[string]any{})
	if err != nil {
		return nil, "", err
	}
	var toolsResponse struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(listToolsResult.result, &toolsResponse); err != nil {
		return nil, "", err
	}
	if len(toolsResponse.Tools) == 0 {
		return nil, "", fmt.Errorf("list_tools returned no tools")
	}
	if err := assertHTTPToolAnnotations(toolsResponse.Tools); err != nil {
		return nil, "", err
	}
	getProjectResult, err := postJSONRPC(httpClient, endpoint, sessionID, 3, "tools/call", map[string]any{
		"name":      "get_project",
		"arguments": map[string]any{},
	})
	if err != nil {
		return nil, "", err
	}
	var getProjectPayload struct {
		StructuredContent map[string]any `json:"structuredContent"`
	}
	if err := json.Unmarshal(getProjectResult.result, &getProjectPayload); err != nil {
		return nil, "", err
	}
	if len(getProjectPayload.StructuredContent) == 0 {
		return nil, "", fmt.Errorf("get_project returned no structured content")
	}
	return getProjectPayload.StructuredContent, sessionID, nil
}

// assertHTTPToolAnnotations spot-checks that get_project's read-only
// annotation survives the HTTP JSON-RPC round trip, since the raw
// map[string]any decoding here (unlike the typed stdio client) would
// silently accept a missing "annotations" key.
func assertHTTPToolAnnotations(tools []map[string]any) error {
	for _, tool := range tools {
		if tool["name"] != "get_project" {
			continue
		}
		annotations, ok := tool["annotations"].(map[string]any)
		if !ok {
			return fmt.Errorf("get_project has no annotations over HTTP transport: %#v", tool)
		}
		if readOnly, ok := annotations["readOnlyHint"].(bool); !ok || !readOnly {
			return fmt.Errorf("get_project readOnlyHint over HTTP = %#v, want true", annotations["readOnlyHint"])
		}
		return nil
	}
	return fmt.Errorf("get_project not found in HTTP tools/list response")
}

type jsonRPCEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	result    json.RawMessage
	sessionID string
}

func postJSONRPC(client *http.Client, endpoint, sessionID string, id any, method string, params any) (*jsonRPCResponse, error) {
	payload := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
	var envelope jsonRPCEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", envelope.Error.Message)
	}
	return &jsonRPCResponse{result: envelope.Result, sessionID: resp.Header.Get("Mcp-Session-Id")}, nil
}

func postNotification(client *http.Client, endpoint, sessionID, method string, params any) (*jsonRPCResponse, error) {
	payload := jsonRPCRequest{JSONRPC: "2.0", Method: method, Params: params}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
	return &jsonRPCResponse{result: nil, sessionID: resp.Header.Get("Mcp-Session-Id")}, nil
}

func assertDistinctHTTPAgentSessions(t *testing.T, repositoryPath, dataRoot string, wantActive int) error {
	t.Helper()
	projectDB, err := sqlite.Open(context.Background(), mustProjectDatabasePath(t, integrationEnvironment{repository: repositoryPath, dataRoot: dataRoot}), sqlite.Options{})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := projectDB.Close(context.Background()); closeErr != nil {
			t.Errorf("close project db: %v", closeErr)
		}
	}()
	var count int
	var distinctNames int
	err = projectDB.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_sessions WHERE ended_at IS NULL").Scan(&count); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT COUNT(DISTINCT client_name) FROM agent_sessions WHERE ended_at IS NULL").Scan(&distinctNames)
	})
	if err != nil {
		return err
	}
	if count != wantActive {
		return fmt.Errorf("active agent sessions = %d, want %d", count, wantActive)
	}
	if distinctNames != wantActive {
		return fmt.Errorf("distinct client names = %d, want %d", distinctNames, wantActive)
	}
	return nil
}
