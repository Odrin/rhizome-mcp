package main

import (
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"os/signal"
	goruntime "runtime"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/config"
	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/projectconfig"
	projectruntime "rhizome-mcp/internal/runtime"
)

const attemptCleanupInterval = time.Minute

func main() {
	cfg := config.Load()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		slog.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *config.Config) error {
	startingPath, err := os.Getwd()
	if err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	pathInputs := projectconfig.PathInputs{
		GOOS:         goruntime.GOOS,
		HomeDir:      homeDir,
		XDGDataHome:  os.Getenv("XDG_DATA_HOME"),
		LocalAppData: os.Getenv("LOCALAPPDATA"),
	}
	dataRoot, err := projectconfig.ResolveDataRoot(pathInputs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return err
	}

	source := clock.RealClock{}
	project, err := projectruntime.OpenProject(ctx, projectruntime.Options{
		StartingPath: startingPath,
		PathInputs:   pathInputs,
		Clock:        source,
		SQLite:       sqlite.Options{},
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := project.Close(closeCtx); err != nil {
			slog.Error("project close failed", "error", err)
		}
	}()

	issueRepository, err := sqlite.NewIssueRepository(project.Database)
	if err != nil {
		return err
	}
	projectRepository, err := sqlite.NewProjectRepository(project.Database)
	if err != nil {
		return err
	}
	relationRepository, err := sqlite.NewRelationRepository(project.Database)
	if err != nil {
		return err
	}
	graphRepository, err := sqlite.NewGraphRepository(project.Database)
	if err != nil {
		return err
	}
	planningRepository, err := sqlite.NewPlanningRepository(project.Database)
	if err != nil {
		return err
	}
	commentRepository, err := sqlite.NewCommentRepository(project.Database)
	if err != nil {
		return err
	}
	decisionRepository, err := sqlite.NewDecisionRepository(project.Database)
	if err != nil {
		return err
	}
	activityRepository, err := sqlite.NewActivityRepository(project.Database)
	if err != nil {
		return err
	}
	attemptRepository, err := sqlite.NewAttemptRepository(project.Database)
	if err != nil {
		return err
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		return err
	}
	issueService, err := application.NewIssueService(issueRepository, source, generator)
	if err != nil {
		return err
	}
	projectService, err := application.NewProjectService(projectRepository)
	if err != nil {
		return err
	}
	relationService, err := application.NewRelationService(relationRepository, source, generator)
	if err != nil {
		return err
	}
	graphService, err := application.NewGraphService(graphRepository, source)
	if err != nil {
		return err
	}
	planningService, err := application.NewPlanningService(planningRepository, source, generator)
	if err != nil {
		return err
	}
	commentService, err := application.NewCommentService(commentRepository, source, generator)
	if err != nil {
		return err
	}
	decisionService, err := application.NewDecisionService(decisionRepository, source, generator)
	if err != nil {
		return err
	}
	activityService, err := application.NewActivityService(activityRepository)
	if err != nil {
		return err
	}
	attemptService, err := application.NewAttemptService(attemptRepository, source, generator)
	if err != nil {
		return err
	}
	sessionRepository, err := sqlite.NewAgentSessionRepository(project.Database)
	if err != nil {
		return err
	}
	sessionService, err := application.NewAgentSessionService(sessionRepository, source, generator)
	if err != nil {
		return err
	}
	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		ticker := time.NewTicker(attemptCleanupInterval)
		defer ticker.Stop()
		for {
			if _, err := attemptService.ExpireAttempts(cleanupCtx); err != nil && cleanupCtx.Err() == nil {
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
		IssueService:    issueService,
		ProjectService:  projectService,
		RelationService: relationService,
		GraphService:    graphService,
		PlanningService: planningService,
		CommentService:  commentService,
		DecisionService: decisionService,
		ActivityService: activityService,
		AttemptService:  attemptService,
		SessionService:  sessionService,
		ServerName:      cfg.ServerName,
		ServerVersion:   cfg.Version,
		ConfigVersion:   projectconfig.CurrentIdentityVersion,
	})
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}
