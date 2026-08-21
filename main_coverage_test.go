package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/config"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectconfig"
)

type testBoardServeService struct{}

func (testBoardServeService) GetBoard(context.Context) (domain.BoardResult, error) {
	return domain.BoardResult{}, nil
}

func (testBoardServeService) GetIssueDetail(context.Context, string) (domain.IssueDetail, error) {
	return domain.IssueDetail{}, nil
}

func (testBoardServeService) Search(context.Context, domain.SearchInput) (domain.SearchPage, error) {
	return domain.SearchPage{}, nil
}

type captureWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	ch   chan string
	once sync.Once
}

func (writer *captureWriter) Write(p []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	n, err := writer.buf.Write(p)
	if writer.ch != nil {
		writer.once.Do(func() {
			writer.ch <- writer.buf.String()
		})
	}
	return n, err
}

func (writer *captureWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buf.String()
}

func TestBoardServeServiceReportsMissingDependencies(t *testing.T) {
	service := boardServeService{}

	if _, err := service.GetBoard(context.Background()); err == nil || !strings.Contains(err.Error(), "board service is not configured") {
		t.Fatalf("GetBoard() error = %v, want missing board service", err)
	}
	if _, err := service.GetIssueDetail(context.Background(), "ISSUE-1"); err == nil || !strings.Contains(err.Error(), "issue detail service is not configured") {
		t.Fatalf("GetIssueDetail() error = %v, want missing issue detail service", err)
	}
	if _, err := service.Search(context.Background(), domain.SearchInput{}); err == nil || !strings.Contains(err.Error(), "search service is not configured") {
		t.Fatalf("Search() error = %v, want missing search service", err)
	}
}

func TestRunBoardServeStartsLoopbackServerAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdout := &captureWriter{ch: make(chan string, 1)}
	errCh := make(chan error, 1)
	go func() {
		errCh <- runBoardServe(ctx, &config.Config{HTTPAddress: "127.0.0.1:0"}, stdout, testBoardServeService{})
	}()

	var listenerURL string
	select {
	case listenerURL = <-stdout.ch:
		listenerURL = strings.TrimSpace(listenerURL)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for listener URL")
	}
	if !strings.HasPrefix(listenerURL, "http://") {
		t.Fatalf("listener URL = %q, want loopback URL", listenerURL)
	}

	resp, err := http.Get(listenerURL)
	if err != nil {
		t.Fatalf("GET %s: %v", listenerURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", listenerURL, resp.StatusCode, http.StatusOK)
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runBoardServe() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}
}

func TestNewHTTPHandlerRejectsNilRouterAndRoutesMCPPaths(t *testing.T) {
	if _, err := newHTTPHandler(&config.Config{ServerName: "test", Version: "v1"}, nil); err == nil || !strings.Contains(err.Error(), "project router is required") {
		t.Fatalf("newHTTPHandler(nil router) error = %v, want required-router error", err)
	}

	router := newProjectRouter(filepath.Join(t.TempDir(), "data"), clock.RealClock{}, sqlite.Options{}, nil)
	handler, err := newHTTPHandler(&config.Config{ServerName: "test", Version: "v1"}, router)
	if err != nil {
		t.Fatalf("newHTTPHandler(router) error = %v", err)
	}

	for _, path := range []string{"/mcp", "/mcp/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code == http.StatusNotFound {
			t.Fatalf("handler for %s returned 404, want routed handler", path)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/not-mcp", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("handler for /not-mcp status = %d, want 404", rr.Code)
	}
}

func TestRunServeHTTPRejectsNilRouter(t *testing.T) {
	err := runServeHTTP(context.Background(), &config.Config{HTTPAddress: "127.0.0.1:0"}, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "project router is required") {
		t.Fatalf("runServeHTTP(nil router) error = %v, want required-router error", err)
	}
}

func TestRunConnectRejectsUnsupportedTargetAndConfigWritersWork(t *testing.T) {
	if err := runConnect(context.Background(), t.TempDir(), "unsupported", "/tmp/rhizome", false, false, false, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unsupported target") {
		t.Fatalf("runConnect() error = %v, want unsupported target", err)
	}

	t.Setenv("PATH", t.TempDir())
	if canExecuteCodex() {
		t.Fatal("canExecuteCodex() = true, want false when codex is absent")
	}

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"servers":{"existing":{"command":"/old"}}}`), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}
	newConfig := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"rhizome-mcp": map[string]interface{}{"command": "/tmp/rhizome", "args": []string{"serve"}},
		},
	}
	if err := mergeAndWriteJSONConfig(configPath, newConfig, "mcpServers", "rhizome-mcp"); err != nil {
		t.Fatalf("mergeAndWriteJSONConfig() error = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	var merged map[string]interface{}
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("unmarshal merged config: %v", err)
	}
	servers, ok := merged["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("merged config mcpServers = %#v, want object", merged["mcpServers"])
	}
	entry, ok := servers["rhizome-mcp"].(map[string]interface{})
	if !ok {
		t.Fatalf("merged config entry = %#v, want object", servers["rhizome-mcp"])
	}
	if entry["command"] != "/tmp/rhizome" {
		t.Fatalf("merged config command = %v, want /tmp/rhizome", entry["command"])
	}

	var buffer bytes.Buffer
	if err := writeJSONToWriter(&buffer, map[string]interface{}{"ok": true}); err != nil {
		t.Fatalf("writeJSONToWriter() error = %v", err)
	}
	if !strings.Contains(buffer.String(), `"ok": true`) {
		t.Fatalf("writeJSONToWriter() output = %q, want indented JSON", buffer.String())
	}
}

func TestRunConnectJSONPrintOnlyEmitsStructuredJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	startingPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(startingPath, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}`), 0o644); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	// Discover resolves symlinks in the root it returns (matching os.Executable
	// + EvalSymlinks elsewhere); on macOS t.TempDir() lives under a /var
	// symlink to /private/var, so the expected value must be resolved too.
	resolvedStartingPath, err := filepath.EvalSymlinks(startingPath)
	if err != nil {
		t.Fatalf("resolve starting path symlinks: %v", err)
	}

	if err := runConnect(context.Background(), startingPath, "json", "/tmp/rhizome", true, false, false, &stdout, &stderr); err != nil {
		t.Fatalf("runConnect(json, printOnly) error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("runConnect(json, printOnly) stderr = %q, want empty", stderr.String())
	}

	var got map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal JSON output: %v", err)
	}
	servers, ok := got["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers = %#v, want object", got["mcpServers"])
	}
	entry, ok := servers["rhizome-mcp"].(map[string]interface{})
	if !ok {
		t.Fatalf("rhizome-mcp entry = %#v, want object", servers["rhizome-mcp"])
	}
	if entry["command"] != "/tmp/rhizome" {
		t.Fatalf("command = %v, want /tmp/rhizome", entry["command"])
	}
	args, ok := entry["args"].([]interface{})
	if !ok || len(args) != 3 || args[0] != "serve" || args[1] != "--project-root" || args[2] != resolvedStartingPath {
		t.Fatalf("args = %#v, want [serve --project-root %s]", entry["args"], resolvedStartingPath)
	}
	if _, err := os.Stat(filepath.Join(startingPath, ".mcp.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("print-only JSON target unexpectedly wrote config at %v", err)
	}
}

func TestRunConnectFailsClearlyOutsideProject(t *testing.T) {
	var stdout, stderr bytes.Buffer
	startingPath := t.TempDir()
	err := runConnect(context.Background(), startingPath, "json", "/tmp/rhizome", true, false, false, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error outside any project")
	}
	if !strings.Contains(err.Error(), "no rhizome-mcp project found") {
		t.Fatalf("error = %v, want a clear no-project message", err)
	}
}

func TestRunConnectEmitsNpxFormWhenLaunchedViaNpmWrapper(t *testing.T) {
	var stdout, stderr bytes.Buffer
	startingPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(startingPath, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}`), 0o644); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	resolvedStartingPath, err := filepath.EvalSymlinks(startingPath)
	if err != nil {
		t.Fatalf("resolve starting path symlinks: %v", err)
	}
	if err := runConnect(context.Background(), startingPath, "json", "/tmp/rhizome", true, false, true, &stdout, &stderr); err != nil {
		t.Fatalf("runConnect() error = %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal JSON output: %v", err)
	}
	servers := got["mcpServers"].(map[string]interface{})
	entry := servers["rhizome-mcp"].(map[string]interface{})
	if entry["command"] != "npx" {
		t.Fatalf("command = %v, want npx", entry["command"])
	}
	args, ok := entry["args"].([]interface{})
	want := []interface{}{"-y", "rhizome-mcp", "serve", "--project-root", resolvedStartingPath}
	if !ok || len(args) != len(want) {
		t.Fatalf("args = %#v, want %v", entry["args"], want)
	}
	for index := range want {
		if args[index] != want[index] {
			t.Fatalf("args = %#v, want %v", entry["args"], want)
		}
	}
}

func TestRunConnectBareCommandOptInEmitsPortableCommandName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	startingPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(startingPath, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}`), 0o644); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	if err := runConnect(context.Background(), startingPath, "json", "/tmp/rhizome", true, true, false, &stdout, &stderr); err != nil {
		t.Fatalf("runConnect() error = %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal JSON output: %v", err)
	}
	servers := got["mcpServers"].(map[string]interface{})
	entry := servers["rhizome-mcp"].(map[string]interface{})
	if entry["command"] != "rhizome-mcp" {
		t.Fatalf("command = %v, want the bare portable command name rhizome-mcp", entry["command"])
	}
}

func TestConnectClaudeAndVSCodeMergeExistingConfig(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		startingPath := t.TempDir()
		configPath := filepath.Join(startingPath, ".mcp.json")
		initialConfig := map[string]interface{}{
			"mcpServers": map[string]interface{}{
				"other-server": map[string]interface{}{"type": "stdio", "command": "other", "args": []string{"run"}},
			},
			"otherKey": "value",
		}
		initialData, err := json.MarshalIndent(initialConfig, "", "  ")
		if err != nil {
			t.Fatalf("marshal initial Claude config: %v", err)
		}
		if err := os.WriteFile(configPath, initialData, 0o644); err != nil {
			t.Fatalf("write initial Claude config: %v", err)
		}

		invocation := resolveConnectServeInvocation("/tmp/rhizome", startingPath, false, false)
		if err := connectClaude(startingPath, invocation, false, io.Discard); err != nil {
			t.Fatalf("connectClaude() error = %v", err)
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read Claude config: %v", err)
		}
		var merged map[string]interface{}
		if err := json.Unmarshal(data, &merged); err != nil {
			t.Fatalf("unmarshal Claude config: %v", err)
		}
		if merged["otherKey"] != "value" {
			t.Fatalf("otherKey = %v, want value", merged["otherKey"])
		}
		servers, ok := merged["mcpServers"].(map[string]interface{})
		if !ok {
			t.Fatalf("Claude mcpServers = %#v, want object", merged["mcpServers"])
		}
		if _, ok := servers["other-server"]; !ok {
			t.Fatal("other-server entry was not preserved")
		}
		entry, ok := servers["rhizome-mcp"].(map[string]interface{})
		if !ok {
			t.Fatalf("rhizome-mcp entry = %#v, want object", servers["rhizome-mcp"])
		}
		if entry["command"] != "/tmp/rhizome" {
			t.Fatalf("Claude command = %v, want /tmp/rhizome", entry["command"])
		}
		args, ok := entry["args"].([]interface{})
		if !ok || len(args) != 3 || args[0] != "serve" || args[1] != "--project-root" || args[2] != startingPath {
			t.Fatalf("Claude args = %#v, want [serve --project-root %s]", entry["args"], startingPath)
		}
	})

	t.Run("vscode", func(t *testing.T) {
		startingPath := t.TempDir()
		configPath := filepath.Join(startingPath, ".vscode", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatalf("create .vscode dir: %v", err)
		}
		initialConfig := map[string]interface{}{
			"servers": map[string]interface{}{
				"other-server": map[string]interface{}{"type": "stdio", "command": "other", "args": []string{"run"}},
			},
			"otherKey": "value",
		}
		initialData, err := json.MarshalIndent(initialConfig, "", "  ")
		if err != nil {
			t.Fatalf("marshal initial VS Code config: %v", err)
		}
		if err := os.WriteFile(configPath, initialData, 0o644); err != nil {
			t.Fatalf("write initial VS Code config: %v", err)
		}

		var stdout bytes.Buffer
		invocation := resolveConnectServeInvocation("/tmp/rhizome", startingPath, false, false)
		if err := connectVSCode(startingPath, invocation, false, &stdout); err != nil {
			t.Fatalf("connectVSCode() error = %v", err)
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read VS Code config: %v", err)
		}
		var merged map[string]interface{}
		if err := json.Unmarshal(data, &merged); err != nil {
			t.Fatalf("unmarshal VS Code config: %v", err)
		}
		if merged["otherKey"] != "value" {
			t.Fatalf("otherKey = %v, want value", merged["otherKey"])
		}
		servers, ok := merged["servers"].(map[string]interface{})
		if !ok {
			t.Fatalf("VS Code servers = %#v, want object", merged["servers"])
		}
		if _, ok := servers["other-server"]; !ok {
			t.Fatal("other-server entry was not preserved")
		}
		entry, ok := servers["rhizome-mcp"].(map[string]interface{})
		if !ok {
			t.Fatalf("rhizome-mcp entry = %#v, want object", servers["rhizome-mcp"])
		}
		if entry["command"] != "/tmp/rhizome" {
			t.Fatalf("VS Code command = %v, want /tmp/rhizome", entry["command"])
		}
		args, ok := entry["args"].([]interface{})
		if !ok || len(args) != 3 || args[0] != "serve" || args[1] != "--project-root" || args[2] != startingPath {
			t.Fatalf("VS Code args = %#v, want [serve --project-root %s]", entry["args"], startingPath)
		}
		if !strings.Contains(stdout.String(), "Wrote .vscode/mcp.json.") {
			t.Fatalf("VS Code output = %q, want success message", stdout.String())
		}
	})
}

func TestMergeAndWriteJSONConfigReportsParseAndWriteErrors(t *testing.T) {
	malformedPath := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(malformedPath, []byte(`{"mcpServers":`), 0o644); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}
	if err := mergeAndWriteJSONConfig(malformedPath, map[string]interface{}{"mcpServers": map[string]interface{}{}}, "mcpServers", "rhizome-mcp"); err == nil || !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("mergeAndWriteJSONConfig(malformed) error = %v, want parse config file", err)
	}

	writePath := filepath.Join(t.TempDir(), "missing", "config.json")
	if err := mergeAndWriteJSONConfig(writePath, map[string]interface{}{"mcpServers": map[string]interface{}{}}, "mcpServers", "rhizome-mcp"); err == nil || !strings.Contains(err.Error(), "write config file") {
		t.Fatalf("mergeAndWriteJSONConfig(write fail) error = %v, want write config file", err)
	}
}

func TestComposedServicesNilSafeAccessorsAndClose(t *testing.T) {
	var bundle *composedServices
	if got := bundle.ProjectRef(); got != "" {
		t.Fatalf("nil ProjectRef() = %q, want empty", got)
	}
	services := bundle.ProjectServices()
	if services.IssueService != nil || services.ProjectService != nil || services.RelationService != nil || services.GraphService != nil || services.PlanningService != nil || services.CommentService != nil || services.DecisionService != nil || services.ActivityService != nil || services.SearchService != nil || services.ReviewService != nil || services.AttemptService != nil || services.SessionService != nil || services.WorkContextService != nil {
		t.Fatalf("nil ProjectServices() returned non-zero services: %#v", services)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatalf("nil Close() error = %v, want nil", err)
	}

	bundle = &composedServices{}
	if got := bundle.ProjectRef(); got != "" {
		t.Fatalf("empty ProjectRef() = %q, want empty", got)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatalf("empty Close() error = %v, want nil", err)
	}
}
