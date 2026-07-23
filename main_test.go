package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
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

func TestInitRejectsInRepositoryDataRootThenRetrySucceeds(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	badDataRoot := filepath.Join(repoRoot, "data")
	var stdout, stderr bytes.Buffer

	err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", badDataRoot, "init"}, repoRoot, pathInputs)
	if err == nil {
		t.Fatal("expected init to fail for an in-repository data root")
	}
	if !strings.Contains(err.Error(), "application data root must exist outside the repository") {
		t.Fatalf("init error = %v, want outside-repository rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, projectconfig.IdentityFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("identity file stat error = %v, want not exist", statErr)
	}
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		t.Fatalf("read repo root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("repo root entries after rejected init = %v, want none", entries)
	}

	goodDataRoot := filepath.Join(tempDir, "data")
	stdout.Reset()
	stderr.Reset()
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", goodDataRoot, "init"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("retry init command failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, projectconfig.IdentityFileName)); err != nil {
		t.Fatalf("expected identity file after retry: %v", err)
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

func TestVersionSubcommand(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}

	var stdout, stderr bytes.Buffer
	cfg := &config.Config{Version: "v1.2.3", VersionCommit: "abc1234", VersionDate: "2024-01-01T00:00:00Z"}

	if err := runCLI(ctx, cfg, &stdout, &stderr, []string{"version"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "v1.2.3") {
		t.Fatalf("expected version v1.2.3 in output, got %q", output)
	}
	if !strings.Contains(output, "abc1234") {
		t.Fatalf("expected commit abc1234 in output, got %q", output)
	}
	if !strings.Contains(output, "2024-01-01") {
		t.Fatalf("expected date 2024-01-01 in output, got %q", output)
	}
}

func TestVersionFlag(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}

	var stdout, stderr bytes.Buffer
	cfg := &config.Config{Version: "v2.0.0", VersionCommit: "def5678", VersionDate: "2024-02-01T00:00:00Z"}

	if err := runCLI(ctx, cfg, &stdout, &stderr, []string{"--version"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("--version flag failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "v2.0.0") {
		t.Fatalf("expected version v2.0.0 in output, got %q", output)
	}
}

func TestVersionShortFlag(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}

	var stdout, stderr bytes.Buffer
	cfg := &config.Config{Version: "v2.1.0", VersionCommit: "ghi9012", VersionDate: "2024-03-01T00:00:00Z"}

	if err := runCLI(ctx, cfg, &stdout, &stderr, []string{"-v"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("-v flag failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "v2.1.0") {
		t.Fatalf("expected version v2.1.0 in output, got %q", output)
	}
}

func TestDoctorCommandIncludesVersion(t *testing.T) {
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
	cfg := &config.Config{Version: "v1.5.0", VersionCommit: "jkl3456", VersionDate: "2024-04-01T00:00:00Z"}
	if err := runCLI(ctx, cfg, &stdout, &stderr, []string{"--data-root", dataRoot, "doctor", "--format", "json"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("doctor command failed: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "app_version") {
		t.Fatalf("expected app_version in doctor output, got %q", output)
	}
	if !strings.Contains(output, "v1.5.0") {
		t.Fatalf("expected version v1.5.0 in doctor output, got %q", output)
	}
}

func TestProjectInfoIncludesVersion(t *testing.T) {
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
	cfg := &config.Config{Version: "v1.6.0", VersionCommit: "mno7890", VersionDate: "2024-05-01T00:00:00Z"}
	if err := runCLI(ctx, cfg, &stdout, &stderr, []string{"--data-root", dataRoot, "project", "info", "--format", "json"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("project info command failed: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "app_version") {
		t.Fatalf("expected app_version in project info output, got %q", output)
	}
	if !strings.Contains(output, "v1.6.0") {
		t.Fatalf("expected version v1.6.0 in project info output, got %q", output)
	}
}

func TestComputeVersionInfoEnvVarOverride(t *testing.T) {
	// Test that VERSION env var has highest precedence
	ver, commit, date := computeVersionInfo("v1.2.3", "abc1234", "2024-01-01T00:00:00Z", "v2.0.0-env", nil, false)
	if ver != "v2.0.0-env" {
		t.Fatalf("expected env override v2.0.0-env, got %s", ver)
	}
	if commit != "abc1234" || date != "2024-01-01T00:00:00Z" {
		t.Fatalf("commit and date should use injected values when env overrides version")
	}
}

func TestComputeVersionInfoLdflagsInjection(t *testing.T) {
	// Test that ldflags-injected version is used when not "dev" and no env override
	ver, commit, date := computeVersionInfo("v1.2.3", "abc1234", "2024-01-01T00:00:00Z", "", nil, false)
	if ver != "v1.2.3" {
		t.Fatalf("expected ldflags version v1.2.3, got %s", ver)
	}
	if commit != "abc1234" || date != "2024-01-01T00:00:00Z" {
		t.Fatalf("expected ldflags commit and date, got %s/%s", commit, date)
	}
}

func TestComputeVersionInfoVCSFallback(t *testing.T) {
	// Test that VCS info from debug.BuildInfo is used when ldflags is "dev"
	buildInfo := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.5.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123456789abcdef"},
			{Key: "vcs.time", Value: "2024-06-15T10:30:45Z"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	ver, commit, date := computeVersionInfo("dev", "none", "unknown", "", buildInfo, true)
	if ver != "v1.5.0" {
		t.Fatalf("expected VCS version v1.5.0, got %s", ver)
	}
	if commit != "abc1234" { // shortened to 7 chars
		t.Fatalf("expected shortened commit abc1234, got %s", commit)
	}
	if date != "2024-06-15T10:30:45Z" {
		t.Fatalf("expected VCS date 2024-06-15T10:30:45Z, got %s", date)
	}
}

func TestComputeVersionInfoVCSDirtyFlag(t *testing.T) {
	// Test that vcs.modified=true appends -dirty to commit hash
	buildInfo := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.0.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "def987654321def987"},
			{Key: "vcs.time", Value: "2024-07-01T00:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	ver, commit, date := computeVersionInfo("dev", "none", "unknown", "", buildInfo, true)
	if ver != "v1.0.0" {
		t.Fatalf("expected VCS version v1.0.0, got %s", ver)
	}
	if commit != "def9876-dirty" {
		t.Fatalf("expected dirty commit def9876-dirty, got %s", commit)
	}
	if date != "2024-07-01T00:00:00Z" {
		t.Fatalf("expected VCS date 2024-07-01T00:00:00Z, got %s", date)
	}
}

func TestComputeVersionInfoDevFallback(t *testing.T) {
	// Test that "dev" is returned when nothing is available
	ver, commit, date := computeVersionInfo("dev", "none", "unknown", "", nil, false)
	if ver != "dev" {
		t.Fatalf("expected dev fallback, got %s", ver)
	}
	if commit != "none" || date != "unknown" {
		t.Fatalf("expected fallback commit/date, got %s/%s", commit, date)
	}
}

func TestComputeVersionInfoVCSWithNoRevision(t *testing.T) {
	// Test VCS fallback when revision is missing but other info exists
	buildInfo := &debug.BuildInfo{
		Main: debug.Module{Version: "v2.0.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.time", Value: "2024-08-01T12:00:00Z"},
		},
	}
	ver, commit, date := computeVersionInfo("dev", "none", "unknown", "", buildInfo, true)
	if ver != "v2.0.0" {
		t.Fatalf("expected version v2.0.0, got %s", ver)
	}
	// commit should remain as injected value since no vcs.revision
	if commit != "none" {
		t.Fatalf("expected injected commit none when no vcs.revision, got %s", commit)
	}
	if date != "2024-08-01T12:00:00Z" {
		t.Fatalf("expected VCS date, got %s", date)
	}
}
