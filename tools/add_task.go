package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/store"
)

type AddTaskInput struct {
	Title    string `json:"title" jsonschema:"required,description=Title of the task"`
	Priority string `json:"priority,omitempty" jsonschema:"description=Priority: low, medium, or high"`
	DueDate  string `json:"dueDate,omitempty" jsonschema:"description=Optional due date in YYYY-MM-DD format"`
}

type AddTaskOutput struct {
	Task    store.Task `json:"task" jsonschema:"description=The created task"`
	Message string     `json:"message" jsonschema:"description=Operation status message"`
}

func AddTaskHandler(ctx context.Context, _ *mcp.CallToolRequest, input AddTaskInput) (*mcp.CallToolResult, AddTaskOutput, error) {
	if ctx.Err() != nil {
		return nil, AddTaskOutput{}, ctx.Err()
	}

	task, err := store.Default().Add(input.Title, input.Priority, input.DueDate)
	if err != nil {
		return nil, AddTaskOutput{}, fmt.Errorf("create task: %w", err)
	}

	return nil, AddTaskOutput{
		Task:    task,
		Message: "task created",
	}, nil
}

func registerAddTaskTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_task",
		Description: "Create a new task with optional priority and due date",
	}, AddTaskHandler)
}
