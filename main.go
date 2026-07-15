package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

var (
	initRunner  = runInit
	serveRunner = runServe
)

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
	attemptService     *application.AttemptService
	workContextService *application.WorkContextService
	sessionService     *application.AgentSessionService
}

func main() {
	cfg := config.Load()
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
	serveHandler := func(ctx context.Context) error {
		if bundle == nil {
			bundle, project, err = composeServices(ctx, startingPath, pathInputs, dataRootOverride)
			if err != nil {
				return err
			}
		}
		return serveRunner(ctx, cfg, stderr, bundle)
	}

	if len(args) > 0 && args[0] != "init" && (args[0] == "serve" || args[0] == "project" || args[0] == "issue" || args[0] == "search" || args[0] == "graph") {
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
			ProjectService: bundle.projectService,
			IssueService:   bundle.issueService,
			SearchService:  bundle.searchService,
			GraphService:   bundle.graphService,
		}
	}

	adapter := cliadapter.New(services, stdout, stderr, initHandler, serveHandler)
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
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		ticker := time.NewTicker(attemptCleanupInterval)
		defer ticker.Stop()
		for {
			if _, err := bundle.attemptService.ExpireAttempts(cleanupCtx); err != nil && cleanupCtx.Err() == nil {
				slog.Error("attempt expiry cleanup failed", "error", err)
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
	server, err := mcpadapter.NewServer(mcpadapter.Options{
		IssueService:       bundle.issueService,
		ProjectService:     bundle.projectService,
		RelationService:    bundle.relationService,
		GraphService:       bundle.graphService,
		PlanningService:    bundle.planningService,
		CommentService:     bundle.commentService,
		DecisionService:    bundle.decisionService,
		ActivityService:    bundle.activityService,
		SearchService:      bundle.searchService,
		AttemptService:     bundle.attemptService,
		SessionService:     bundle.sessionService,
		WorkContextService: bundle.workContextService,
		ServerName:         cfg.ServerName,
		ServerVersion:      cfg.Version,
		ConfigVersion:      projectconfig.CurrentIdentityVersion,
	})
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
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
	attemptService, err := application.NewAttemptService(attemptRepository, source, generator)
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
		attemptService:     attemptService,
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

func writeJSON(w io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
