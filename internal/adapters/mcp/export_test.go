package mcp

import (
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

// TrackedSessionCounts reports how many connections and started sessions the
// adapter still tracks. Transports that serve many sessions from one server
// reuse the adapter, so these counts must return to zero once sessions end.
func (server *Server) TrackedSessionCounts() (connections int, started int) {
	if server == nil {
		return 0, 0
	}
	server.adapter.sessionMu.Lock()
	defer server.adapter.sessionMu.Unlock()
	return len(server.adapter.connectionSessions), len(server.adapter.sessionStarted)
}

// ToolProfileIncludesCoreToolForTest exercises toolProfileIncludes exactly
// as the real registry does for a groupCore-tagged tool with the given
// readOnlyHint. It exists so mcp_test can prove a hypothetical mutating
// core tool is excluded from the read-only profile (ISSUE-99) without
// needing a real registered tool: groupCore's "always advertised" bypass
// must never reach past the read-only check.
func ToolProfileIncludesCoreToolForTest(profile string, readOnlyHint bool) bool {
	toolDef := &sdkmcp.Tool{Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: readOnlyHint}}
	return toolProfileIncludes(domain.ToolProfile(profile), groupCore, toolDef)
}
