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
	server, err := mcpadapter.NewServer(mcpadapter.Options{
		IssueService:   issueService,
		ProjectService: projectService,
		ServerName:     cfg.ServerName,
		ServerVersion:  cfg.Version,
		ConfigVersion:  projectconfig.CurrentIdentityVersion,
	})
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}
