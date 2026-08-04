package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/config"
	cliadapter "rhizome-mcp/internal/adapters/cli"
	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
	"rhizome-mcp/internal/projectconfig"
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

type composedServices struct {
	project *projectruntime.Project

	projectService     *application.ProjectService
	issueService       *application.IssueService
	relationService    *application.RelationService
	graphService       *application.GraphService
	planningService    *application.PlanningService
	commentService     *application.CommentService
	decisionService    *application.DecisionService
	activityService    *application.ActivityService
	searchService      *application.SearchService
	reviewService      *application.ReviewService
	attemptService     *application.AttemptService
	maintenanceService *application.MaintenanceService
	workContextService *application.WorkContextService
	sessionService     *application.AgentSessionService
	boardService       *application.BoardService

	closeOnce sync.Once
	closeErr  error
}

func (bundle *composedServices) ProjectRef() string {
	if bundle == nil || bundle.project == nil {
		return ""
	}
	return bundle.project.ProjectID
}

func (bundle *composedServices) ProjectServices() mcpadapter.ProjectServices {
	if bundle == nil {
		return mcpadapter.ProjectServices{}
	}
	return mcpadapter.ProjectServices{
		IssueService:       bundle.issueService,
		ProjectService:     bundle.projectService,
		RelationService:    bundle.relationService,
		GraphService:       bundle.graphService,
		PlanningService:    bundle.planningService,
		CommentService:     bundle.commentService,
		DecisionService:    bundle.decisionService,
		ActivityService:    bundle.activityService,
		SearchService:      bundle.searchService,
		ReviewService:      bundle.reviewService,
		AttemptService:     bundle.attemptService,
		SessionService:     bundle.sessionService,
		WorkContextService: bundle.workContextService,
	}
}

func (bundle *composedServices) Close(ctx context.Context) error {
	if bundle == nil || bundle.project == nil {
		return nil
	}
	bundle.closeOnce.Do(func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		bundle.closeErr = bundle.project.Close(closeCtx)
	})
	return bundle.closeErr
}

func main() {
	resolvedVersion, resolvedCommit, resolvedDate := resolveVersion()
	cfg := config.Load()
	cfg.Version = resolvedVersion
	cfg.VersionCommit = resolvedCommit
	cfg.VersionDate = resolvedDate
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
		XDGDataHome:  os.Getenv("XDG_DATA_HOME"),
		LocalAppData: os.Getenv("LOCALAPPDATA"),
	}

	if err := runCLI(ctx, cfg, os.Stdout, os.Stderr, os.Args[1:], startingPath, pathInputs); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
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

	var bundle *composedServices
	var project *projectruntime.Project
	var router mcpadapter.ProjectRouter
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
		serveProjectRoot, serveShared, err = resolveServeProjectRoot(startingPath, serveProjectRootOverride)
		if err != nil {
			return err
		}
	}

	serveHandler := func(ctx context.Context, httpAddress string, toolProfile string) error {
		if httpAddress != "" {
			cfg.HTTPAddress = httpAddress
		}
		if toolProfile != "" {
			cfg.ToolProfile = toolProfile
		}
		if router == nil {
			dataRoot, dataRootErr := resolveDataRoot(pathInputs, dataRootOverride)
			if dataRootErr != nil {
				return dataRootErr
			}
			if serveShared {
				router = newProjectRouter(dataRoot, clock.RealClock{}, sqlite.Options{}, nil)
			} else {
				composeRoot := startingPath
				if serveProjectRoot != "" {
					composeRoot = serveProjectRoot
				}
				bundle, project, err = composeServices(ctx, composeRoot, pathInputs, dataRootOverride)
				if err != nil {
					return err
				}
				router = newProjectRouter(dataRoot, clock.RealClock{}, sqlite.Options{}, bundle)
			}
		}
		return serveRunner(ctx, cfg, stderr, router)
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
		report, err := project.Doctor(ctx, full)
		return doctorReportFromRuntime(report, cfg.Version), err
	}
	connectHandler := func(ctx context.Context, target string, printOnly bool) error {
		exePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("determine binary path: %w", err)
		}
		realPath, err := filepath.EvalSymlinks(exePath)
		if err != nil {
			return fmt.Errorf("resolve binary path: %w", err)
		}
		return runConnect(ctx, startingPath, target, realPath, printOnly, stdout, stderr)
	}

	if len(args) > 0 && args[0] != "init" && args[0] != "connect" && args[0] != "serve" && (args[0] == "project" || args[0] == "issue" || args[0] == "search" || args[0] == "graph" || args[0] == "maintenance" || args[0] == "backup" || args[0] == "doctor" || args[0] == "board") {
		bundle, project, err = composeServices(ctx, startingPath, pathInputs, dataRootOverride)
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
		services = cliadapter.Services{
			ProjectService:     bundle.projectService,
			IssueService:       bundle.issueService,
			SearchService:      bundle.searchService,
			GraphService:       bundle.graphService,
			MaintenanceService: bundle.maintenanceService,
			BoardService:       bundle.boardService,
		}
	}

	adapter := cliadapter.New(services, stdout, stderr, initHandler, serveHandler)
	adapter.SetBackupHandler(backupHandler)
	adapter.SetDoctorHandler(doctorHandler)
	adapter.SetConnectHandler(connectHandler)
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

func resolveServeProjectRoot(startingPath, projectRootOverride string) (string, bool, error) {
	if projectRootOverride != "" {
		resolved, err := projectconfig.LoadProjectRoot(projectRootOverride)
		if err != nil {
			return "", false, err
		}
		return resolved.Root, false, nil
	}
	if envRoot := os.Getenv("RHIZOME_PROJECT_ROOT"); envRoot != "" {
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
			"Run 'rhizome-mcp connect claude' (or codex/vscode/json) to register this server with your MCP client.",
		},
	}
	return writeJSON(stdout, response)
}

func runServe(ctx context.Context, cfg *config.Config, stderr io.Writer, router mcpadapter.ProjectRouter) (err error) {
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
	defer func() {
		if router == nil {
			return
		}
		if closer, ok := router.(interface{ Close(context.Context) error }); ok {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 5*time.Second)
			defer cancel()
			err = errors.Join(err, closer.Close(closeCtx))
		}
	}()

	if cfg.HTTPAddress != "" {
		return serveHTTP(ctx, cfg, stderr, router)
	}
	return serveStdio(ctx, cfg, stderr, router)
}

func newMCPServer(cfg *config.Config, router mcpadapter.ProjectRouter) (*mcpadapter.Server, error) {
	if router == nil {
		return nil, errors.New("project router is required")
	}
	return mcpadapter.NewServer(mcpadapter.Options{
		ProjectRouter: router,
		ServerName:    cfg.ServerName,
		ServerVersion: cfg.Version,
		ConfigVersion: projectconfig.CurrentIdentityVersion,
		ToolProfile:   cfg.ToolProfile,
	})
}

func runServeStdio(ctx context.Context, cfg *config.Config, stderr io.Writer, router mcpadapter.ProjectRouter) error {
	server, err := newMCPServer(cfg, router)
	if err != nil {
		return err
	}
	return server.Run(ctx, &sdkmcp.StdioTransport{})
}

func runServeHTTP(ctx context.Context, cfg *config.Config, stderr io.Writer, router mcpadapter.ProjectRouter) error {
	handler, err := newHTTPHandler(cfg, router)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	return projectruntime.ServeHTTPServer(ctx, projectruntime.HTTPServerOptions{Address: cfg.HTTPAddress, Logger: logger, Handler: handler})
}

func newHTTPHandler(cfg *config.Config, router mcpadapter.ProjectRouter) (http.Handler, error) {
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

func runConnect(ctx context.Context, startingPath string, target string, binaryPath string, printOnly bool, stdout, stderr io.Writer) error {
	switch target {
	case "claude":
		return connectClaude(ctx, startingPath, binaryPath, printOnly, stdout)
	case "vscode":
		return connectVSCode(ctx, startingPath, binaryPath, printOnly, stdout)
	case "codex":
		return connectCodex(ctx, binaryPath, printOnly, stdout, stderr)
	case "json":
		return connectJSON(binaryPath, stdout)
	default:
		return fmt.Errorf("unsupported target %q", target)
	}
}

func connectClaude(ctx context.Context, startingPath string, binaryPath string, printOnly bool, stdout io.Writer) error {
	mcpJSONPath := filepath.Join(startingPath, ".mcp.json")

	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"rhizome-mcp": map[string]interface{}{
				"type":    "stdio",
				"command": binaryPath,
				"args":    []string{"serve", "--project-root", startingPath},
			},
		},
	}

	if printOnly {
		return writeJSONToWriter(stdout, config)
	}

	return mergeAndWriteJSONConfig(mcpJSONPath, config, "mcpServers", "rhizome-mcp")
}

func connectVSCode(ctx context.Context, startingPath string, binaryPath string, printOnly bool, stdout io.Writer) error {
	vscodeDir := filepath.Join(startingPath, ".vscode")
	mcpJSONPath := filepath.Join(vscodeDir, "mcp.json")

	config := map[string]interface{}{
		"servers": map[string]interface{}{
			"rhizome-mcp": map[string]interface{}{
				"type":    "stdio",
				"command": binaryPath,
				"args":    []string{"serve", "--project-root", startingPath},
			},
		},
	}

	if printOnly {
		return writeJSONToWriter(stdout, config)
	}

	if err := os.MkdirAll(vscodeDir, 0o755); err != nil {
		return fmt.Errorf("create .vscode directory: %w", err)
	}

	if err := mergeAndWriteJSONConfig(mcpJSONPath, config, "servers", "rhizome-mcp"); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Wrote .vscode/mcp.json.")
	fmt.Fprintln(stdout, "Tip: the Rhizome MCP extension on the VS Code Marketplace bundles this binary and")
	fmt.Fprintln(stdout, "registers the server automatically, so most users won't need `connect vscode` at all:")
	fmt.Fprintln(stdout, "https://marketplace.visualstudio.com/items?itemName=odrin.rhizome-mcp")
	return nil
}

func connectCodex(ctx context.Context, binaryPath string, printOnly bool, stdout, stderr io.Writer) error {
	tomlSnippet := fmt.Sprintf(`[mcp_servers.rhizome-mcp]
command = "%s"
args = ["serve"]
`, binaryPath)

	if printOnly || !canExecuteCodex() {
		if printOnly {
			fmt.Fprint(stdout, "Add the following to your Codex configuration:\n\n")
		} else {
			fmt.Fprint(stdout, "Codex not found on PATH. Add the following to your Codex configuration:\n\n")
		}
		fmt.Fprint(stdout, tomlSnippet)
		return nil
	}

	cmd := exec.CommandContext(ctx, "codex", "mcp", "add", "rhizome-mcp", "--", binaryPath, "serve")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func connectJSON(binaryPath string, stdout io.Writer) error {
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"rhizome-mcp": map[string]interface{}{
				"command": binaryPath,
				"args":    []string{"serve"},
			},
		},
	}
	return writeJSONToWriter(stdout, config)
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
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config file: %w", err)
	}

	if existingConfig == nil {
		existingConfig = make(map[string]interface{})
	}

	if _, exists := existingConfig[configKey]; !exists {
		existingConfig[configKey] = make(map[string]interface{})
	}

	servers, ok := existingConfig[configKey].(map[string]interface{})
	if !ok {
		servers = make(map[string]interface{})
		existingConfig[configKey] = servers
	}

	newServer := newConfig[configKey].(map[string]interface{})[serverKey]
	servers[serverKey] = newServer

	data, err := json.MarshalIndent(existingConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
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

func newComposedServices(project *projectruntime.Project) (*composedServices, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}

	source := clock.RealClock{}
	issueRepository, err := sqlite.NewIssueRepository(project.Database)
	if err != nil {
		return nil, err
	}
	projectRepository, err := sqlite.NewProjectRepository(project.Database)
	if err != nil {
		return nil, err
	}
	relationRepository, err := sqlite.NewRelationRepository(project.Database)
	if err != nil {
		return nil, err
	}
	graphRepository, err := sqlite.NewGraphRepository(project.Database)
	if err != nil {
		return nil, err
	}
	planningRepository, err := sqlite.NewPlanningRepository(project.Database)
	if err != nil {
		return nil, err
	}
	commentRepository, err := sqlite.NewCommentRepository(project.Database)
	if err != nil {
		return nil, err
	}
	decisionRepository, err := sqlite.NewDecisionRepository(project.Database)
	if err != nil {
		return nil, err
	}
	activityRepository, err := sqlite.NewActivityRepository(project.Database)
	if err != nil {
		return nil, err
	}
	searchRepository, err := sqlite.NewSearchRepository(project.Database)
	if err != nil {
		return nil, err
	}
	reviewRepository, err := sqlite.NewReviewRepository(project.Database)
	if err != nil {
		return nil, err
	}
	searchIndexRepository, err := sqlite.NewSearchIndexRepository(project.Database)
	if err != nil {
		return nil, err
	}
	attemptRepository, err := sqlite.NewAttemptRepository(project.Database)
	if err != nil {
		return nil, err
	}
	workContextRepository, err := sqlite.NewWorkContextRepository(project.Database)
	if err != nil {
		return nil, err
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		return nil, err
	}
	issueService, err := application.NewIssueService(issueRepository, source, generator)
	if err != nil {
		return nil, err
	}
	projectService, err := application.NewProjectService(projectRepository)
	if err != nil {
		return nil, err
	}
	relationService, err := application.NewRelationService(relationRepository, source, generator)
	if err != nil {
		return nil, err
	}
	graphService, err := application.NewGraphService(graphRepository, source)
	if err != nil {
		return nil, err
	}
	planningService, err := application.NewPlanningService(planningRepository, source, generator)
	if err != nil {
		return nil, err
	}
	commentService, err := application.NewCommentService(commentRepository, source, generator)
	if err != nil {
		return nil, err
	}
	decisionService, err := application.NewDecisionService(decisionRepository, source, generator)
	if err != nil {
		return nil, err
	}
	activityService, err := application.NewActivityService(activityRepository)
	if err != nil {
		return nil, err
	}
	searchService, err := application.NewSearchService(searchRepository)
	if err != nil {
		return nil, err
	}
	reviewService, err := application.NewReviewService(reviewRepository, issueRepository, source)
	if err != nil {
		return nil, err
	}
	attemptService, err := application.NewAttemptService(attemptRepository, source, generator)
	if err != nil {
		return nil, err
	}
	maintenanceService, err := application.NewMaintenanceService(attemptRepository, searchIndexRepository, source)
	if err != nil {
		return nil, err
	}
	workContextService, err := application.NewWorkContextService(workContextRepository, source)
	if err != nil {
		return nil, err
	}
	sessionRepository, err := sqlite.NewAgentSessionRepository(project.Database)
	if err != nil {
		return nil, err
	}
	sessionService, err := application.NewAgentSessionService(sessionRepository, source, generator)
	if err != nil {
		return nil, err
	}
	boardService, err := application.NewBoardService(issueService, attemptService, reviewService, graphService, source)
	if err != nil {
		return nil, err
	}

	return &composedServices{
		project:            project,
		projectService:     projectService,
		issueService:       issueService,
		relationService:    relationService,
		graphService:       graphService,
		planningService:    planningService,
		commentService:     commentService,
		decisionService:    decisionService,
		activityService:    activityService,
		searchService:      searchService,
		reviewService:      reviewService,
		attemptService:     attemptService,
		maintenanceService: maintenanceService,
		workContextService: workContextService,
		sessionService:     sessionService,
		boardService:       boardService,
	}, nil
}

func composeServices(ctx context.Context, startingPath string, pathInputs projectconfig.PathInputs, dataRootOverride string) (bundle *composedServices, project *projectruntime.Project, err error) {
	project, err = openProject(ctx, startingPath, pathInputs, dataRootOverride)
	if err != nil {
		return nil, nil, err
	}
	openedProject := project
	keepProject := false
	defer func() {
		if keepProject {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := openedProject.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	bundle, err = newComposedServices(project)
	if err != nil {
		return nil, nil, err
	}
	keepProject = true
	return bundle, project, nil
}

func composeServicesFromExistingProject(ctx context.Context, projectID, dataRoot string, source clock.Clock, sqliteOptions sqlite.Options) (bundle *composedServices, project *projectruntime.Project, err error) {
	if source == nil {
		source = clock.RealClock{}
	}
	project, err = projectruntime.OpenExistingProject(ctx, projectID, dataRoot, source, sqliteOptions)
	if err != nil {
		return nil, nil, err
	}
	openedProject := project
	keepProject := false
	defer func() {
		if keepProject {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := openedProject.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	bundle, err = newComposedServices(project)
	if err != nil {
		return nil, nil, err
	}
	keepProject = true
	return bundle, project, nil
}

func openProject(ctx context.Context, startingPath string, pathInputs projectconfig.PathInputs, dataRootOverride string) (*projectruntime.Project, error) {
	options := projectruntime.Options{
		StartingPath: startingPath,
		PathInputs:   pathInputs,
		Clock:        clock.RealClock{},
		SQLite:       sqlite.Options{},
	}
	if dataRootOverride != "" {
		options.DataRoot = dataRootOverride
	}
	return projectruntime.OpenProject(ctx, options)
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
