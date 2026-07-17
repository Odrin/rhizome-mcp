package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/config"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
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

func TestDoctorCommandUsesCustomDataRoot(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	var stdout, stderr bytes.Buffer

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "init"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	project, err := projectruntime.OpenProject(ctx, projectruntime.Options{StartingPath: repoRoot, DataRoot: dataRoot, PathInputs: pathInputs, Clock: clock.RealClock{}, SQLite: sqlite.Options{}})
	if err != nil {
		t.Fatalf("reopen project: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = project.Close(closeCtx)
	}()

	stdout.Reset()
	stderr.Reset()
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "doctor", "--format", "json"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("doctor command failed: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "\"healthy\"") || !strings.Contains(output, "\"checks\"") {
		t.Fatalf("expected doctor JSON output, got %q", output)
	}
}

func TestBackupCommandCreatesValidatedBackup(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	var stdout, stderr bytes.Buffer

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "init"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
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

	if err := project.Database.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, "CREATE TABLE backup_test (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO backup_test(value) VALUES (?)", "backup-data")
		return err
	}); err != nil {
		t.Fatalf("write backup data: %v", err)
	}

	backupOutput := filepath.Join(tempDir, "backup.db")
	stdout.Reset()
	stderr.Reset()
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "backup", "--output", backupOutput}, repoRoot, pathInputs); err != nil {
		t.Fatalf("backup command failed: %v", err)
	}
	if !strings.Contains(stdout.String(), backupOutput) {
		t.Fatalf("expected backup output path in stdout, got %q", stdout.String())
	}

	backupDB, err := sqlite.Open(ctx, backupOutput, sqlite.Options{})
	if err != nil {
		t.Fatalf("open backup database: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = backupDB.Close(closeCtx)
	}()

	var count int
	var value string
	if err := backupDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM backup_test").Scan(&count); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT value FROM backup_test WHERE id = 1").Scan(&value)
	}); err != nil {
		t.Fatalf("read backup data: %v", err)
	}
	if count != 1 || value != "backup-data" {
		t.Fatalf("backup rows = %d/%q, want 1/backup-data", count, value)
	}
}

func TestServeWithoutHTTPAddressUsesStdioTransport(t *testing.T) {
	originalServeStdio := serveStdio
	called := false
	serveStdio = func(context.Context, *config.Config, io.Writer, *composedServices) error {
		called = true
		return nil
	}
	defer func() { serveStdio = originalServeStdio }()

	if err := runServe(context.Background(), &config.Config{}, io.Discard, nil); err != nil {
		t.Fatalf("serve without HTTP address failed: %v", err)
	}
	if !called {
		t.Fatal("expected stdio serve path to be used")
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

func TestMaintenanceCommandsUseCustomDataRoot(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	var stdout, stderr bytes.Buffer

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "init"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
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

	generator, err := ids.NewGenerator(clock.RealClock{}, rand.Reader)
	if err != nil {
		t.Fatalf("create generator: %v", err)
	}
	issueRepository, err := sqlite.NewIssueRepository(project.Database)
	if err != nil {
		t.Fatalf("create issue repository: %v", err)
	}
	attemptRepository, err := sqlite.NewAttemptRepository(project.Database)
	if err != nil {
		t.Fatalf("create attempt repository: %v", err)
	}
	issueService, err := application.NewIssueService(issueRepository, clock.RealClock{}, generator)
	if err != nil {
		t.Fatalf("create issue service: %v", err)
	}
	attemptService, err := application.NewAttemptService(attemptRepository, clock.RealClock{}, generator)
	if err != nil {
		t.Fatalf("create attempt service: %v", err)
	}
	created, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "maintenance issue", Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	claim, err := attemptService.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: created.Issue.DisplayID})
	if err != nil {
		t.Fatalf("claim issue: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "maintenance", "release-attempt", claim.Attempt.ID}, repoRoot, pathInputs); err != nil {
		t.Fatalf("maintenance release command failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "attempt_id") {
		t.Fatalf("expected release output, got %q", stdout.String())
	}

	reopenedProject, err := projectruntime.OpenProject(ctx, projectruntime.Options{StartingPath: repoRoot, DataRoot: dataRoot, PathInputs: pathInputs, Clock: clock.RealClock{}, SQLite: sqlite.Options{}})
	if err != nil {
		t.Fatalf("reopen project: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reopenedProject.Close(closeCtx)
	}()
	reopenedAttemptRepository, err := sqlite.NewAttemptRepository(reopenedProject.Database)
	if err != nil {
		t.Fatalf("create reopened attempt repository: %v", err)
	}
	reopenedAttemptService, err := application.NewAttemptService(reopenedAttemptRepository, clock.RealClock{}, generator)
	if err != nil {
		t.Fatalf("create reopened attempt service: %v", err)
	}
	if _, err := reopenedAttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: created.Issue.DisplayID}); err != nil {
		t.Fatalf("expected issue to become claimable again: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "maintenance", "rebuild-search-index"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("maintenance rebuild command failed: %v", err)
	}
	if stdout.String() != "search index rebuilt\n" {
		t.Fatalf("expected rebuild output, got %q", stdout.String())
	}

	var searchCount int
	if err := reopenedProject.Database.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM search_index`).Scan(&searchCount)
	}); err != nil {
		t.Fatalf("read search index count: %v", err)
	}
	if searchCount == 0 {
		t.Fatal("expected search index to contain rebuilt rows")
	}
}
