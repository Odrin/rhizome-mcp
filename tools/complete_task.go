package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/store"
)

type CompleteTaskInput struct {
	ID int `json:"id" jsonschema:"required,description=Task ID to mark as completed"`
}

type CompleteTaskOutput struct {
	Task    store.Task `json:"task" jsonschema:"description=The updated task"`
	Message string     `json:"message" jsonschema:"description=Operation status message"`
}

func CompleteTaskHandler(ctx context.Context, _ *mcp.CallToolRequest, input CompleteTaskInput) (*mcp.CallToolResult, CompleteTaskOutput, error) {
	if ctx.Err() != nil {
		return nil, CompleteTaskOutput{}, ctx.Err()
	}

	task, err := store.Default().Complete(input.ID)
	if err != nil {
		return nil, CompleteTaskOutput{}, fmt.Errorf("complete task: %w", err)
	}

	return nil, CompleteTaskOutput{
		Task:    task,
		Message: "task completed",
	}, nil
}

func registerCompleteTaskTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "complete_task",
		Description: "Mark a task as completed by ID",
	}, CompleteTaskHandler)
}
