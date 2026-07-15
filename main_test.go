package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/config"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/projectconfig"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestInitCreatesUsableDatabase(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot, err := projectconfig.ResolveDataRoot(pathInputs)
	if err != nil {
		t.Fatalf("resolve data root: %v", err)
	}
	var stdout, stderr bytes.Buffer

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"init"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	discovered, err := projectconfig.Discover(repoRoot)
	if err != nil {
		t.Fatalf("discover project after init: %v", err)
	}
	if discovered.Identity.ProjectID == "" {
		t.Fatal("expected initialized project ID")
	}
	if _, err := os.Stat(filepath.Join(repoRoot, projectconfig.IdentityFileName)); err != nil {
		t.Fatalf("expected identity file: %v", err)
	}

	project, err := projectruntime.OpenProject(ctx, projectruntime.Options{StartingPath: repoRoot, DataRoot: dataRoot, PathInputs: pathInputs, Clock: clock.RealClock{}, SQLite: sqlite.Options{}})
	if err != nil {
		t.Fatalf("open initialized project: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = project.Close(closeCtx)
	}()
	if _, err := os.Stat(project.DatabasePath); err != nil {
		t.Fatalf("expected database at %s: %v", project.DatabasePath, err)
	}
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "project", "info", "--format", "json"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("project info command with initialized data root failed: %v", err)
	}
}

func TestServeCommandUsesExplicitHandler(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	if _, err := projectconfig.ResolveDataRoot(pathInputs); err != nil {
		t.Fatalf("resolve data root: %v", err)
	}
	var stdout, stderr bytes.Buffer

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"init"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	called := false
	originalServeRunner := serveRunner
	serveRunner = func(ctx context.Context, cfg *config.Config, stderr io.Writer, bundle *composedServices) error {
		called = true
		return nil
	}
	defer func() { serveRunner = originalServeRunner }()

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"serve"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("serve command failed: %v", err)
	}
	if !called {
		t.Fatal("expected serve handler to be invoked")
	}
}
