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
	"os/signal"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/config"
	cliadapter "rhizome-mcp/internal/adapters/cli"
	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/projectconfig"
	projectruntime "rhizome-mcp/internal/runtime"
)

const attemptCleanupInterval = time.Minute

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

	initHandler := func(ctx context.Context, dataRoot string) error {
		if dataRootOverride != "" && dataRoot != "" {
			return errors.New("data root may only be specified once")
		}
		if dataRoot == "" {
			dataRoot = dataRootOverride
		}
		return initRunner(ctx, startingPath, pathInputs, dataRoot, stdout)
	}
	serveHandler := func(ctx context.Context, httpAddress string) error {
		if httpAddress != "" {
			cfg.HTTPAddress = httpAddress
		}
		if bundle == nil {
			bundle, project, err = composeServices(ctx, startingPath, pathInputs, dataRootOverride)
			if err != nil {
				return err
			}
		}
		return serveRunner(ctx, cfg, stderr, bundle)
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

	if len(args) > 0 && args[0] != "init" && (args[0] == "serve" || args[0] == "project" || args[0] == "issue" || args[0] == "search" || args[0] == "graph" || args[0] == "maintenance" || args[0] == "backup" || args[0] == "doctor") {
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
		}
	}

	adapter := cliadapter.New(services, stdout, stderr, initHandler, serveHandler)
	adapter.SetBackupHandler(backupHandler)
	adapter.SetDoctorHandler(doctorHandler)
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

	response := cliadapter.InitResponse{Root: proj.Root, ProjectID: proj.Identity.ProjectID, DatabasePath: proj.DatabasePath}
	return writeJSON(stdout, response)
}

func runServe(ctx context.Context, cfg *config.Config, stderr io.Writer, bundle *composedServices) error {
	if cfg == nil {
		cfg = &config.Config{}
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		ticker := time.NewTicker(attemptCleanupInterval)
		defer ticker.Stop()
		for {
			if bundle != nil && bundle.attemptService != nil {
				if _, err := bundle.attemptService.ExpireAttempts(cleanupCtx); err != nil && cleanupCtx.Err() == nil {
					slog.Error("attempt expiry cleanup failed", "error", err)
				}
			}
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() {
		stopCleanup()
		<-cleanupDone
	}()

	if cfg.HTTPAddress != "" {
		return serveHTTP(ctx, cfg, stderr, bundle)
	}
	return serveStdio(ctx, cfg, stderr, bundle)
}

func newMCPServer(cfg *config.Config, bundle *composedServices) (*mcpadapter.Server, error) {
	return mcpadapter.NewServer(mcpadapter.Options{
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
		ServerName:         cfg.ServerName,
		ServerVersion:      cfg.Version,
		ConfigVersion:      projectconfig.CurrentIdentityVersion,
	})
}

func runServeStdio(ctx context.Context, cfg *config.Config, stderr io.Writer, bundle *composedServices) error {
	server, err := newMCPServer(cfg, bundle)
	if err != nil {
		return err
	}
	return server.Run(ctx, &sdkmcp.StdioTransport{})
}

func runServeHTTP(ctx context.Context, cfg *config.Config, stderr io.Writer, bundle *composedServices) error {
	handler, err := newHTTPHandler(cfg, bundle)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	return projectruntime.ServeHTTPServer(ctx, projectruntime.HTTPServerOptions{Address: cfg.HTTPAddress, Logger: logger, Handler: handler})
}

func newHTTPHandler(cfg *config.Config, bundle *composedServices) (http.Handler, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if bundle == nil {
		return nil, errors.New("mcp services are required")
	}
	// One server serves every session: the adapter keys all of its state per
	// session, and the SDK allows the factory to return the same server.
	server, err := newMCPServer(cfg, bundle)
	if err != nil {
		return nil, err
	}
	serverFactory := func(*http.Request) *sdkmcp.Server {
		return server.SDKServer()
	}
	streamableHandler := sdkmcp.NewStreamableHTTPHandler(serverFactory, &sdkmcp.StreamableHTTPOptions{JSONResponse: true})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			if err := server.EndSession(request.Context(), request.Header.Get("Mcp-Session-Id")); err != nil {
				slog.Error("http agent session end failed", "error", err)
			}
		}
		streamableHandler.ServeHTTP(writer, request)
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	return mux, nil
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

	source := clock.RealClock{}
	issueRepository, err := sqlite.NewIssueRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	projectRepository, err := sqlite.NewProjectRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	relationRepository, err := sqlite.NewRelationRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	graphRepository, err := sqlite.NewGraphRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	planningRepository, err := sqlite.NewPlanningRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	commentRepository, err := sqlite.NewCommentRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	decisionRepository, err := sqlite.NewDecisionRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	activityRepository, err := sqlite.NewActivityRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	searchRepository, err := sqlite.NewSearchRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	reviewRepository, err := sqlite.NewReviewRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	searchIndexRepository, err := sqlite.NewSearchIndexRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	attemptRepository, err := sqlite.NewAttemptRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	workContextRepository, err := sqlite.NewWorkContextRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	issueService, err := application.NewIssueService(issueRepository, source, generator)
	if err != nil {
		return nil, nil, err
	}
	projectService, err := application.NewProjectService(projectRepository)
	if err != nil {
		return nil, nil, err
	}
	relationService, err := application.NewRelationService(relationRepository, source, generator)
	if err != nil {
		return nil, nil, err
	}
	graphService, err := application.NewGraphService(graphRepository, source)
	if err != nil {
		return nil, nil, err
	}
	planningService, err := application.NewPlanningService(planningRepository, source, generator)
	if err != nil {
		return nil, nil, err
	}
	commentService, err := application.NewCommentService(commentRepository, source, generator)
	if err != nil {
		return nil, nil, err
	}
	decisionService, err := application.NewDecisionService(decisionRepository, source, generator)
	if err != nil {
		return nil, nil, err
	}
	activityService, err := application.NewActivityService(activityRepository)
	if err != nil {
		return nil, nil, err
	}
	searchService, err := application.NewSearchService(searchRepository)
	if err != nil {
		return nil, nil, err
	}
	reviewService, err := application.NewReviewService(reviewRepository, issueRepository, source)
	if err != nil {
		return nil, nil, err
	}
	attemptService, err := application.NewAttemptService(attemptRepository, source, generator)
	if err != nil {
		return nil, nil, err
	}
	maintenanceService, err := application.NewMaintenanceService(attemptRepository, searchIndexRepository, source)
	if err != nil {
		return nil, nil, err
	}
	workContextService, err := application.NewWorkContextService(workContextRepository, source)
	if err != nil {
		return nil, nil, err
	}
	sessionRepository, err := sqlite.NewAgentSessionRepository(project.Database)
	if err != nil {
		return nil, nil, err
	}
	sessionService, err := application.NewAgentSessionService(sessionRepository, source, generator)
	if err != nil {
		return nil, nil, err
	}

	bundle = &composedServices{
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
