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
	"sync"
	"testing"
	"time"

	"rhizome-mcp/config"
	"rhizome-mcp/internal/adapters/sqlite"
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

	integrationBinary = filepath.Join(tempDir, "rhizome-mcp")
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

func TestIntegrationHTTPTransportIsolatesSessions(t *testing.T) {
	env := newIntegrationEnvironment(t)
	ctx := context.Background()
	pathInputs := projectconfig.PathInputs{GOOS: runtime.GOOS, HomeDir: t.TempDir(), XDGDataHome: t.TempDir()}
	bundle, project, err := composeServices(ctx, env.repository, pathInputs, env.dataRoot)
	if err != nil {
		t.Fatalf("compose services: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()
		if closeErr := project.Close(closeCtx); closeErr != nil {
			t.Errorf("close project: %v", closeErr)
		}
	}()

	handler, err := newHTTPHandler(&config.Config{ServerName: "rhizome-http-test", Version: "test"}, bundle)
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
		if item.sessionID == "" {
			t.Fatalf("client %s did not receive a session ID", item.clientName)
		}
		if _, ok := item.result["project"]; !ok {
			t.Fatalf("client %s get_project result missing project payload: %#v", item.clientName, item.result)
		}
	}

	if _, _, err := communicateThroughHTTP(t, server.URL+"/mcp", "client-c"); err != nil {
		t.Fatalf("later HTTP connection failed: %v", err)
	}

	if err := assertDistinctHTTPAgentSessions(t, env.repository, env.dataRoot, 3); err != nil {
		t.Fatalf("assert HTTP agent sessions: %v", err)
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
	if sessionID == "" {
		return nil, "", fmt.Errorf("initialize did not return a session ID")
	}
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
