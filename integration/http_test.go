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
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestIntegrationHTTPAdversarialRequestsAreRejected(t *testing.T) {
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

func TestIntegrationHTTPServeEphemeralPortWorkflow(t *testing.T) {
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	if _, _, err := communicateThroughHTTP(t, endpoint, "http-client"); err != nil {
		t.Fatalf("HTTP workflow failed: %v\nstderr:\n%s", err, server.output.String())
	}
}

func TestIntegrationHTTPServeConcurrentClientsOnEphemeralPort(t *testing.T) {
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

	if err := assertDistinctHTTPAgentSessions(t, env.repository, env.dataRoot, 3); err != nil {
		t.Fatalf("assert concurrent HTTP agent sessions: %v", err)
	}
}

func TestIntegrationHTTPServeStopsOnInterrupt(t *testing.T) {
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
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "localhost:0")
	if err := server.waitForExit(t); err == nil {
		t.Fatalf("expected hostname address to fail startup")
	}
	if stderr := server.output.String(); !strings.Contains(stderr, "invalid http address") {
		t.Fatalf("expected invalid address error in stderr, got %s", stderr)
	}
}

type integrationHTTPServer struct {
	cmd       *exec.Cmd
	output    *capturedOutput
	endpoint  string
	endpointC chan string
	doneC     chan error
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

func launchIntegrationHTTPServer(t *testing.T, env integrationEnvironment, httpAddress string) *integrationHTTPServer {
	t.Helper()
	cmd := exec.Command(integrationBinary, "--data-root", env.dataRoot, "serve", "--http-address", httpAddress)
	cmd.Dir = env.repository

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
		scanner := bufio.NewScanner(stderrReader)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line + "\n")
			if endpoint := parseIntegrationHTTPListenerEndpoint(line); endpoint != "" {
				select {
				case server.endpointC <- endpoint:
				default:
				}
				return
			}
		}
		_ = scanner.Err()
		_ = stderrReader.Close()
	}()
	go func() {
		err := cmd.Wait()
		_ = stderrWriter.Close()
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
	if server.cmd.ProcessState != nil {
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
