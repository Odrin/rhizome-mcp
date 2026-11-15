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
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cliadapter "rhizome-mcp/internal/adapters/cli"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/compose"
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

	router := compose.NewRouter(filepath.Join(t.TempDir(), "data"), clock.RealClock{}, sqlite.Options{}, nil)
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

	nullPath := filepath.Join(t.TempDir(), "null.json")
	if err := os.WriteFile(nullPath, []byte(`null`), 0o644); err != nil {
		t.Fatalf("write null config: %v", err)
	}
	if err := mergeAndWriteJSONConfig(nullPath, map[string]interface{}{"mcpServers": map[string]interface{}{}}, "mcpServers", "rhizome-mcp"); err == nil || !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("mergeAndWriteJSONConfig(null) error = %v, want parse config file", err)
	}

	nonObjectSectionPath := filepath.Join(t.TempDir(), "bad-section.json")
	if err := os.WriteFile(nonObjectSectionPath, []byte(`{"context_servers":"not-an-object"}`), 0o644); err != nil {
		t.Fatalf("write non-object section config: %v", err)
	}
	if err := mergeAndWriteJSONConfig(nonObjectSectionPath, map[string]interface{}{"context_servers": map[string]interface{}{}}, "context_servers", "rhizome-mcp"); err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
		t.Fatalf("mergeAndWriteJSONConfig(non-object section) error = %v, want JSON object error", err)
	}

	nestedPath := filepath.Join(t.TempDir(), "missing", "nested", "config.json")
	if err := mergeAndWriteJSONConfig(nestedPath, map[string]interface{}{"mcpServers": map[string]interface{}{"rhizome-mcp": map[string]interface{}{"command": "/tmp/rhizome"}}}, "mcpServers", "rhizome-mcp"); err != nil {
		t.Fatalf("mergeAndWriteJSONConfig(auto-mkdir) error = %v", err)
	}
	if _, err := os.Stat(nestedPath); err != nil {
		t.Fatalf("expected nested config file to be created: %v", err)
	}

	parentFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("file"), 0o644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	if err := mergeAndWriteJSONConfig(filepath.Join(parentFile, "config.json"), map[string]interface{}{"mcpServers": map[string]interface{}{}}, "mcpServers", "rhizome-mcp"); err == nil || !strings.Contains(err.Error(), "stat config file") {
		t.Fatalf("mergeAndWriteJSONConfig(stat fail) error = %v, want stat config file", err)
	}

	if goruntime.GOOS == "windows" || os.Geteuid() != 0 {
		readOnlyPath := filepath.Join(t.TempDir(), "readonly.json")
		if err := os.WriteFile(readOnlyPath, []byte(`{}`), 0o444); err != nil {
			t.Fatalf("write readonly config: %v", err)
		}
		if err := mergeAndWriteJSONConfig(readOnlyPath, map[string]interface{}{"mcpServers": map[string]interface{}{}}, "mcpServers", "rhizome-mcp"); err == nil || !strings.Contains(err.Error(), "write config file") {
			t.Fatalf("mergeAndWriteJSONConfig(write fail) error = %v, want write config file", err)
		}
	}
}

func initTestProjectDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project dir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}`), 0o644); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve project dir symlinks: %v", err)
	}
	return resolved
}

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func readJSONMap(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read JSON file %s: %v", path, err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal JSON file %s: %v", path, err)
	}
	return parsed
}

func toStringSlice(t *testing.T, raw interface{}) []string {
	t.Helper()
	items, ok := raw.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T (%#v)", raw, raw)
	}
	out := make([]string, len(items))
	for i, item := range items {
		str, ok := item.(string)
		if !ok {
			t.Fatalf("expected string at index %d, got %T", i, item)
		}
		out[i] = str
	}
	return out
}

func TestRunConnectNewTargetsProjectScopedAndNonDestructiveMerge(t *testing.T) {
	tests := []struct {
		target     string
		configKey  string
		relPath    func(projectRoot, home string) string
		extraTop   map[string]interface{}
		otherEntry map[string]interface{}
	}{
		{
			target:     "cursor",
			configKey:  "mcpServers",
			relPath:    func(projectRoot, _ string) string { return filepath.Join(projectRoot, ".cursor", "mcp.json") },
			extraTop:   map[string]interface{}{"cursorSetting": true},
			otherEntry: map[string]interface{}{"command": "other-cursor", "args": []string{"--stdio"}},
		},
		{
			target:     "windsurf",
			configKey:  "mcpServers",
			relPath:    func(_, home string) string { return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json") },
			extraTop:   map[string]interface{}{"windsurfSetting": "keep"},
			otherEntry: map[string]interface{}{"command": "other-windsurf", "args": []string{"serve"}},
		},
		{
			target:     "zed",
			configKey:  "context_servers",
			relPath:    func(projectRoot, _ string) string { return filepath.Join(projectRoot, ".zed", "settings.json") },
			extraTop:   map[string]interface{}{"theme": "One Dark", "tab_size": float64(4)},
			otherEntry: map[string]interface{}{"command": "other-zed", "args": []string{"mcp"}},
		},
		{
			target:     "gemini",
			configKey:  "mcpServers",
			relPath:    func(projectRoot, _ string) string { return filepath.Join(projectRoot, ".gemini", "settings.json") },
			extraTop:   map[string]interface{}{"selectedAuthType": "oauth-personal"},
			otherEntry: map[string]interface{}{"command": "other-gemini", "args": []string{"run"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.target+"/create_and_print", func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatalf("create home dir: %v", err)
			}
			setTestHome(t, home)
			projectRoot := initTestProjectDir(t, filepath.Join(t.TempDir(), "repo"))
			configPath := tc.relPath(projectRoot, home)

			var printOut bytes.Buffer
			if err := runConnectWithOptions(context.Background(), projectRoot, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
				Target:    tc.target,
				PrintOnly: true,
			}, os.UserHomeDir, &printOut, io.Discard); err != nil {
				t.Fatalf("runConnect(%s, printOnly) error = %v", tc.target, err)
			}
			if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s --print unexpectedly created %s", tc.target, configPath)
			}
			var printed map[string]interface{}
			if err := json.Unmarshal(printOut.Bytes(), &printed); err != nil {
				t.Fatalf("unmarshal %s print output: %v", tc.target, err)
			}
			printedServers := printed[tc.configKey].(map[string]interface{})
			printedEntry := printedServers["rhizome-mcp"].(map[string]interface{})
			if _, hasType := printedEntry["type"]; hasType {
				t.Fatalf("%s entry unexpectedly included type field: %#v", tc.target, printedEntry)
			}

			if err := runConnectWithOptions(context.Background(), projectRoot, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
				Target: tc.target,
			}, os.UserHomeDir, io.Discard, io.Discard); err != nil {
				t.Fatalf("runConnect(%s) error = %v", tc.target, err)
			}
			written := readJSONMap(t, configPath)
			servers := written[tc.configKey].(map[string]interface{})
			entry := servers["rhizome-mcp"].(map[string]interface{})
			if entry["command"] != "/tmp/rhizome" {
				t.Fatalf("%s command = %v, want /tmp/rhizome", tc.target, entry["command"])
			}
			wantArgs := []string{"serve", "--project-root", projectRoot}
			if gotArgs := toStringSlice(t, entry["args"]); !slices.Equal(gotArgs, wantArgs) {
				t.Fatalf("%s args = %v, want %v", tc.target, gotArgs, wantArgs)
			}
		})

		t.Run(tc.target+"/non_destructive_merge", func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatalf("create home dir: %v", err)
			}
			setTestHome(t, home)
			projectRoot := initTestProjectDir(t, filepath.Join(t.TempDir(), "repo"))
			configPath := tc.relPath(projectRoot, home)
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatalf("create config parent dir: %v", err)
			}

			seed := map[string]interface{}{
				tc.configKey: map[string]interface{}{
					"existing-server": tc.otherEntry,
				},
			}
			for key, value := range tc.extraTop {
				seed[key] = value
			}
			seedBytes, err := json.MarshalIndent(seed, "", "  ")
			if err != nil {
				t.Fatalf("marshal seed config: %v", err)
			}
			if err := os.WriteFile(configPath, seedBytes, 0o644); err != nil {
				t.Fatalf("write seed config: %v", err)
			}

			if err := runConnectWithOptions(context.Background(), projectRoot, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
				Target: tc.target,
			}, os.UserHomeDir, io.Discard, io.Discard); err != nil {
				t.Fatalf("runConnect(%s) merge error = %v", tc.target, err)
			}

			merged := readJSONMap(t, configPath)
			for key, wantValue := range tc.extraTop {
				if merged[key] != wantValue {
					t.Fatalf("%s top-level key %q = %#v, want %#v", tc.target, key, merged[key], wantValue)
				}
			}
			servers := merged[tc.configKey].(map[string]interface{})
			if _, ok := servers["existing-server"]; !ok {
				t.Fatalf("%s did not preserve existing-server entry", tc.target)
			}
			if _, ok := servers["rhizome-mcp"]; !ok {
				t.Fatalf("%s did not add rhizome-mcp entry", tc.target)
			}
		})
	}
}

func TestRunConnectGlobalTargetsUseUserHomeDirAndSkipDiscovery(t *testing.T) {
	outsideDir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create home dir: %v", err)
	}
	setTestHome(t, home)

	fileTargets := []struct {
		target    string
		configKey string
		wantType  bool
		path      string
	}{
		{target: "claude", configKey: "mcpServers", wantType: true, path: filepath.Join(home, ".claude.json")},
		{target: "cursor", configKey: "mcpServers", wantType: false, path: filepath.Join(home, ".cursor", "mcp.json")},
		{target: "windsurf", configKey: "mcpServers", wantType: false, path: filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")},
		{target: "zed", configKey: "context_servers", wantType: false, path: filepath.Join(home, ".config", "zed", "settings.json")},
		{target: "gemini", configKey: "mcpServers", wantType: false, path: filepath.Join(home, ".gemini", "settings.json")},
	}

	for _, tc := range fileTargets {
		t.Run(tc.target, func(t *testing.T) {
			if err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
				Target: tc.target,
				Global: true,
			}, os.UserHomeDir, io.Discard, io.Discard); err != nil {
				t.Fatalf("runConnect(%s --global) error = %v", tc.target, err)
			}
			cfg := readJSONMap(t, tc.path)
			servers := cfg[tc.configKey].(map[string]interface{})
			entry := servers["rhizome-mcp"].(map[string]interface{})
			if tc.wantType && entry["type"] != "stdio" {
				t.Fatalf("%s global type = %v, want stdio", tc.target, entry["type"])
			}
			if gotArgs := toStringSlice(t, entry["args"]); !slices.Equal(gotArgs, []string{"serve"}) {
				t.Fatalf("%s global args = %v, want [serve]", tc.target, gotArgs)
			}
		})
	}

	t.Run("vscode_global_without_print_errors", func(t *testing.T) {
		err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
			Target: "vscode",
			Global: true,
		}, os.UserHomeDir, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "workspace-scoped") || !strings.Contains(err.Error(), "--print") {
			t.Fatalf("vscode --global error = %v, want workspace-scoped message naming --print", err)
		}
	})

	t.Run("vscode_global_with_print_succeeds", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
			Target:    "vscode",
			Global:    true,
			PrintOnly: true,
		}, os.UserHomeDir, &stdout, io.Discard); err != nil {
			t.Fatalf("vscode --global --print error = %v", err)
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &cfg); err != nil {
			t.Fatalf("unmarshal vscode global snippet: %v", err)
		}
		entry := cfg["servers"].(map[string]interface{})["rhizome-mcp"].(map[string]interface{})
		if gotArgs := toStringSlice(t, entry["args"]); !slices.Equal(gotArgs, []string{"serve"}) {
			t.Fatalf("vscode global args = %v, want [serve]", gotArgs)
		}
	})

	t.Run("codex_global_print", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
			Target:    "codex",
			Global:    true,
			PrintOnly: true,
		}, os.UserHomeDir, &stdout, io.Discard); err != nil {
			t.Fatalf("codex --global --print error = %v", err)
		}
		if !strings.Contains(stdout.String(), `args = ["serve"]`) {
			t.Fatalf("codex global output = %q, want args = [\"serve\"]", stdout.String())
		}
	})

	t.Run("json_global", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
			Target: "json",
			Global: true,
		}, os.UserHomeDir, &stdout, io.Discard); err != nil {
			t.Fatalf("json --global error = %v", err)
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &cfg); err != nil {
			t.Fatalf("unmarshal json global output: %v", err)
		}
		entry := cfg["mcpServers"].(map[string]interface{})["rhizome-mcp"].(map[string]interface{})
		if gotArgs := toStringSlice(t, entry["args"]); !slices.Equal(gotArgs, []string{"serve"}) {
			t.Fatalf("json global args = %v, want [serve]", gotArgs)
		}
	})

	t.Run("runCLI_global_uses_t_Setenv_HOME_when_PathInputs_HomeDir_empty", func(t *testing.T) {
		cliHome := filepath.Join(t.TempDir(), "cli-home")
		if err := os.MkdirAll(cliHome, 0o755); err != nil {
			t.Fatalf("create cli-home: %v", err)
		}
		setTestHome(t, cliHome)
		var stdout, stderr bytes.Buffer
		if err := runCLI(context.Background(), &config.Config{}, &stdout, &stderr, []string{"connect", "zed", "--global", "--profile", "agent"}, outsideDir, projectconfig.PathInputs{}); err != nil {
			t.Fatalf("runCLI(connect zed --global) error = %v", err)
		}
		cfg := readJSONMap(t, filepath.Join(cliHome, ".config", "zed", "settings.json"))
		entry := cfg["context_servers"].(map[string]interface{})["rhizome-mcp"].(map[string]interface{})
		wantArgs := []string{"serve", "--profile", "agent"}
		if gotArgs := toStringSlice(t, entry["args"]); !slices.Equal(gotArgs, wantArgs) {
			t.Fatalf("zed global profile args = %v, want %v", gotArgs, wantArgs)
		}
	})
}

func TestRunConnectProjectRootProfileAndToolsets(t *testing.T) {
	baseDir := t.TempDir()
	otherRepo := initTestProjectDir(t, filepath.Join(baseDir, "other-repo"))
	outsideDir := filepath.Join(baseDir, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	uninitSubdir := filepath.Join(otherRepo, "uninit-subdir")
	if err := os.MkdirAll(uninitSubdir, 0o755); err != nil {
		t.Fatalf("create uninit subdir: %v", err)
	}

	t.Run("explicit_absolute_project_root_with_profile", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
			Target:      "cursor",
			ProjectRoot: otherRepo,
			ToolProfile: "read-only",
		}, os.UserHomeDir, &stdout, io.Discard); err != nil {
			t.Fatalf("runConnect(--project-root) error = %v", err)
		}
		cfg := readJSONMap(t, filepath.Join(otherRepo, ".cursor", "mcp.json"))
		entry := cfg["mcpServers"].(map[string]interface{})["rhizome-mcp"].(map[string]interface{})
		wantArgs := []string{"serve", "--project-root", otherRepo, "--profile", "read-only"}
		if gotArgs := toStringSlice(t, entry["args"]); !slices.Equal(gotArgs, wantArgs) {
			t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
		}
	})

	t.Run("explicit_relative_project_root_with_toolsets", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
			Target:      "json",
			PrintOnly:   true,
			BareCommand: true,
			ProjectRoot: "../other-repo",
			Toolsets:    "issues,planning",
		}, os.UserHomeDir, &stdout, io.Discard); err != nil {
			t.Fatalf("runConnect(relative --project-root) error = %v", err)
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &cfg); err != nil {
			t.Fatalf("unmarshal json output: %v", err)
		}
		entry := cfg["mcpServers"].(map[string]interface{})["rhizome-mcp"].(map[string]interface{})
		if entry["command"] != "rhizome-mcp" {
			t.Fatalf("command = %v, want rhizome-mcp", entry["command"])
		}
		wantArgs := []string{"serve", "--project-root", otherRepo, "--toolsets", "issues,planning"}
		if gotArgs := toStringSlice(t, entry["args"]); !slices.Equal(gotArgs, wantArgs) {
			t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
		}
	})

	t.Run("rejects_uninitialized_explicit_project_root", func(t *testing.T) {
		err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, cliadapter.ConnectOptions{
			Target:      "claude",
			ProjectRoot: uninitSubdir,
		}, os.UserHomeDir, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "no rhizome-mcp project found") {
			t.Fatalf("error = %v, want clear uninitialized project error", err)
		}
	})

	t.Run("direct_validation_guards", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			opts cliadapter.ConnectOptions
			want string
		}{
			{
				name: "global_and_project_root",
				opts: cliadapter.ConnectOptions{Target: "claude", Global: true, ProjectRoot: otherRepo},
				want: "mutually exclusive",
			},
			{
				name: "profile_and_toolsets",
				opts: cliadapter.ConnectOptions{Target: "claude", Global: true, ToolProfile: "agent", Toolsets: "issues"},
				want: "mutually exclusive",
			},
			{
				name: "invalid_profile",
				opts: cliadapter.ConnectOptions{Target: "claude", Global: true, ToolProfile: "bogus"},
				want: "valid profiles",
			},
			{
				name: "invalid_toolsets",
				opts: cliadapter.ConnectOptions{Target: "claude", Global: true, Toolsets: "bogus"},
				want: "valid toolsets",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := runConnectWithOptions(context.Background(), outsideDir, "/tmp/rhizome", "", false, tc.opts, os.UserHomeDir, io.Discard, io.Discard)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want substring %q", err, tc.want)
				}
			})
		}
	})
}

func TestComposedServicesNilSafeAccessorsAndClose(t *testing.T) {
	var bundle *compose.Services
	if got := bundle.ProjectRef(); got != "" {
		t.Fatalf("nil ProjectRef() = %q, want empty", got)
	}
	services := bundle.Bundle()
	if services.IssueService != nil || services.ProjectService != nil || services.RelationService != nil || services.GraphService != nil || services.PlanningService != nil || services.CommentService != nil || services.DecisionService != nil || services.ActivityService != nil || services.SearchService != nil || services.ReviewService != nil || services.AttemptService != nil || services.SessionService != nil || services.WorkContextService != nil {
		t.Fatalf("nil Bundle() returned non-zero services: %#v", services)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatalf("nil Close() error = %v, want nil", err)
	}

	bundle = &compose.Services{}
	if got := bundle.ProjectRef(); got != "" {
		t.Fatalf("empty ProjectRef() = %q, want empty", got)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatalf("empty Close() error = %v, want nil", err)
	}
}
