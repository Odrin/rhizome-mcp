//go:build integration

// This file holds the one integration test that cannot move into the
// dedicated integration/ package: TestIntegrationHTTPTransportIsolatesSessions
// calls composeServices and newHTTPHandler directly (unexported package-main
// internals) to build an in-process httptest server, and Go does not allow
// splitting one package across directories. Every other integration test
// lives in integration/ and only shells out to the compiled binary or talks
// to it over stdio/HTTP; see integration/environment_test.go for their shared
// helpers. This file necessarily duplicates the small amount of plumbing
// (binary build, temp repository setup, raw HTTP JSON-RPC helpers) that this
// one test still needs.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/config"
	"rhizome-mcp/internal/projectconfig"
)

const integrationTimeout = 10 * time.Second

var integrationBinary string

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "rhizome-mcp-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create integration build directory: %v\n", err)
		os.Exit(1)
	}

	binaryName := "rhizome-mcp"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	integrationBinary = filepath.Join(tempDir, binaryName)
	command := exec.Command("go", "build", "-o", integrationBinary, ".")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build integration server: %v\n%s", err, output.String())
		exitIntegrationTests(tempDir, 1)
	}

	exitIntegrationTests(tempDir, m.Run())
}

func exitIntegrationTests(tempDir string, exitCode int) {
	if err := os.RemoveAll(tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "remove integration build directory: %v\n", err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

type integrationEnvironment struct {
	repository string
	dataRoot   string
}

func newIntegrationEnvironment(t *testing.T) integrationEnvironment {
	t.Helper()
	tempDir := t.TempDir()
	env := integrationEnvironment{
		repository: filepath.Join(tempDir, "repository"),
		dataRoot:   filepath.Join(tempDir, "data"),
	}
	if err := os.Mkdir(env.repository, 0o755); err != nil {
		t.Fatalf("create test repository: %v", err)
	}
	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "init")
	return env
}

func runIntegrationCommand(t *testing.T, env integrationEnvironment, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, integrationBinary, args...)
	command.Dir = env.repository
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("%s failed: %v\nstdout:\n%s\nstderr:\n%s", command.String(), err, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

func TestIntegrationHTTPMCPConformanceMatrix(t *testing.T) {
	env := newIntegrationEnvironment(t)
	ctx := context.Background()
	pathInputs := projectconfig.PathInputs{GOOS: runtime.GOOS, HomeDir: t.TempDir(), XDGDataHome: t.TempDir()}
	bundle, _, err := composeServices(ctx, env.repository, pathInputs, env.dataRoot)
	if err != nil {
		t.Fatalf("compose services: %v", err)
	}
	router := newProjectRouter(env.dataRoot, clock.RealClock{}, sqlite.Options{}, bundle)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()
		if closeErr := router.Close(closeCtx); closeErr != nil {
			t.Errorf("close project: %v", closeErr)
		}
	}()

	handler, err := newHTTPHandler(&config.Config{ServerName: "rhizome-http-test", Version: "test"}, router)
	if err != nil {
		t.Fatalf("create HTTP handler: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	httpClient := &http.Client{Timeout: integrationTimeout}
	discoverResult, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 1, "2026-07-28", "server/discover", map[string]any{})
	if err != nil {
		t.Fatalf("server/discover failed: %v", err)
	}
	if discoverResult.sessionID != "" {
		t.Fatalf("modern discovery returned session header = %q, want empty", discoverResult.sessionID)
	}
	var discoverPayload map[string]any
	if err := json.Unmarshal(discoverResult.result, &discoverPayload); err != nil {
		t.Fatalf("decode discover result: %v", err)
	}
	serverInfoFound := false
	if _, ok := discoverPayload["serverInfo"]; ok {
		serverInfoFound = true
	} else if meta, ok := discoverPayload["_meta"].(map[string]any); ok {
		_, serverInfoFound = meta["io.modelcontextprotocol/serverInfo"]
	}
	if !serverInfoFound {
		t.Fatalf("discover result missing serverInfo: %#v", discoverPayload)
	}

	toolsResult, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 2, "2026-07-28", "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	if toolsResult.sessionID != "" {
		t.Fatalf("tools/list returned session header = %q, want empty", toolsResult.sessionID)
	}

	callResult, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 3, "2026-07-28", "tools/call", map[string]any{
		"name":      "get_project",
		"arguments": map[string]any{},
		"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
	})
	if err != nil {
		t.Fatalf("tools/call failed: %v", err)
	}
	if callResult.sessionID != "" {
		t.Fatalf("tools/call returned session header = %q, want empty", callResult.sessionID)
	}

	legacyInitialize, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 4, "2025-11-25", "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "in-process-client", "version": "1.0"},
	})
	if err != nil {
		t.Fatalf("legacy initialize failed: %v", err)
	}
	if legacyInitialize.sessionID != "" {
		t.Fatalf("legacy initialize returned session header = %q, want empty", legacyInitialize.sessionID)
	}
	if _, err := postNotificationWithProtocolVersion(httpClient, server.URL+"/mcp", "", "2025-11-25", "notifications/initialized", map[string]any{}); err != nil {
		t.Fatalf("legacy initialized notification failed: %v", err)
	}

	legacyTools, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 6, "2025-11-25", "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("legacy tools/list failed: %v", err)
	}
	if legacyTools.sessionID != "" {
		t.Fatalf("legacy tools/list returned session header = %q, want empty", legacyTools.sessionID)
	}

	createSession, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 7, "2026-07-28", "tools/call", map[string]any{
		"name": "create_agent_session",
		"arguments": map[string]any{
			"client_name": "http-conformance",
		},
		"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
	})
	if err != nil {
		t.Fatalf("create_agent_session failed: %v", err)
	}
	var createdSession struct {
		StructuredContent struct {
			Handle string `json:"agent_session_handle"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(createSession.result, &createdSession); err != nil {
		t.Fatalf("decode create_agent_session result: %v", err)
	}
	if createdSession.StructuredContent.Handle == "" {
		t.Fatal("create_agent_session returned empty handle")
	}

	omittedHandle, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 8, "2026-07-28", "tools/call", map[string]any{
		"name":      "create_issue",
		"arguments": map[string]any{"type": "task", "title": "omitted-handle"},
		"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
	})
	if err != nil {
		t.Fatalf("create_issue without handle failed: %v", err)
	}
	if omittedHandle.sessionID != "" {
		t.Fatalf("create_issue without handle returned session header = %q, want empty", omittedHandle.sessionID)
	}

	explicitHandle, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 9, "2026-07-28", "tools/call", map[string]any{
		"name": "create_issue",
		"arguments": map[string]any{
			"type":                 "task",
			"title":                "explicit-handle",
			"agent_session_handle": createdSession.StructuredContent.Handle,
		},
		"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
	})
	if err != nil {
		t.Fatalf("create_issue with handle failed: %v", err)
	}
	if explicitHandle.sessionID != "" {
		t.Fatalf("create_issue with handle returned session header = %q, want empty", explicitHandle.sessionID)
	}

	invalidHandle, err := postJSONRPCWithProtocolVersion(httpClient, server.URL+"/mcp", "", 10, "2026-07-28", "tools/call", map[string]any{
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
	if invalidHandle.sessionID != "" {
		t.Fatalf("invalid handle request returned session header = %q, want empty", invalidHandle.sessionID)
	}
	if invalidHandle.result == nil || len(invalidHandle.result) == 0 {
		t.Fatal("invalid handle request did not return a JSON-RPC error envelope")
	}
}

func TestIntegrationHTTPTransportIsolatesSessions(t *testing.T) {
	env := newIntegrationEnvironment(t)
	ctx := context.Background()
	pathInputs := projectconfig.PathInputs{GOOS: runtime.GOOS, HomeDir: t.TempDir(), XDGDataHome: t.TempDir()}
	bundle, _, err := composeServices(ctx, env.repository, pathInputs, env.dataRoot)
	if err != nil {
		t.Fatalf("compose services: %v", err)
	}
	router := newProjectRouter(env.dataRoot, clock.RealClock{}, sqlite.Options{}, bundle)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()
		if closeErr := router.Close(closeCtx); closeErr != nil {
			t.Errorf("close project: %v", closeErr)
		}
	}()

	handler, err := newHTTPHandler(&config.Config{ServerName: "rhizome-http-test", Version: "test"}, router)
	if err != nil {
		t.Fatalf("create HTTP handler: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	results := make(chan struct {
		clientName string
		result     map[string]any
		sessionID  string
		toolName   string
		err        error
	}, 2)
	var wg sync.WaitGroup
	for _, clientName := range []string{"client-a", "client-b"} {
		clientName := clientName
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, sessionID, err := communicateThroughHTTP(t, server.URL+"/mcp", clientName)
			if err != nil {
				results <- struct {
					clientName string
					result     map[string]any
					sessionID  string
					toolName   string
					err        error
				}{clientName: clientName, err: err}
				return
			}
			results <- struct {
				clientName string
				result     map[string]any
				sessionID  string
				toolName   string
				err        error
			}{clientName: clientName, result: result, sessionID: sessionID}
		}()
	}
	wg.Wait()
	close(results)

	var seen []struct {
		clientName string
		result     map[string]any
		sessionID  string
		toolName   string
		err        error
	}
	for item := range results {
		seen = append(seen, item)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 client results, got %d", len(seen))
	}
	for _, item := range seen {
		if item.err != nil {
			t.Fatalf("client %s failed: %v", item.clientName, item.err)
		}
		if _, ok := item.result["project"]; !ok {
			t.Fatalf("client %s get_project result missing project payload: %#v", item.clientName, item.result)
		}
	}

	if _, _, err := communicateThroughHTTP(t, server.URL+"/mcp", "client-c"); err != nil {
		t.Fatalf("later HTTP connection failed: %v", err)
	}

	if err := assertDistinctHTTPAgentSessions(t, env.repository, env.dataRoot, 0); err != nil {
		t.Fatalf("assert no automatic HTTP agent sessions: %v", err)
	}
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
	return postJSONRPCWithProtocolVersion(client, endpoint, sessionID, id, "2025-11-25", method, params)
}

func postJSONRPCWithProtocolVersion(client *http.Client, endpoint, sessionID string, id any, protocolVersion, method string, params any) (*jsonRPCResponse, error) {
	payloadParams := normalizeMCPParams(protocolVersion, method, params)
	payload := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: payloadParams}
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
	req.Header.Set("Mcp-Method", method)
	req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if toolName := mcpToolName(method, payloadParams); toolName != "" {
		req.Header.Set("Mcp-Name", toolName)
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
	return postNotificationWithProtocolVersion(client, endpoint, sessionID, "2025-11-25", method, params)
}

func postNotificationWithProtocolVersion(client *http.Client, endpoint, sessionID, protocolVersion, method string, params any) (*jsonRPCResponse, error) {
	payloadParams := normalizeMCPParams(protocolVersion, method, params)
	payload := jsonRPCRequest{JSONRPC: "2.0", Method: method, Params: payloadParams}
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
	req.Header.Set("Mcp-Method", method)
	req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if toolName := mcpToolName(method, payloadParams); toolName != "" {
		req.Header.Set("Mcp-Name", toolName)
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

func mustProjectDatabasePath(t *testing.T, env integrationEnvironment) string {
	t.Helper()
	project, err := projectconfig.Discover(env.repository)
	if err != nil {
		t.Fatalf("discover project identity: %v", err)
	}
	databasePath, err := projectconfig.ProjectDatabasePath(env.dataRoot, project.Identity.ProjectID)
	if err != nil {
		t.Fatalf("resolve project database path: %v", err)
	}
	return databasePath
}
