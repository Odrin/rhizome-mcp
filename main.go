package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/config"
	"rhizome-mcp/resources"
	"rhizome-mcp/tools"
)

func main() {
	cfg := config.Load()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := mcp.NewServer(
		&mcp.Implementation{Name: cfg.ServerName, Version: cfg.Version},
		&mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{
				Tools:     &mcp.ToolCapabilities{},
				Resources: &mcp.ResourceCapabilities{},
			},
		},
	)

	tools.RegisterTools(server)
	resources.RegisterResources(server)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		slog.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}
