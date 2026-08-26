package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/compose"
	"rhizome-mcp/internal/config"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
	"rhizome-mcp/internal/projectconfig"
	"rhizome-mcp/internal/projectrouting"
	projectruntime "rhizome-mcp/internal/runtime"
)

type boardURLTestListener struct {
	address net.Addr
}

func (listener boardURLTestListener) Accept() (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (listener boardURLTestListener) Close() error {
	return nil
}

func (listener boardURLTestListener) Addr() net.Addr {
	return listener.address
}

func TestBoardServeURLUsesListenerAddress(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		address net.Addr
		wantURL string
	}{
		{name: "ipv4", address: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43210}, wantURL: "http://127.0.0.1:43210/"},
		{name: "ipv6", address: &net.TCPAddr{IP: net.ParseIP("::1"), Port: 43210}, wantURL: "http://[::1]:43210/"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := boardServeURL(boardURLTestListener{address: testCase.address}); got != testCase.wantURL {
				t.Fatalf("boardServeURL() = %q, want %q", got, testCase.wantURL)
			}
		})
	}
}

func TestSearchCommandReportsInvalidFTSQuery(t *testing.T) {
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

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "search", "multi-project"}, repoRoot, pathInputs); err == nil {
		t.Fatal("expected invalid search query to fail")
	} else if !strings.Contains(err.Error(), "search query is invalid") {
		t.Fatalf("search command error = %v, want invalid FTS query", err)
	}
}

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
	serveStdio = func(context.Context, *config.Config, io.Writer, projectrouting.ProjectRouter) error {
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

func TestServeProjectRootPrecedenceUsesFlagEnvCwdAndShared(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	var stdout, stderr bytes.Buffer
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "init"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}
	discovered, err := projectconfig.Discover(projectRoot)
	if err != nil {
		t.Fatalf("discover initialized project: %v", err)
	}

	flagRoot := filepath.Join(tempDir, "flag-root")
	if err := os.MkdirAll(flagRoot, 0o755); err != nil {
		t.Fatalf("create flag root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(flagRoot, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}`), 0o644); err != nil {
		t.Fatalf("write flag identity: %v", err)
	}
	envRoot := filepath.Join(tempDir, "env-root")
	if err := os.MkdirAll(envRoot, 0o755); err != nil {
		t.Fatalf("create env root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`), 0o644); err != nil {
		t.Fatalf("write env identity: %v", err)
	}
	sharedRoot := filepath.Join(tempDir, "shared-root")
	if err := os.MkdirAll(sharedRoot, 0o755); err != nil {
		t.Fatalf("create shared root: %v", err)
	}

	for _, tc := range []struct {
		name        string
		args        []string
		env         string
		startingDir string
		wantProject string
		wantShared  bool
	}{
		{name: "flag precedence", args: []string{"serve", "--project-root", flagRoot}, wantProject: "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{name: "env precedence", args: []string{"serve"}, env: envRoot, wantProject: "01ARZ3NDEKTSV4RRFFQ69G5FAW"},
		{name: "cwd discovery", args: []string{"serve"}, startingDir: projectRoot, wantProject: discovered.Identity.ProjectID},
		{name: "shared fallback", args: []string{"serve"}, startingDir: sharedRoot, wantShared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured projectrouting.ProjectRouter
			originalServeRunner := serveRunner
			serveRunner = func(ctx context.Context, cfg *config.Config, stderr io.Writer, router projectrouting.ProjectRouter) error {
				captured = router
				return nil
			}
			defer func() { serveRunner = originalServeRunner }()

			startingDir := tc.startingDir
			if startingDir == "" {
				startingDir = projectRoot
			}
			if err := runCLI(ctx, &config.Config{ProjectRoot: tc.env}, &stdout, &stderr, tc.args, startingDir, pathInputs); err != nil {
				t.Fatalf("runCLI(%s) error = %v", tc.name, err)
			}
			if captured == nil {
				t.Fatal("expected serve runner to receive a router")
			}
			router, ok := captured.(*compose.Router)
			if !ok {
				t.Fatalf("router type = %T, want *compose.Router", captured)
			}
			if tc.wantShared {
				if router.DefaultProjectRef() != "" {
					t.Fatal("expected shared router to use a nil default bundle")
				}
				return
			}
			if router.DefaultProjectRef() == "" {
				t.Fatal("expected router to carry a default bundle")
			}
			if router.DefaultProjectRef() != tc.wantProject {
				t.Fatalf("router default ref = %q, want %q", router.DefaultProjectRef(), tc.wantProject)
			}
		})
	}
}

func TestServeProjectRootExplicitInvalidRootFailsWithoutFallback(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	var stdout, stderr bytes.Buffer
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "init"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}
	t.Setenv("RHIZOME_PROJECT_ROOT", filepath.Join(tempDir, "env-root"))

	originalServeRunner := serveRunner
	serveRunner = func(context.Context, *config.Config, io.Writer, projectrouting.ProjectRouter) error { return nil }
	defer func() { serveRunner = originalServeRunner }()

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"serve", "--project-root", filepath.Join(tempDir, "missing")}, projectRoot, pathInputs); err == nil {
		t.Fatal("expected explicit invalid project root to fail")
	}
}

func TestNewMCPServerWithSharedRouterHasNilDefault(t *testing.T) {
	router := compose.NewRouter(filepath.Join(t.TempDir(), "data"), clock.RealClock{}, sqlite.Options{}, nil)
	server, err := newMCPServer(&config.Config{ServerName: "test", Version: "v1", ToolProfile: "full"}, router)
	if err != nil {
		t.Fatalf("newMCPServer(shared) error = %v", err)
	}
	if server == nil {
		t.Fatal("expected shared server")
	}
}

func TestNewMCPServerWithDefaultRouterStillSupportsOmittedRef(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	if err := runCLI(ctx, &config.Config{}, io.Discard, io.Discard, []string{"--data-root", dataRoot, "init"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}
	project, err := projectruntime.OpenProject(ctx, projectruntime.Options{StartingPath: repoRoot, DataRoot: dataRoot, PathInputs: pathInputs, Clock: clock.RealClock{}, SQLite: sqlite.Options{}})
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = project.Close(closeCtx)
	}()
	bundle, err := compose.NewServices(project, project.Clock())
	if err != nil {
		t.Fatalf("compose services: %v", err)
	}
	router := compose.NewRouter(dataRoot, clock.RealClock{}, sqlite.Options{}, bundle)
	server, err := newMCPServer(&config.Config{ServerName: "test", Version: "v1", ToolProfile: "full"}, router)
	if err != nil {
		t.Fatalf("newMCPServer(default) error = %v", err)
	}
	if server == nil {
		t.Fatal("expected default server")
	}
}

// TestRouterComposedServicesUseInjectedClockNotWallClock is ISSUE-196's
// router-level regression test: it opens a project through the same
// composeFn (composeServicesFromExistingProject -> compose.NewServices) the
// router uses in production, with a fake clock fixed far from wall-clock
// time, and asserts both a lease's expiry and a freshly-minted ULID's
// embedded timestamp are computed from that fake clock -- not RealClock --
// so a regression that silently drops the injected clock anywhere in the
// composition chain fails this test instead of only surfacing once tests
// depend on deterministic time (ISSUE-178/179).
func TestRouterComposedServicesUseInjectedClockNotWallClock(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	if err := runCLI(ctx, &config.Config{}, io.Discard, io.Discard, []string{"--data-root", dataRoot, "init"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	// Far from wall-clock time: if anything in the composition chain falls
	// back to RealClock, the assertions below fail by years, not by a race-y
	// few milliseconds.
	fakeNow := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	fakeClock := clock.NewFakeClock(fakeNow)

	project, err := projectruntime.OpenProject(ctx, projectruntime.Options{StartingPath: repoRoot, DataRoot: dataRoot, PathInputs: pathInputs, Clock: fakeClock, SQLite: sqlite.Options{}})
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	projectID := project.ProjectID
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := project.Close(closeCtx); err != nil {
		t.Fatalf("close project: %v", err)
	}

	router := compose.NewRouter(dataRoot, fakeClock, sqlite.Options{}, nil)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = router.Close(closeCtx)
	}()
	lease, err := router.Acquire(ctx, &projectID)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() {
		if err := lease.Release(); err != nil {
			t.Errorf("Release() error = %v", err)
		}
	}()

	issue, err := lease.Services().IssueService.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "clock threading", Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	leaseSeconds := 900
	claim, err := lease.Services().AttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: issue.ID, LeaseSeconds: &leaseSeconds})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	wantExpiry := fakeNow.Add(time.Duration(leaseSeconds) * time.Second)
	if !claim.Attempt.LeaseExpiresAt.Equal(wantExpiry) {
		t.Fatalf("lease_expires_at = %v, want %v (fake-now + lease duration)", claim.Attempt.LeaseExpiresAt, wantExpiry)
	}

	parsed, err := ids.ParseStrict(claim.Attempt.ID)
	if err != nil {
		t.Fatalf("ParseStrict(attempt id) error = %v", err)
	}
	gotULIDTime := ulid.Time(parsed.Time()).UTC()
	if !gotULIDTime.Equal(fakeNow) {
		t.Fatalf("attempt id ULID timestamp = %v, want %v (fake clock)", gotULIDTime, fakeNow)
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
	var capturedRouter projectrouting.ProjectRouter
	originalServeRunner := serveRunner
	serveRunner = func(ctx context.Context, cfg *config.Config, stderr io.Writer, router projectrouting.ProjectRouter) error {
		called = true
		capturedRouter = router
		return nil
	}
	defer func() { serveRunner = originalServeRunner }()

	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"serve"}, repoRoot, pathInputs); err != nil {
		t.Fatalf("serve command failed: %v", err)
	}
	if !called {
		t.Fatal("expected serve handler to be invoked")
	}
	if _, err := capturedRouter.Acquire(ctx, nil); !errors.Is(err, compose.ErrRouterClosed) {
		t.Fatalf("router Acquire() after serve = %v, want %v", err, compose.ErrRouterClosed)
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

type fakeAttemptService struct {
	callCount  int
	errAfter   int
	callsMutex chan struct{}
}

func newFakeAttemptService(errAfter int) *fakeAttemptService {
	return &fakeAttemptService{
		callCount:  0,
		errAfter:   errAfter,
		callsMutex: make(chan struct{}, 1),
	}
}

func (s *fakeAttemptService) ExpireAttempts(ctx context.Context) (ports.ExpireAttemptsResult, error) {
	s.callsMutex <- struct{}{}
	defer func() { <-s.callsMutex }()
	s.callCount++
	if s.errAfter > 0 && s.callCount > s.errAfter {
		return ports.ExpireAttemptsResult{}, errors.New("simulated expiry error")
	}
	return ports.ExpireAttemptsResult{}, nil
}

func (s *fakeAttemptService) CallCount() int {
	s.callsMutex <- struct{}{}
	defer func() { <-s.callsMutex }()
	return s.callCount
}

func TestAttemptSweeperRunsOnStartAndOnTicker(t *testing.T) {
	fakeService := newFakeAttemptService(0)
	interval := 3 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := runAttemptSweeper(ctx, interval, fakeService)

	// Poll for at least 2 calls instead of sleeping
	deadline := time.Now().Add(100 * time.Millisecond)
	for fakeService.CallCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}

	cancel()
	<-done

	if fakeService.CallCount() < 2 {
		t.Fatalf("expected at least 2 calls (start + ticker), got %d", fakeService.CallCount())
	}
}

func TestAttemptSweeperContinuesAfterError(t *testing.T) {
	fakeService := newFakeAttemptService(1)
	interval := 3 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := runAttemptSweeper(ctx, interval, fakeService)

	// Poll for at least 3 calls to ensure we see the error case
	deadline := time.Now().Add(100 * time.Millisecond)
	for fakeService.CallCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}

	cancel()
	<-done

	if fakeService.CallCount() < 3 {
		t.Fatalf("expected at least 3 calls (to see 2nd error), got %d", fakeService.CallCount())
	}
}

func TestAttemptSweeperStopsCleanlyOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	interval := 3 * time.Millisecond

	done := runAttemptSweeper(ctx, interval, nil)
	time.Sleep(8 * time.Millisecond)
	cancel()

	// Use a timeout to ensure clean shutdown
	select {
	case <-done:
		// Good, the sweeper stopped
	case <-time.After(100 * time.Millisecond):
		t.Fatal("sweeper did not stop cleanly after context cancellation")
	}
}

func TestAttemptSweeperNoGoroutineLeakOnStop(t *testing.T) {
	initialGoroutines := goruntime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	interval := 3 * time.Millisecond

	done := runAttemptSweeper(ctx, interval, nil)
	time.Sleep(8 * time.Millisecond)
	cancel()

	// Wait for sweeper to finish
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for sweeper to stop")
	}

	// Small sleep to ensure goroutine is fully cleaned up
	time.Sleep(5 * time.Millisecond)
	finalGoroutines := goruntime.NumGoroutine()

	if finalGoroutines > initialGoroutines {
		t.Fatalf("goroutine leak detected: started %d, ended %d", initialGoroutines, finalGoroutines)
	}
}

// TestResolveServeProjectRootPrecedence is a focused unit test on
// resolveServeProjectRoot's own precedence logic (flag > env > cwd
// discovery), independent of the full CLI. resolveServeProjectRoot takes
// its inputs as plain parameters rather than reading the environment
// itself specifically so this is possible without t.Setenv.
func TestResolveServeProjectRootPrecedence(t *testing.T) {
	tempDir := t.TempDir()

	flagRoot := filepath.Join(tempDir, "flag-root")
	if err := os.MkdirAll(flagRoot, 0o755); err != nil {
		t.Fatalf("create flag root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(flagRoot, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}`), 0o644); err != nil {
		t.Fatalf("write flag identity: %v", err)
	}
	envRoot := filepath.Join(tempDir, "env-root")
	if err := os.MkdirAll(envRoot, 0o755); err != nil {
		t.Fatalf("create env root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`), 0o644); err != nil {
		t.Fatalf("write env identity: %v", err)
	}
	cwdRoot := filepath.Join(tempDir, "cwd-root")
	if err := os.MkdirAll(cwdRoot, 0o755); err != nil {
		t.Fatalf("create cwd root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdRoot, projectconfig.IdentityFileName), []byte(`{"version":1,"project_id":"01ARZ3NDEKTSV4RRFFQ69G5FAX"}`), 0o644); err != nil {
		t.Fatalf("write cwd identity: %v", err)
	}
	sharedRoot := filepath.Join(tempDir, "shared-root")
	if err := os.MkdirAll(sharedRoot, 0o755); err != nil {
		t.Fatalf("create shared root: %v", err)
	}

	t.Run("flag wins over env and cwd", func(t *testing.T) {
		root, shared, err := resolveServeProjectRoot(flagRoot, envRoot, cwdRoot)
		if err != nil {
			t.Fatalf("resolveServeProjectRoot() error = %v", err)
		}
		if shared {
			t.Fatal("shared = true, want false")
		}
		resolved, err := projectconfig.Discover(root)
		if err != nil {
			t.Fatalf("Discover(%q) error = %v", root, err)
		}
		if resolved.Identity.ProjectID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
			t.Fatalf("project id = %q, want the flag root's identity", resolved.Identity.ProjectID)
		}
	})

	t.Run("env wins over cwd when flag is empty", func(t *testing.T) {
		root, shared, err := resolveServeProjectRoot("", envRoot, cwdRoot)
		if err != nil {
			t.Fatalf("resolveServeProjectRoot() error = %v", err)
		}
		if shared {
			t.Fatal("shared = true, want false")
		}
		resolved, err := projectconfig.Discover(root)
		if err != nil {
			t.Fatalf("Discover(%q) error = %v", root, err)
		}
		if resolved.Identity.ProjectID != "01ARZ3NDEKTSV4RRFFQ69G5FAW" {
			t.Fatalf("project id = %q, want the env root's identity", resolved.Identity.ProjectID)
		}
	})

	t.Run("falls back to cwd discovery when flag and env are both empty", func(t *testing.T) {
		root, shared, err := resolveServeProjectRoot("", "", cwdRoot)
		if err != nil {
			t.Fatalf("resolveServeProjectRoot() error = %v", err)
		}
		if shared {
			t.Fatal("shared = true, want false")
		}
		resolved, err := projectconfig.Discover(root)
		if err != nil {
			t.Fatalf("Discover(%q) error = %v", root, err)
		}
		if resolved.Identity.ProjectID != "01ARZ3NDEKTSV4RRFFQ69G5FAX" {
			t.Fatalf("project id = %q, want the cwd root's identity", resolved.Identity.ProjectID)
		}
	})

	t.Run("reports shared fallback when nothing is found", func(t *testing.T) {
		root, shared, err := resolveServeProjectRoot("", "", sharedRoot)
		if err != nil {
			t.Fatalf("resolveServeProjectRoot() error = %v", err)
		}
		if !shared {
			t.Fatal("shared = false, want true")
		}
		if root != "" {
			t.Fatalf("root = %q, want empty for the shared fallback", root)
		}
	})
}

// TestServeWarnsWhenTransportOrProfileSelectedFromEnvironment is a
// regression test for ISSUE-205 AC3: a bare `serve` (no --http-address /
// --profile flag) must warn on stderr when the transport or tool profile
// it is about to use came from the environment, since HTTP transport
// activating silently from an inherited legacy HTTP_ADDRESS was the
// original bug (docs/08).
func TestServeWarnsWhenTransportOrProfileSelectedFromEnvironment(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	var stdout, stderr bytes.Buffer
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "init"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	originalServeRunner := serveRunner
	serveRunner = func(context.Context, *config.Config, io.Writer, projectrouting.ProjectRouter) error { return nil }
	defer func() { serveRunner = originalServeRunner }()

	stderr.Reset()
	envConfig := &config.Config{HTTPAddress: "127.0.0.1:0", HTTPAddressFromEnv: true, ToolProfile: "read-only", ToolProfileFromEnv: true}
	if err := runCLI(ctx, envConfig, &stdout, &stderr, []string{"serve"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("serve command failed: %v", err)
	}
	if !strings.Contains(stderr.String(), "HTTP transport selected via environment variable") {
		t.Fatalf("stderr = %q, want an HTTP transport environment-selection warning", stderr.String())
	}
	if !strings.Contains(stderr.String(), "tool profile") {
		t.Fatalf("stderr = %q, want a tool profile environment-selection warning", stderr.String())
	}

	stderr.Reset()
	flagConfig := &config.Config{HTTPAddress: "127.0.0.1:0", HTTPAddressFromEnv: true, ToolProfile: "read-only", ToolProfileFromEnv: true}
	if err := runCLI(ctx, flagConfig, &stdout, &stderr, []string{"serve", "--http-address", "127.0.0.1:0", "--profile", "read-only"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("serve command failed: %v", err)
	}
	if strings.Contains(stderr.String(), "selected via environment variable") {
		t.Fatalf("stderr = %q, want no environment-selection warning when flags are explicit", stderr.String())
	}
}

// TestServeWarnsWhenToolsetsSelectedFromEnvironment extends the ISSUE-205
// convention to RHIZOME_TOOLSETS: a bare `serve` must warn on stderr when
// the toolset selection it is about to use came from the environment, and
// stay silent when --toolsets was passed explicitly.
func TestServeWarnsWhenToolsetsSelectedFromEnvironment(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	var stdout, stderr bytes.Buffer
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "init"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	originalServeRunner := serveRunner
	serveRunner = func(context.Context, *config.Config, io.Writer, projectrouting.ProjectRouter) error { return nil }
	defer func() { serveRunner = originalServeRunner }()

	stderr.Reset()
	envConfig := &config.Config{Toolsets: "issues,planning", ToolsetsFromEnv: true}
	if err := runCLI(ctx, envConfig, &stdout, &stderr, []string{"serve"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("serve command failed: %v", err)
	}
	if !strings.Contains(stderr.String(), "toolsets \"issues,planning\" selected via environment variable") {
		t.Fatalf("stderr = %q, want a toolsets environment-selection warning", stderr.String())
	}

	stderr.Reset()
	flagConfig := &config.Config{Toolsets: "issues,planning", ToolsetsFromEnv: true}
	if err := runCLI(ctx, flagConfig, &stdout, &stderr, []string{"serve", "--toolsets", "issues,planning"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("serve command failed: %v", err)
	}
	if strings.Contains(stderr.String(), "selected via environment variable") {
		t.Fatalf("stderr = %q, want no environment-selection warning when --toolsets is explicit", stderr.String())
	}
}

// TestServeFailsWhenEnvironmentProfileMeetsToolsetsFlag drives the real,
// unstubbed serve path: an environment-selected tool profile combined with
// an explicit --toolsets flag has no defined precedence and must fail
// startup before any transport opens (the mutual-exclusion check in
// mcpadapter.NewServer), rather than silently preferring either input.
func TestServeFailsWhenEnvironmentProfileMeetsToolsetsFlag(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("create repo root: %v", err)
	}
	pathInputs := projectconfig.PathInputs{GOOS: "linux", HomeDir: tempDir, XDGDataHome: tempDir}
	dataRoot := filepath.Join(tempDir, "data")
	var stdout, stderr bytes.Buffer
	if err := runCLI(ctx, &config.Config{}, &stdout, &stderr, []string{"--data-root", dataRoot, "init"}, projectRoot, pathInputs); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	envConfig := &config.Config{ServerName: "test", Version: "v1", ToolProfile: "agent", ToolProfileFromEnv: true}
	err := runCLI(ctx, envConfig, &stdout, &stderr, []string{"--data-root", dataRoot, "serve", "--toolsets", "issues"}, projectRoot, pathInputs)
	if err == nil {
		t.Fatal("serve unexpectedly started with an environment profile and a --toolsets flag")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("serve error = %v, want a mutually-exclusive message", err)
	}
}

// TestInternalPackagesDoNotImportConfig is a regression test for ISSUE-205
// AC4: internal/config is main's own external-input loader; no package
// under internal/ may import it (each internal package that needs an
// external input, like internal/runtime's HTTP address, must receive it as
// a plain parameter from main instead, so it stays testable without
// mutating the process environment or depending on this repo's specific
// environment-variable names).
func TestInternalPackagesDoNotImportConfig(t *testing.T) {
	const forbiddenImport = "rhizome-mcp/internal/config"
	var offenders []string
	err := filepath.WalkDir("internal", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if path == filepath.Join("internal", "config", "config.go") || path == filepath.Join("internal", "config", "config_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, imported := range file.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			if importPath == forbiddenImport {
				offenders = append(offenders, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	if len(offenders) != 0 {
		t.Fatalf("packages under internal/ importing %s: %v", forbiddenImport, offenders)
	}
}
