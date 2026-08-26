//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationConnectClaudeCreatesConfig(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	output := runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "claude")

	mcpJSONPath := filepath.Join(env.repository, ".mcp.json")
	if _, err := os.Stat(mcpJSONPath); err != nil {
		t.Fatalf(".mcp.json was not created: %v", err)
	}

	data, err := os.ReadFile(mcpJSONPath)
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}

	servers, ok := config["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers is not a map")
	}

	rhizome, ok := servers["rhizome-mcp"].(map[string]interface{})
	if !ok {
		t.Fatalf("rhizome-mcp entry is not a map")
	}

	if rhizome["type"] != "stdio" {
		t.Errorf("type = %v, want stdio", rhizome["type"])
	}

	command, ok := rhizome["command"].(string)
	if !ok || command == "" {
		t.Errorf("command is empty or not a string: %v", rhizome["command"])
	}

	if !filepath.IsAbs(command) {
		t.Errorf("command is not an absolute path: %s", command)
	}

	args, ok := rhizome["args"].([]interface{})
	if !ok || len(args) != 3 || args[0] != "serve" || args[1] != "--project-root" || args[2] != env.repository {
		t.Errorf("args = %v, want [serve --project-root %s]", rhizome["args"], env.repository)
	}

	_ = output
}

func TestIntegrationConnectClaudePrintDoesNotWrite(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "claude", "--print")

	mcpJSONPath := filepath.Join(env.repository, ".mcp.json")
	if _, err := os.Stat(mcpJSONPath); err == nil {
		t.Fatalf(".mcp.json should not exist after --print")
	}
}

func TestIntegrationConnectClaudeIdempotent(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "claude")

	mcpJSONPath := filepath.Join(env.repository, ".mcp.json")
	firstData, err := os.ReadFile(mcpJSONPath)
	if err != nil {
		t.Fatalf("read .mcp.json first time: %v", err)
	}

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "claude")

	secondData, err := os.ReadFile(mcpJSONPath)
	if err != nil {
		t.Fatalf("read .mcp.json second time: %v", err)
	}

	if !bytes.Equal(firstData, secondData) {
		t.Errorf("file changed on second run:\nfirst:\n%s\nsecond:\n%s", string(firstData), string(secondData))
	}
}

func TestIntegrationConnectClaudePreservesOtherEntries(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	mcpJSONPath := filepath.Join(env.repository, ".mcp.json")
	initialConfig := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"other-server": map[string]interface{}{
				"type":    "stdio",
				"command": "other",
				"args":    []string{"run"},
			},
		},
		"otherKey": "value",
	}
	initialData, _ := json.MarshalIndent(initialConfig, "", "  ")
	if err := os.WriteFile(mcpJSONPath, initialData, 0o644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "claude")

	data, err := os.ReadFile(mcpJSONPath)
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}

	if config["otherKey"] != "value" {
		t.Errorf("otherKey was not preserved")
	}

	servers := config["mcpServers"].(map[string]interface{})
	if _, ok := servers["other-server"]; !ok {
		t.Errorf("other-server entry was not preserved")
	}

	if _, ok := servers["rhizome-mcp"]; !ok {
		t.Errorf("rhizome-mcp entry was not added")
	}
}

func TestIntegrationConnectVSCodeCreatesConfig(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "vscode")

	mcpJSONPath := filepath.Join(env.repository, ".vscode", "mcp.json")
	if _, err := os.Stat(mcpJSONPath); err != nil {
		t.Fatalf(".vscode/mcp.json was not created: %v", err)
	}

	data, err := os.ReadFile(mcpJSONPath)
	if err != nil {
		t.Fatalf("read .vscode/mcp.json: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse .vscode/mcp.json: %v", err)
	}

	servers, ok := config["servers"].(map[string]interface{})
	if !ok {
		t.Fatalf("servers is not a map")
	}

	rhizome, ok := servers["rhizome-mcp"].(map[string]interface{})
	if !ok {
		t.Fatalf("rhizome-mcp entry is not a map")
	}

	command, ok := rhizome["command"].(string)
	if !ok || command == "" {
		t.Errorf("command is empty or not a string: %v", rhizome["command"])
	}

	if !filepath.IsAbs(command) {
		t.Errorf("command is not an absolute path: %s", command)
	}

	args, ok := rhizome["args"].([]interface{})
	if !ok || len(args) != 3 || args[0] != "serve" || args[1] != "--project-root" || args[2] != env.repository {
		t.Errorf("args = %v, want [serve --project-root %s]", rhizome["args"], env.repository)
	}
}

func TestIntegrationConnectVSCodeIdempotent(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "vscode")

	mcpJSONPath := filepath.Join(env.repository, ".vscode", "mcp.json")
	firstData, err := os.ReadFile(mcpJSONPath)
	if err != nil {
		t.Fatalf("read .vscode/mcp.json first time: %v", err)
	}

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "vscode")

	secondData, err := os.ReadFile(mcpJSONPath)
	if err != nil {
		t.Fatalf("read .vscode/mcp.json second time: %v", err)
	}

	if !bytes.Equal(firstData, secondData) {
		t.Errorf("file changed on second run")
	}
}

func TestIntegrationConnectCodexPrint(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, integrationBinary, "--data-root", env.dataRoot, "connect", "codex", "--print")
	command.Dir = env.repository
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		t.Fatalf("connect codex --print failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "[mcp_servers.rhizome-mcp]") {
		t.Errorf("output doesn't contain TOML section header")
	}
	if !strings.Contains(output, "command =") {
		t.Errorf("output doesn't contain command assignment")
	}
	wantArgs := fmt.Sprintf("args = [%q, %q, %q]", "serve", "--project-root", env.repository)
	if !strings.Contains(output, wantArgs) {
		t.Errorf("output = %s, want an args assignment containing %s (codex must agree with the other targets on pinning --project-root)", output, wantArgs)
	}
}

func TestIntegrationConnectJSON(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, integrationBinary, "--data-root", env.dataRoot, "connect", "json")
	command.Dir = env.repository
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		t.Fatalf("connect json failed: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &config); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	servers, ok := config["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("mcpServers key not found in output")
	}
	rhizome, ok := servers["rhizome-mcp"].(map[string]interface{})
	if !ok {
		t.Fatalf("rhizome-mcp entry is not a map")
	}
	args, ok := rhizome["args"].([]interface{})
	if !ok || len(args) != 3 || args[0] != "serve" || args[1] != "--project-root" || args[2] != env.repository {
		t.Errorf("args = %v, want [serve --project-root %s] (json must agree with the other targets on pinning --project-root)", rhizome["args"], env.repository)
	}
}

func TestIntegrationConnectUnknownTarget(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	_, stderr, err := runIntegrationCommandExpectingFailure(t, env.repository, "--data-root", env.dataRoot, "connect", "unknown")
	if err == nil {
		t.Fatalf("expected connect unknown to fail")
	}

	if !strings.Contains(stderr, "unsupported target") {
		t.Errorf("stderr doesn't mention unsupported target: %s", stderr)
	}

	for _, target := range []string{"claude", "codex", "vscode", "json"} {
		if !strings.Contains(stderr, target) {
			t.Errorf("stderr doesn't list supported target %s: %s", target, stderr)
		}
	}
}

// TestIntegrationConnectFromSubdirectoryUsesDiscoveredRoot is a regression
// test for ISSUE-206 AC1/AC4: connect must discover the actual project root
// (walking up from cwd) and write the config there, not at whatever
// subdirectory it was invoked from.
func TestIntegrationConnectFromSubdirectoryUsesDiscoveredRoot(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	subdirectory := filepath.Join(env.repository, "nested", "deeper")
	if err := os.MkdirAll(subdirectory, 0o755); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, integrationBinary, "--data-root", env.dataRoot, "connect", "claude")
	command.Dir = subdirectory
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("connect from subdirectory failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	mcpJSONPath := filepath.Join(env.repository, ".mcp.json")
	data, err := os.ReadFile(mcpJSONPath)
	if err != nil {
		t.Fatalf(".mcp.json was not written at the discovered root %s: %v", env.repository, err)
	}
	if _, err := os.Stat(filepath.Join(subdirectory, ".mcp.json")); err == nil {
		t.Fatalf(".mcp.json was unexpectedly written at the subdirectory %s, not the discovered root", subdirectory)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}
	servers := config["mcpServers"].(map[string]interface{})
	rhizome := servers["rhizome-mcp"].(map[string]interface{})
	args := rhizome["args"].([]interface{})
	if len(args) != 3 || args[1] != "--project-root" || args[2] != env.repository {
		t.Fatalf("args = %v, want --project-root pinned to the discovered root %s, not the subdirectory", args, env.repository)
	}
}

// TestIntegrationConnectOutsideProjectFailsClearly is a regression test for
// ISSUE-206 AC1: connect must fail with a clear, actionable error when no
// project identity is found at or above the starting directory, rather
// than silently writing a config for a project that does not exist.
func TestIntegrationConnectOutsideProjectFailsClearly(t *testing.T) {
	t.Parallel()
	outsideDir := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "data")

	_, stderr, err := runIntegrationCommandExpectingFailure(t, outsideDir, "--data-root", dataRoot, "connect", "claude")
	if err == nil {
		t.Fatal("expected connect outside any project to fail")
	}
	if !strings.Contains(stderr, "no rhizome-mcp project found") && !strings.Contains(stderr, "init") {
		t.Errorf("stderr = %q, want a clear message pointing at `rhizome-mcp init`", stderr)
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, ".mcp.json")); !os.IsNotExist(statErr) {
		t.Fatalf(".mcp.json stat error = %v, want not-exist (nothing should be written on failure)", statErr)
	}
}

// TestIntegrationConnectAllTargetsAgreeOnProjectRootPinning is a regression
// test for ISSUE-206 AC2/AC4: all four connect targets must pin
// --project-root to the same discovered root, using the same shared
// command-resolution logic, modulo each target's own envelope shape.
func TestIntegrationConnectAllTargetsAgreeOnProjectRootPinning(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	extractArgs := func(t *testing.T, target string) []interface{} {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()
		command := exec.CommandContext(ctx, integrationBinary, "--data-root", env.dataRoot, "connect", target, "--print")
		command.Dir = env.repository
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("connect %s --print failed: %v\nstderr:\n%s", target, err, stderr.String())
		}
		switch target {
		case "codex":
			output := stdout.String()
			wantArgs := fmt.Sprintf("args = [%q, %q, %q]", "serve", "--project-root", env.repository)
			if !strings.Contains(output, wantArgs) {
				t.Fatalf("codex output = %s, want %s", output, wantArgs)
			}
			return []interface{}{"serve", "--project-root", env.repository}
		case "vscode":
			var config map[string]interface{}
			if err := json.Unmarshal(stdout.Bytes(), &config); err != nil {
				t.Fatalf("parse vscode output: %v", err)
			}
			servers := config["servers"].(map[string]interface{})
			rhizome := servers["rhizome-mcp"].(map[string]interface{})
			return rhizome["args"].([]interface{})
		default:
			var config map[string]interface{}
			if err := json.Unmarshal(stdout.Bytes(), &config); err != nil {
				t.Fatalf("parse %s output: %v", target, err)
			}
			servers := config["mcpServers"].(map[string]interface{})
			rhizome := servers["rhizome-mcp"].(map[string]interface{})
			return rhizome["args"].([]interface{})
		}
	}

	want := []interface{}{"serve", "--project-root", env.repository}
	for _, target := range []string{"claude", "vscode", "json"} {
		got := extractArgs(t, target)
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Errorf("target %s args = %v, want %v", target, got, want)
		}
	}
	extractArgs(t, "codex") // asserts internally; codex's envelope isn't JSON so it can't share the []interface{} comparison above.
}

// TestIntegrationConnectEmitsNpxFormUnderLauncherEnvVar is a regression
// test for ISSUE-206 AC3/AC4: when launched via the npm wrapper
// (RHIZOME_MCP_NPX=1), connect must emit an "npx rhizome-mcp" command
// instead of this resolved binary's absolute path, which points into the
// npx cache and goes stale on eviction or a version bump.
func TestIntegrationConnectEmitsNpxFormUnderLauncherEnvVar(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, integrationBinary, "--data-root", env.dataRoot, "connect", "claude", "--print")
	command.Dir = env.repository
	command.Env = append(os.Environ(), "RHIZOME_MCP_NPX=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("connect under RHIZOME_MCP_NPX=1 failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var config map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &config); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	servers := config["mcpServers"].(map[string]interface{})
	rhizome := servers["rhizome-mcp"].(map[string]interface{})
	if rhizome["command"] != "npx" {
		t.Fatalf("command = %v, want npx", rhizome["command"])
	}
	args := rhizome["args"].([]interface{})
	want := []interface{}{"-y", "rhizome-mcp", "serve", "--project-root", env.repository}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for index := range want {
		if args[index] != want[index] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}
}
