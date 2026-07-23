//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rhizome-mcp/internal/projectconfig"
)

func TestIntegrationInitRejectsExistingInRepositoryDataRootThenRetrySucceeds(t *testing.T) {
	tempDir := t.TempDir()
	repository := filepath.Join(tempDir, "repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatalf("create test repository: %v", err)
	}
	badDataRoot := filepath.Join(repository, "data")
	if err := os.Mkdir(badDataRoot, 0o755); err != nil {
		t.Fatalf("create in-repository data root: %v", err)
	}

	_, stderr, runErr := runIntegrationCommandExpectingFailure(t, repository, "--data-root", badDataRoot, "init")
	if runErr == nil {
		t.Fatal("expected init to fail for an existing in-repository data root")
	}
	if !strings.Contains(stderr, "application data root must exist outside the repository") {
		t.Fatalf("stderr = %q, want outside-repository rejection", stderr)
	}
	assertDirectoryEntryNames(t, repository, []string{"data"})
	assertDirectoryEntryNames(t, badDataRoot, nil)

	goodDataRoot := filepath.Join(tempDir, "data")
	env := integrationEnvironment{repository: repository, dataRoot: goodDataRoot}
	runIntegrationCommand(t, env, "--data-root", goodDataRoot, "init")

	if _, err := os.Stat(filepath.Join(repository, projectconfig.IdentityFileName)); err != nil {
		t.Fatalf("expected identity file after retry: %v", err)
	}
	runIntegrationCommand(t, env, "--data-root", goodDataRoot, "doctor", "--format", "json")
}

func TestIntegrationInitRejectsNonexistentInRepositoryDataRootThenRetrySucceeds(t *testing.T) {
	tempDir := t.TempDir()
	repository := filepath.Join(tempDir, "repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatalf("create test repository: %v", err)
	}
	badDataRoot := filepath.Join(repository, "data")

	_, stderr, runErr := runIntegrationCommandExpectingFailure(t, repository, "--data-root", badDataRoot, "init")
	if runErr == nil {
		t.Fatal("expected init to fail for a nonexistent in-repository data root")
	}
	if !strings.Contains(stderr, "application data root must exist outside the repository") {
		t.Fatalf("stderr = %q, want outside-repository rejection", stderr)
	}
	assertDirectoryEntryNames(t, repository, nil)

	goodDataRoot := filepath.Join(tempDir, "data")
	env := integrationEnvironment{repository: repository, dataRoot: goodDataRoot}
	runIntegrationCommand(t, env, "--data-root", goodDataRoot, "init")

	if _, err := os.Stat(filepath.Join(repository, projectconfig.IdentityFileName)); err != nil {
		t.Fatalf("expected identity file after retry: %v", err)
	}
	runIntegrationCommand(t, env, "--data-root", goodDataRoot, "doctor", "--format", "json")
}
