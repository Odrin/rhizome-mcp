package resources

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/store"
)

type overviewResource struct {
	Stats   store.Stats  `json:"stats"`
	Pending []store.Task `json:"pendingTasks"`
}

func tasksOverviewHandler(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	payload := overviewResource{
		Stats:   store.Default().Stats(),
		Pending: store.Default().List("pending"),
	}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal tasks overview: %w", err)
	}

	uri := "tasks://overview"
	if req != nil && req.Params.URI != "" {
		uri = req.Params.URI
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      uri,
				MIMEType: "application/json",
				Text:     string(b),
			},
		},
	}, nil
}

func registerTasksOverviewResource(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:         "tasks://overview",
		Name:        "Tasks Overview",
		Description: "Current task statistics and pending tasks",
		MIMEType:    "application/json",
	}, tasksOverviewHandler)
}
