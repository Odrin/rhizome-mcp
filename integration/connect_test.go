//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationConnectClaudeCreatesConfig(t *testing.T) {
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
	if !ok || len(args) != 1 || args[0] != "serve" {
		t.Errorf("args = %v, want [serve]", rhizome["args"])
	}

	_ = output
}

func TestIntegrationConnectClaudePrintDoesNotWrite(t *testing.T) {
	env := newIntegrationEnvironment(t)

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "connect", "claude", "--print")

	mcpJSONPath := filepath.Join(env.repository, ".mcp.json")
	if _, err := os.Stat(mcpJSONPath); err == nil {
		t.Fatalf(".mcp.json should not exist after --print")
	}
}

func TestIntegrationConnectClaudeIdempotent(t *testing.T) {
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
}

func TestIntegrationConnectVSCodeIdempotent(t *testing.T) {
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
	if !strings.Contains(output, "args = [\"serve\"]") {
		t.Errorf("output doesn't contain args assignment")
	}
}

func TestIntegrationConnectJSON(t *testing.T) {
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

	if _, ok := config["mcpServers"]; !ok {
		t.Errorf("mcpServers key not found in output")
	}
}

func TestIntegrationConnectUnknownTarget(t *testing.T) {
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
