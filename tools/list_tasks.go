package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/store"
)

type ListTasksInput struct {
	Status string `json:"status,omitempty" jsonschema:"description=Filter by status: pending, completed, or all"`
}

type ListTasksOutput struct {
	Tasks   []store.Task `json:"tasks" jsonschema:"description=Tasks matching the filter"`
	Count   int          `json:"count" jsonschema:"description=Number of tasks returned"`
	Message string       `json:"message" jsonschema:"description=Operation status message"`
}

func ListTasksHandler(ctx context.Context, _ *mcp.CallToolRequest, input ListTasksInput) (*mcp.CallToolResult, ListTasksOutput, error) {
	if ctx.Err() != nil {
		return nil, ListTasksOutput{}, ctx.Err()
	}

	status := strings.ToLower(strings.TrimSpace(input.Status))
	if status != "" && status != "all" && status != "pending" && status != "completed" {
		return nil, ListTasksOutput{}, fmt.Errorf("invalid status %q; expected pending, completed, or all", input.Status)
	}

	tasks := store.Default().List(status)
	return nil, ListTasksOutput{
		Tasks:   tasks,
		Count:   len(tasks),
		Message: "tasks listed",
	}, nil
}

func registerListTasksTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List tasks with an optional status filter",
	}, ListTasksHandler)
}
