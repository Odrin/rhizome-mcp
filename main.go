package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	cliadapter "rhizome-mcp/internal/adapters/cli"
	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/compose"
	"rhizome-mcp/internal/config"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/inventory"
	"rhizome-mcp/internal/ports"
	"rhizome-mcp/internal/projectconfig"
	"rhizome-mcp/internal/projectrouting"
	projectruntime "rhizome-mcp/internal/runtime"
)

const attemptCleanupInterval = time.Minute

type attemptExpirer interface {
	ExpireAttempts(ctx context.Context) (ports.ExpireAttemptsResult, error)
}

func runAttemptSweeper(ctx context.Context, interval time.Duration, expirer attemptExpirer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if expirer != nil {
				if _, err := expirer.ExpireAttempts(ctx); err != nil && ctx.Err() == nil {
					slog.Error("attempt expiry cleanup failed", "error", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

// Version information is injected at build time via ldflags.
// If not injected (e.g., in local builds), fallback values are used.
var (
	version = "dev"     // injected via -X main.version=...
	commit  = "none"    // injected via -X main.commit=...
	date    = "unknown" // injected via -X main.date=...
)

var (
	initRunner  = runInit
	serveRunner = runServe
	serveStdio  = runServeStdio
	serveHTTP   = runServeHTTP
)

// computeVersionInfo computes version, commit, and date with the following precedence:
// 1. VERSION environment variable (if set) - allows runtime override
// 2. ldflags-injected version (if not "dev")
// 3. git VCS info from build info
// 4. "dev" fallback if nothing else is available
//
// This is a pure function that does not mutate globals. It is used by resolveVersion()
// and can be called directly from tests with injected build info.
func computeVersionInfo(injectedVersion, injectedCommit, injectedDate, envVersion string, buildInfo *debug.BuildInfo, buildInfoOK bool) (string, string, string) {
	// Precedence 1: VERSION env var (highest)
	if envVersion != "" {
		return envVersion, injectedCommit, injectedDate
	}

	// Precedence 2: ldflags-injected version
	if injectedVersion != "dev" {
		return injectedVersion, injectedCommit, injectedDate
	}

	// Precedence 3: fallback to runtime/debug.ReadBuildInfo() for git VCS info
	if buildInfoOK && buildInfo != nil {
		var vcsRev, vcsTime, vcsModified string
		for _, setting := range buildInfo.Settings {
			switch setting.Key {
			case "vcs.revision":
				vcsRev = setting.Value
			case "vcs.time":
				vcsTime = setting.Value
			case "vcs.modified":
				vcsModified = setting.Value
			}
		}
		// Use module version as base if available
		moduleVersion := buildInfo.Main.Version
		if moduleVersion == "" {
			moduleVersion = "dev"
		}
		// Compute commit from git info
		resultCommit := injectedCommit
		if vcsRev != "" {
			shortRev := vcsRev
			if len(shortRev) > 7 {
				shortRev = shortRev[:7]
			}
			resultCommit = shortRev
			if vcsModified == "true" {
				resultCommit += "-dirty"
			}
		}
		// Compute date from git info
		resultDate := injectedDate
		if vcsTime != "" {
			resultDate = vcsTime
		}
		return moduleVersion, resultCommit, resultDate
	}

	// Precedence 4: fallback to "dev"
	return "dev", injectedCommit, injectedDate
}

// resolveVersion determines the effective version string by reading package-level
// version variables, environment, and build info, and returns the resolved values.
// It does not mutate any globals.
func resolveVersion() (string, string, string) {
	info, ok := debug.ReadBuildInfo()
	return computeVersionInfo(version, commit, date, os.Getenv("VERSION"), info, ok)
}

// formatVersionOutput returns a formatted version string for display.
func formatVersionOutput(version, commit, date string) string {
	return fmt.Sprintf("rhizome-mcp %s (commit %s, built %s)", version, commit, date)
}

func main() {
	resolvedVersion, resolvedCommit, resolvedDate := resolveVersion()
	cfg, warnings := config.Load(os.Getenv)
	cfg.Version = resolvedVersion
	cfg.VersionCommit = resolvedCommit
	cfg.VersionDate = resolvedDate
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, warning)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startingPath, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	pathInputs := projectconfig.PathInputs{
		GOOS:         goruntime.GOOS,
		HomeDir:      homeDir,
		XDGDataHome:  cfg.XDGDataHome,
		LocalAppData: cfg.LocalAppData,
	}

	if err := runCLI(ctx, cfg, os.Stdout, os.Stderr, os.Args[1:], startingPath, pathInputs); err != nil {
		// A usage mistake, a failed doctor check and a runtime failure are
		// different things to a script, so they get different exit codes.
		// cliadapter.ExitCodeFor owns the mapping (docs/05 section 14).
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(cliadapter.ExitCodeFor(err))
	}
}

func runCLI(ctx context.Context, cfg *config.Config, stdout, stderr io.Writer, args []string, startingPath string, pathInputs projectconfig.PathInputs) error {
	// Handle version subcommand and --version/--help flags early (before project initialization)
	if len(args) > 0 && args[0] == "version" {
		versionStr := formatVersionOutput(cfg.Version, cfg.VersionCommit, cfg.VersionDate)
		fmt.Fprintln(stdout, versionStr)
		return nil
	}
	for _, arg := range args {
		if arg == "--version" || arg == "-v" {
			versionStr := formatVersionOutput(cfg.Version, cfg.VersionCommit, cfg.VersionDate)
			fmt.Fprintln(stdout, versionStr)
			return nil
		}
	}

	var err error
	args, dataRootOverride, err := extractDataRootOption(args)
	if err != nil {
		return err
	}

	var bundle *compose.Services
	var project *projectruntime.Project
	var router projectrouting.ProjectRouter
	var serveProjectRoot string
	var serveShared bool

	initHandler := func(ctx context.Context, dataRoot string) error {
		if dataRootOverride != "" && dataRoot != "" {
			return errors.New("data root may only be specified once")
		}
		if dataRoot == "" {
			dataRoot = dataRootOverride
		}
		return initRunner(ctx, startingPath, pathInputs, dataRoot, stdout)
	}
	args, serveProjectRootOverride, err := extractServeProjectRootOption(args)
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "serve" {
		serveProjectRoot, serveShared, err = resolveServeProjectRoot(serveProjectRootOverride, cfg.ProjectRoot, startingPath)
		if err != nil {
			return err
		}
	}

	serveHandler := func(ctx context.Context, opts cliadapter.ServeOptions) (err error) {
		if opts.HTTPAddress != "" {
			cfg.HTTPAddress = opts.HTTPAddress
		} else if cfg.HTTPAddressFromEnv {
			fmt.Fprintf(stderr, "warning: HTTP transport selected via environment variable (address %q); pass --http-address explicitly to make this intentional\n", cfg.HTTPAddress)
		}
		if opts.ToolProfile != "" {
			cfg.ToolProfile = opts.ToolProfile
		} else if cfg.ToolProfileFromEnv {
			fmt.Fprintf(stderr, "warning: tool profile %q selected via environment variable; pass --profile explicitly to make this intentional\n", cfg.ToolProfile)
		}
		if opts.Toolsets != "" {
			cfg.Toolsets = opts.Toolsets
		} else if cfg.ToolsetsFromEnv {
			fmt.Fprintf(stderr, "warning: toolsets %q selected via environment variable; pass --toolsets explicitly to make this intentional\n", cfg.Toolsets)
		}
		if opts.ContentMode != "" {
			cfg.ContentMode = opts.ContentMode
		} else if cfg.ContentModeFromEnv {
			fmt.Fprintf(stderr, "warning: content mode %q selected via environment variable; pass --content-mode explicitly to make this intentional\n", cfg.ContentMode)
		}
		if router == nil {
			dataRoot, dataRootErr := resolveDataRoot(pathInputs, dataRootOverride)
			if dataRootErr != nil {
				return dataRootErr
			}
			if serveShared {
				router = compose.NewRouter(dataRoot, clock.RealClock{}, sqlite.Options{}, nil)
			} else {
				composeRoot := startingPath
				if serveProjectRoot != "" {
					composeRoot = serveProjectRoot
				}
				bundle, project, err = compose.Open(ctx, composeRoot, pathInputs, dataRootOverride)
				if err != nil {
					return err
				}
				router = compose.NewRouter(dataRoot, clock.RealClock{}, sqlite.Options{}, bundle)
			}
		}
		defer func() {
			if closer, ok := router.(interface{ Close(context.Context) error }); ok {
				closeCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 5*time.Second)
				defer cancel()
				err = errors.Join(err, closer.Close(closeCtx))
			}
		}()
		return serveRunner(ctx, cfg, stderr, router)
	}
	boardServeHandler := func(ctx context.Context, httpAddress string, stdoutWriter io.Writer) error {
		if httpAddress == "" {
			httpAddress = "127.0.0.1:0"
		}
		cfg.HTTPAddress = httpAddress
		if bundle == nil {
			bundle, project, err = compose.Open(ctx, startingPath, pathInputs, dataRootOverride)
			if err != nil {
				return err
			}
		}
		b := bundle.Bundle()
		if bundle == nil || b.BoardService == nil || b.IssueDetailService == nil {
			return errors.New("board service is not configured")
		}
		return runBoardServe(ctx, cfg, stdoutWriter, boardServeService{boardService: b.BoardService, issueDetailService: b.IssueDetailService, searchService: b.SearchService})
	}
	backupHandler := func(ctx context.Context, output string) (cliadapter.BackupReport, error) {
		if project == nil {
			return cliadapter.BackupReport{}, errors.New("project is not open")
		}
		report, err := project.Backup(ctx, output)
		if err != nil {
			return cliadapter.BackupReport{}, err
		}
		return cliadapter.BackupReport{OutputPath: report.OutputPath, SchemaVersion: report.SchemaVersion}, nil
	}
	doctorHandler := func(ctx context.Context, full bool) (cliadapter.DoctorReport, error) {
		if project == nil {
			return cliadapter.DoctorReport{}, errors.New("project is not open")
		}
		report, err := project.Doctor(ctx, projectruntime.DoctorOptions{Full: full, HTTPAddress: cfg.HTTPAddress})
		return doctorReportFromRuntime(report, cfg.Version), err
	}
	connectHandler := func(ctx context.Context, opts cliadapter.ConnectOptions) error {
		exePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("determine binary path: %w", err)
		}
		realPath, err := filepath.EvalSymlinks(exePath)
		if err != nil {
			return fmt.Errorf("resolve binary path: %w", err)
		}
		npxLaunched := os.Getenv("RHIZOME_MCP_NPX") == "1"
		homeDirFn := os.UserHomeDir
		if pathInputs.HomeDir != "" {
			fixedHome := pathInputs.HomeDir
			homeDirFn = func() (string, error) { return fixedHome, nil }
		}
		return runConnectWithOptions(ctx, startingPath, realPath, dataRootOverride, npxLaunched, opts, homeDirFn, stdout, stderr)
	}

	// Which commands need an open project is declared once, in the CLI
	// command table, instead of in a literal list here that had to be kept in
	// sync by hand.
	if cliadapter.NeedsProject(args) {
		bundle, project, err = compose.Open(ctx, startingPath, pathInputs, dataRootOverride)
		if err != nil {
			return err
		}
		defer func() {
			if project != nil {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := project.Close(closeCtx); err != nil {
					slog.Error("project close failed", "error", err)
				}
			}
		}()
	}

	var services cliadapter.Services
	if bundle != nil {
		b := bundle.Bundle()
		services = cliadapter.Services{
			ProjectService:     b.ProjectService,
			IssueService:       b.IssueService,
			SearchService:      b.SearchService,
			GraphService:       b.GraphService,
			MaintenanceService: b.MaintenanceService,
			BoardService:       b.BoardService,
		}
	}

	adapter := cliadapter.New(services, stdout, stderr, initHandler, serveHandler)
	adapter.SetBoardServeHandler(boardServeHandler)
	adapter.SetBackupHandler(backupHandler)
	adapter.SetIssueTimestampNormalizationHandler(func(ctx context.Context, backupPath string) (cliadapter.IssueTimestampNormalizationReport, error) {
		if project == nil {
			return cliadapter.IssueTimestampNormalizationReport{}, errors.New("project is not open")
		}
		report, err := project.NormalizeIssueTimestamps(ctx, backupPath)
		return cliadapter.IssueTimestampNormalizationReport{BackupPath: report.BackupPath, NormalizedCount: report.NormalizedCount}, err
	})
	adapter.SetDoctorHandler(doctorHandler)
	adapter.SetConnectHandler(connectHandler)
	adapter.SetProjectsListHandler(func(ctx context.Context, _ string) (inventory.Result, error) {
		dataRoot := dataRootOverride
		if dataRoot == "" {
			resolved, err := projectconfig.ResolveDataRoot(pathInputs)
			if err != nil {
				return inventory.Result{}, err
			}
			dataRoot = resolved
		}
		return inventory.List(dataRoot, pathInputs)
	})
	adapter.SetProjectsMigrateHandler(func(ctx context.Context, projectID string) error {
		dataRoot := dataRootOverride
		if dataRoot == "" {
			resolved, err := projectconfig.ResolveDataRoot(pathInputs)
			if err != nil {
				return err
			}
			dataRoot = resolved
		}
		project, err := projectruntime.MigrateExistingProject(ctx, projectID, dataRoot, clock.RealClock{}, sqlite.Options{})
		if err != nil {
			return err
		}
		return project.Close(ctx)
	})
	adapter.SetAppVersion(cfg.Version)
	return adapter.Run(ctx, args)
}

func extractDataRootOption(args []string) ([]string, string, error) {
	remaining := make([]string, 0, len(args))
	var dataRoot string
	for index := 0; index < len(args); index++ {
		value := args[index]
		if value == "--data-root" {
			if index+1 >= len(args) {
				return nil, "", errors.New("data root requires a path")
			}
			if dataRoot != "" {
				return nil, "", errors.New("data root may only be specified once")
			}
			dataRoot = args[index+1]
			index++
			continue
		}
		if strings.HasPrefix(value, "--data-root=") {
			if dataRoot != "" {
				return nil, "", errors.New("data root may only be specified once")
			}
			dataRoot = strings.TrimPrefix(value, "--data-root=")
			if dataRoot == "" {
				return nil, "", errors.New("data root requires a path")
			}
			continue
		}
		remaining = append(remaining, value)
	}
	return remaining, dataRoot, nil
}

func extractServeProjectRootOption(args []string) ([]string, string, error) {
	if len(args) == 0 || args[0] != "serve" {
		return args, "", nil
	}
	remaining := make([]string, 0, len(args))
	var projectRoot string
	for index := 1; index < len(args); index++ {
		value := args[index]
		if value == "--project-root" {
			if index+1 >= len(args) {
				return nil, "", errors.New("project root requires a path")
			}
			if projectRoot != "" {
				return nil, "", errors.New("project root may only be specified once")
			}
			projectRoot = args[index+1]
			index++
			continue
		}
		if strings.HasPrefix(value, "--project-root=") {
			if projectRoot != "" {
				return nil, "", errors.New("project root may only be specified once")
			}
			projectRoot = strings.TrimPrefix(value, "--project-root=")
			if projectRoot == "" {
				return nil, "", errors.New("project root requires a path")
			}
			continue
		}
		remaining = append(remaining, value)
	}
	remaining = append([]string{"serve"}, remaining...)
	return remaining, projectRoot, nil
}

// resolveServeProjectRoot is a pure function of its three inputs (no direct
// environment access) so precedence -- flag override, then RHIZOME_PROJECT_ROOT,
// then discovery from startingPath -- is directly testable without
// t.Setenv. Callers pass cfg.ProjectRoot (from config.Load) as envRoot.
func resolveServeProjectRoot(flagOverride, envRoot, startingPath string) (string, bool, error) {
	if flagOverride != "" {
		resolved, err := projectconfig.LoadProjectRoot(flagOverride)
		if err != nil {
			return "", false, err
		}
		return resolved.Root, false, nil
	}
	if envRoot != "" {
		resolved, err := projectconfig.LoadProjectRoot(envRoot)
		if err != nil {
			return "", false, err
		}
		return resolved.Root, false, nil
	}
	discovered, err := projectconfig.Discover(startingPath)
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Code == projectconfig.CodeProjectNotFound {
			return "", true, nil
		}
		return "", false, err
	}
	return discovered.Root, false, nil
}

func runInit(ctx context.Context, startingPath string, pathInputs projectconfig.PathInputs, dataRootOverride string, stdout io.Writer) error {
	dataRoot, err := resolveDataRoot(pathInputs, dataRootOverride)
	if err != nil {
		return err
	}
	generator, err := ids.NewGenerator(clock.RealClock{}, rand.Reader)
	if err != nil {
		return err
	}
	proj, err := projectconfig.Initialize(startingPath, generator, dataRoot)
	if err != nil {
		return err
	}
	project, err := projectruntime.OpenProject(ctx, projectruntime.Options{
		StartingPath: startingPath,
		DataRoot:     dataRoot,
		PathInputs:   pathInputs,
		Clock:        clock.RealClock{},
		SQLite:       sqlite.Options{},
	})
	if err != nil {
		// Initialize already succeeded: without this, a later failure (for
		// example opening or migrating the database) would leave a
		// half-initialized identity file and data directory behind.
		if rollbackErr := projectconfig.RollbackInitialize(proj); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = project.Close(closeCtx)
	}()

	response := cliadapter.InitResponse{
		Root:         proj.Root,
		ProjectID:    proj.Identity.ProjectID,
		DatabasePath: proj.DatabasePath,
		NextActions: []string{
			"Run 'rhizome-mcp connect claude' (or codex/vscode/cursor/windsurf/zed/gemini/json) to register this server with your MCP client.",
		},
	}
	return writeJSON(stdout, response)
}

func runServe(ctx context.Context, cfg *config.Config, stderr io.Writer, router projectrouting.ProjectRouter) (err error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	var expirer attemptExpirer
	if routerExpirer, ok := router.(attemptExpirer); ok {
		expirer = routerExpirer
	}
	cleanupDone := runAttemptSweeper(cleanupCtx, attemptCleanupInterval, expirer)
	defer func() {
		stopCleanup()
		<-cleanupDone
	}()

	if cfg.HTTPAddress != "" {
		return serveHTTP(ctx, cfg, stderr, router)
	}
	return serveStdio(ctx, cfg, stderr, router)
}

func newMCPServer(cfg *config.Config, router projectrouting.ProjectRouter) (*mcpadapter.Server, error) {
	if router == nil {
		return nil, errors.New("project router is required")
	}
	exportDirectory := ""
	if rootedRouter, ok := router.(interface{ DataRoot() string }); ok {
		exportDirectory = filepath.Join(rootedRouter.DataRoot(), "exports")
	}
	return mcpadapter.NewServer(mcpadapter.Options{
		ProjectRouter:   router,
		ServerName:      cfg.ServerName,
		ServerVersion:   cfg.Version,
		ConfigVersion:   projectconfig.CurrentIdentityVersion,
		ToolProfile:     cfg.ToolProfile,
		Toolsets:        cfg.Toolsets,
		ContentMode:     cfg.ContentMode,
		ExportDirectory: exportDirectory,
	})
}

func runServeStdio(ctx context.Context, cfg *config.Config, stderr io.Writer, router projectrouting.ProjectRouter) error {
	server, err := newMCPServer(cfg, router)
	if err != nil {
		return err
	}
	return server.Run(ctx, &sdkmcp.StdioTransport{})
}

func runServeHTTP(ctx context.Context, cfg *config.Config, stderr io.Writer, router projectrouting.ProjectRouter) error {
	handler, err := newHTTPHandler(cfg, router)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	return projectruntime.ServeHTTPServer(ctx, projectruntime.HTTPServerOptions{Address: cfg.HTTPAddress, Logger: logger, Handler: handler})
}

func runBoardServe(ctx context.Context, cfg *config.Config, stdout io.Writer, boardService interface {
	GetBoard(context.Context) (domain.BoardResult, error)
	GetIssueDetail(context.Context, string) (domain.IssueDetail, error)
	Search(context.Context, domain.SearchInput) (domain.SearchPage, error)
}) error {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if stdout == nil {
		stdout = io.Discard
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := cliadapter.NewBoardHTTPHandler(boardService)
	return projectruntime.ServeHTTPServer(ctx, projectruntime.HTTPServerOptions{
		Address: cfg.HTTPAddress,
		Logger:  logger,
		Handler: handler,
		OnListener: func(listener net.Listener) {
			_, _ = fmt.Fprintf(stdout, "%s\n", boardServeURL(listener))
		},
	})
}

type boardServeService struct {
	boardService       *application.BoardService
	issueDetailService *application.IssueDetailService
	searchService      *application.SearchService
}

func (service boardServeService) GetBoard(ctx context.Context) (domain.BoardResult, error) {
	if service.boardService == nil {
		return domain.BoardResult{}, errors.New("board service is not configured")
	}
	return service.boardService.GetBoard(ctx)
}

func (service boardServeService) GetBoardWatermark(ctx context.Context) (string, error) {
	if service.boardService == nil {
		return "", errors.New("board service is not configured")
	}
	return service.boardService.GetBoardWatermark(ctx)
}

func (service boardServeService) GetIssueDetail(ctx context.Context, identifier string) (domain.IssueDetail, error) {
	if service.issueDetailService == nil {
		return domain.IssueDetail{}, errors.New("issue detail service is not configured")
	}
	return service.issueDetailService.GetIssueDetail(ctx, identifier)
}

func (service boardServeService) Search(ctx context.Context, input domain.SearchInput) (domain.SearchPage, error) {
	if service.searchService == nil {
		return domain.SearchPage{}, errors.New("search service is not configured")
	}
	return service.searchService.Search(ctx, input)
}

func boardServeURL(listener net.Listener) string {
	if listener == nil {
		return ""
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return fmt.Sprintf("http://%s/", listener.Addr().String())
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

func newHTTPHandler(cfg *config.Config, router projectrouting.ProjectRouter) (http.Handler, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if router == nil {
		return nil, errors.New("project router is required")
	}
	// One server serves every session: the adapter keys all of its state per
	// session, and the SDK allows the factory to return the same server.
	server, err := newMCPServer(cfg, router)
	if err != nil {
		return nil, err
	}
	serverFactory := func(*http.Request) *sdkmcp.Server {
		return server.SDKServer()
	}
	streamableHandler := sdkmcp.NewStreamableHTTPHandler(serverFactory, &sdkmcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	handler := http.HandlerFunc(streamableHandler.ServeHTTP)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	return mux, nil
}

// connectServeInvocation is the command/args every connect target agrees
// on (ISSUE-206 AC2): they all pin --project-root to the discovered
// project root, and they all resolve to the same command form.
type connectServeInvocation struct {
	Command string
	Args    []string
}

// resolveConnectServeInvocation is the single shared helper every connect
// target uses, so every target agrees on whether --project-root is pinned
// (when projectRoot is non-empty) and what command form to use.
//
// Precedence: launched via the npm launcher (RHIZOME_MCP_NPX=1) always
// wins, since the resolved binary path in that case points into the npx
// cache and goes stale on eviction or a version bump -- "npx rhizome-mcp"
// is the only portable form there. Otherwise, --command opts into a bare
// "rhizome-mcp" command name for a config shareable across machines that
// have it on PATH. The default remains this resolved binary's absolute
// path, unchanged from before this fix.
func resolveConnectServeInvocation(binaryPath, projectRoot string, bareCommand, npxLaunched bool) connectServeInvocation {
	args := []string{"serve"}
	if projectRoot != "" {
		args = append(args, "--project-root", projectRoot)
	}
	switch {
	case npxLaunched:
		return connectServeInvocation{Command: "npx", Args: append([]string{"-y", "rhizome-mcp"}, args...)}
	case bareCommand:
		return connectServeInvocation{Command: "rhizome-mcp", Args: args}
	default:
		return connectServeInvocation{Command: binaryPath, Args: args}
	}
}

func runConnect(ctx context.Context, startingPath string, target string, binaryPath string, printOnly, bareCommand, npxLaunched bool, stdout, stderr io.Writer) error {
	return runConnectWithOptions(ctx, startingPath, binaryPath, "", npxLaunched, cliadapter.ConnectOptions{
		Target:      target,
		PrintOnly:   printOnly,
		BareCommand: bareCommand,
	}, os.UserHomeDir, stdout, stderr)
}

func runConnectWithOptions(
	ctx context.Context,
	startingPath string,
	binaryPath string,
	dataRootOverride string,
	npxLaunched bool,
	opts cliadapter.ConnectOptions,
	userHomeDir func() (string, error),
	stdout, stderr io.Writer,
) error {
	// Validate target and flags before touching the filesystem so direct
	// (non-CLI) callers share the same self-sufficient contract.
	switch opts.Target {
	case "claude", "codex", "vscode", "cursor", "windsurf", "zed", "gemini", "json":
	default:
		return fmt.Errorf("unsupported target %q", opts.Target)
	}
	if opts.Global && opts.ProjectRoot != "" {
		return cliadapter.NewUsageError("--global and --project-root are mutually exclusive; pass at most one")
	}
	if opts.Target == "vscode" && opts.Global && !opts.PrintOnly {
		return cliadapter.NewUsageError("VS Code MCP configuration is workspace-scoped (.vscode/mcp.json); pass --print for a global snippet")
	}
	if opts.ToolProfile != "" && opts.Toolsets != "" {
		return cliadapter.NewUsageError("--profile and --toolsets are mutually exclusive; pass at most one")
	}
	if _, err := domain.ParseToolProfile(opts.ToolProfile); err != nil {
		return err
	}
	if _, err := domain.ParseToolsets(opts.Toolsets); err != nil {
		return err
	}

	var projectRoot string
	if !opts.Global {
		var err error
		projectRoot, err = resolveConnectProjectRoot(startingPath, opts.ProjectRoot)
		if err != nil {
			return err
		}
	}

	invocation := resolveConnectServeInvocation(binaryPath, projectRoot, opts.BareCommand, npxLaunched)
	if dataRootOverride != "" {
		if !filepath.IsAbs(dataRootOverride) && startingPath != "" {
			dataRootOverride = filepath.Join(startingPath, dataRootOverride)
		}
		dataRoot, err := filepath.Abs(dataRootOverride)
		if err != nil {
			return fmt.Errorf("resolve data root: %w", err)
		}
		invocation.Args = append(invocation.Args, "--data-root", dataRoot)
	}
	if opts.ToolProfile != "" {
		invocation.Args = append(invocation.Args, "--profile", opts.ToolProfile)
	}
	if opts.Toolsets != "" {
		invocation.Args = append(invocation.Args, "--toolsets", opts.Toolsets)
	}

	switch opts.Target {
	case "codex":
		return connectCodex(ctx, invocation, opts.PrintOnly, stdout, stderr)
	case "json":
		return connectJSON(invocation, stdout)
	case "vscode":
		return connectVSCode(projectRoot, invocation, opts.PrintOnly, stdout)
	default:
		return connectJSONTarget(opts.Target, projectRoot, opts.Global, opts.PrintOnly, invocation, userHomeDir, stdout)
	}
}

func resolveConnectProjectRoot(startingPath, explicitProjectRoot string) (string, error) {
	if explicitProjectRoot != "" {
		candidate := explicitProjectRoot
		if !filepath.IsAbs(candidate) && startingPath != "" {
			candidate = filepath.Join(startingPath, candidate)
		}
		absCandidate, err := filepath.Abs(candidate)
		if err != nil {
			return "", fmt.Errorf("resolve project root %q: %w", explicitProjectRoot, err)
		}
		loaded, err := projectconfig.LoadProjectRoot(absCandidate)
		if err != nil {
			var domainErr *domain.Error
			if errors.As(err, &domainErr) && domainErr.Code == projectconfig.CodeProjectNotFound {
				return "", fmt.Errorf("no rhizome-mcp project found at %q; run `rhizome-mcp init` first", absCandidate)
			}
			return "", fmt.Errorf("resolve project root %q: %w", explicitProjectRoot, err)
		}
		return loaded.Root, nil
	}

	discovered, err := projectconfig.Discover(startingPath)
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Code == projectconfig.CodeProjectNotFound {
			return "", fmt.Errorf("no rhizome-mcp project found at or above %q; run `rhizome-mcp init` first", startingPath)
		}
		return "", fmt.Errorf("discover project root: %w", err)
	}
	return discovered.Root, nil
}

type jsonTargetSpec struct {
	configKey   string
	includeType bool
	projectPath func(projectRoot string) string
	globalPath  func(homeDir string) string
}

var jsonTargetSpecs = map[string]jsonTargetSpec{
	"claude": {
		configKey:   "mcpServers",
		includeType: true,
		projectPath: func(projectRoot string) string { return filepath.Join(projectRoot, ".mcp.json") },
		globalPath:  func(homeDir string) string { return filepath.Join(homeDir, ".claude.json") },
	},
	"cursor": {
		configKey:   "mcpServers",
		includeType: false,
		projectPath: func(projectRoot string) string { return filepath.Join(projectRoot, ".cursor", "mcp.json") },
		globalPath:  func(homeDir string) string { return filepath.Join(homeDir, ".cursor", "mcp.json") },
	},
	"windsurf": {
		configKey:   "mcpServers",
		includeType: false,
		// Windsurf only loads its global config file; workspace-scoped runs still
		// pin --project-root in the args written there.
		projectPath: nil,
		globalPath:  func(homeDir string) string { return filepath.Join(homeDir, ".codeium", "windsurf", "mcp_config.json") },
	},
	"zed": {
		configKey:   "context_servers",
		includeType: false,
		projectPath: func(projectRoot string) string { return filepath.Join(projectRoot, ".zed", "settings.json") },
		globalPath:  func(homeDir string) string { return filepath.Join(homeDir, ".config", "zed", "settings.json") },
	},
	"gemini": {
		configKey:   "mcpServers",
		includeType: false,
		projectPath: func(projectRoot string) string { return filepath.Join(projectRoot, ".gemini", "settings.json") },
		globalPath:  func(homeDir string) string { return filepath.Join(homeDir, ".gemini", "settings.json") },
	},
}

func buildJSONConfig(configKey string, invocation connectServeInvocation, includeType bool) map[string]interface{} {
	entry := map[string]interface{}{
		"command": invocation.Command,
		"args":    invocation.Args,
	}
	if includeType {
		entry["type"] = "stdio"
	}
	return map[string]interface{}{
		configKey: map[string]interface{}{
			"rhizome-mcp": entry,
		},
	}
}

func connectJSONTarget(target, projectRoot string, global, printOnly bool, invocation connectServeInvocation, userHomeDir func() (string, error), stdout io.Writer) error {
	spec, ok := jsonTargetSpecs[target]
	if !ok {
		return fmt.Errorf("unsupported target %q", target)
	}
	config := buildJSONConfig(spec.configKey, invocation, spec.includeType)
	if printOnly {
		return writeJSONToWriter(stdout, config)
	}

	var filePath string
	if global || spec.projectPath == nil {
		if userHomeDir == nil {
			userHomeDir = os.UserHomeDir
		}
		homeDir, err := userHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home directory: %w", err)
		}
		if homeDir == "" {
			return errors.New("resolve home directory: empty home directory")
		}
		filePath = spec.globalPath(homeDir)
	} else {
		filePath = spec.projectPath(projectRoot)
	}
	return mergeAndWriteJSONConfig(filePath, config, spec.configKey, "rhizome-mcp")
}

func connectClaude(projectRoot string, invocation connectServeInvocation, printOnly bool, stdout io.Writer) error {
	return connectJSONTarget("claude", projectRoot, false, printOnly, invocation, os.UserHomeDir, stdout)
}

func connectVSCode(projectRoot string, invocation connectServeInvocation, printOnly bool, stdout io.Writer) error {
	config := buildJSONConfig("servers", invocation, true)
	if printOnly {
		return writeJSONToWriter(stdout, config)
	}
	if projectRoot == "" {
		return cliadapter.NewUsageError("VS Code MCP configuration is workspace-scoped (.vscode/mcp.json); pass --print for a global snippet")
	}

	mcpJSONPath := filepath.Join(projectRoot, ".vscode", "mcp.json")
	if err := mergeAndWriteJSONConfig(mcpJSONPath, config, "servers", "rhizome-mcp"); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Wrote .vscode/mcp.json.")
	fmt.Fprintln(stdout, "Tip: the Rhizome MCP extension on the VS Code Marketplace bundles this binary and")
	fmt.Fprintln(stdout, "registers the server automatically, so most users won't need `connect vscode` at all:")
	fmt.Fprintln(stdout, "https://marketplace.visualstudio.com/items?itemName=odrin.rhizome-mcp")
	return nil
}

func connectCodex(ctx context.Context, invocation connectServeInvocation, printOnly bool, stdout, stderr io.Writer) error {
	quotedArgs := make([]string, len(invocation.Args))
	for index, arg := range invocation.Args {
		quotedArgs[index] = fmt.Sprintf("%q", arg)
	}
	tomlSnippet := fmt.Sprintf(`[mcp_servers.rhizome-mcp]
command = %q
args = [%s]
`, invocation.Command, strings.Join(quotedArgs, ", "))

	if printOnly || !canExecuteCodex() {
		if printOnly {
			fmt.Fprint(stdout, "Add the following to your Codex configuration:\n\n")
		} else {
			fmt.Fprint(stdout, "Codex not found on PATH. Add the following to your Codex configuration:\n\n")
		}
		fmt.Fprint(stdout, tomlSnippet)
		return nil
	}

	cmdArgs := append([]string{"mcp", "add", "rhizome-mcp", "--", invocation.Command}, invocation.Args...)
	cmd := exec.CommandContext(ctx, "codex", cmdArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func connectJSON(invocation connectServeInvocation, stdout io.Writer) error {
	return writeJSONToWriter(stdout, buildJSONConfig("mcpServers", invocation, false))
}

func canExecuteCodex() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

func mergeAndWriteJSONConfig(filePath string, newConfig map[string]interface{}, configKey string, serverKey string) error {
	var existingConfig map[string]interface{}

	if _, err := os.Stat(filePath); err == nil {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read config file: %w", err)
		}
		if err := json.Unmarshal(data, &existingConfig); err != nil {
			return fmt.Errorf("parse config file: %w", err)
		}
		if existingConfig == nil {
			return fmt.Errorf("parse config file: expected a JSON object")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config file: %w", err)
	}

	if existingConfig == nil {
		existingConfig = make(map[string]interface{})
	}

	rawServers, exists := existingConfig[configKey]
	var servers map[string]interface{}
	if !exists || rawServers == nil {
		servers = make(map[string]interface{})
		existingConfig[configKey] = servers
	} else {
		var ok bool
		servers, ok = rawServers.(map[string]interface{})
		if !ok {
			return fmt.Errorf("parse config file: %q must be a JSON object", configKey)
		}
	}

	newServer := newConfig[configKey].(map[string]interface{})[serverKey]
	servers[serverKey] = newServer

	data, err := json.MarshalIndent(existingConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	return nil
}

func writeJSONToWriter(w io.Writer, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func resolveDataRoot(pathInputs projectconfig.PathInputs, dataRootOverride string) (string, error) {
	if dataRootOverride != "" {
		return dataRootOverride, nil
	}
	dataRoot, err := projectconfig.ResolveDataRoot(pathInputs)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return "", err
	}
	return dataRoot, nil
}

func doctorReportFromRuntime(report projectruntime.DoctorReport, appVersion string) cliadapter.DoctorReport {
	checks := make([]cliadapter.DoctorCheck, len(report.Checks))
	for index, check := range report.Checks {
		checks[index] = cliadapter.DoctorCheck{Check: check.Name, Healthy: check.Healthy, Message: check.Message}
	}
	return cliadapter.DoctorReport{Full: report.Full, AppVersion: appVersion, Checks: checks}
}

func writeJSON(w io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
