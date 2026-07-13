package resources

import "github.com/modelcontextprotocol/go-sdk/mcp"

func RegisterResources(server *mcp.Server) {
	registerTasksOverviewResource(server)
}
