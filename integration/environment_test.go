//go:build integration

// Package integration_test holds black-box integration tests for rhizome-mcp:
// each test builds and drives the real compiled binary (over stdio, HTTP, or
// as a plain subprocess), never the internal packages directly. The one
// exception, TestIntegrationHTTPTransportIsolatesSessions, needs unexported
// package-main internals and therefore lives in the repository root's
// integration_test.go instead (Go cannot split package main across
// directories); this package builds its own copy of the small amount of
// shared plumbing that test also needs.
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	command := exec.Command("go", "build", "-o", integrationBinary, "rhizome-mcp")
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

func (env integrationEnvironment) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.Command(integrationBinary, "--data-root", env.dataRoot, "serve")
	command.Dir = env.repository
	client := mcp.NewClient(&mcp.Implementation{Name: "rhizome-integration-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command:           command,
		TerminateDuration: integrationTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("connect to MCP server: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close MCP session: %v", err)
		}
	})
	return session
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

func runIntegrationCommandExpectingFailure(t *testing.T, repository string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, integrationBinary, args...)
	command.Dir = repository
	var stdoutBuf, stderrBuf bytes.Buffer
	command.Stdout = &stdoutBuf
	command.Stderr = &stderrBuf
	err = command.Run()
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded: stdout=%s", command.String(), stdoutBuf.String())
	}
	return stdoutBuf.String(), stderrBuf.String(), err
}

func assertDirectoryEntryNames(t *testing.T, path string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read directory %s: %v", path, err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if len(got) != len(wantSorted) {
		t.Fatalf("directory %s entries = %v, want %v", path, got, wantSorted)
	}
	for i := range got {
		if got[i] != wantSorted[i] {
			t.Fatalf("directory %s entries = %v, want %v", path, got, wantSorted)
		}
	}
}

func callIntegrationTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s protocol error: %v", name, err)
	}
	return result
}

func decodeIntegrationResult(t *testing.T, result *mcp.CallToolResult, destination any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode structured result %s: %v", data, err)
	}
}

func hasTool(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
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
