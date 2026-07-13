package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

func RegisterTools(server *mcp.Server) {
	registerAddTaskTool(server)
	registerListTasksTool(server)
	registerCompleteTaskTool(server)
}
