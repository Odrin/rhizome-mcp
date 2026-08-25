package mcp

import (
	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

// StrictOutputSchemaForTest returns the strict (pre-projection) output schema
// registered for a tool, or nil when the tool is unknown. tools/list
// advertises the compact projection; conformance tests validate real
// responses against this strict original so advertisement trimming never
// weakens the validation contract.
func StrictOutputSchemaForTest(name string) *jsonschema.Schema {
	value, ok := strictOutputSchemas.Load(name)
	if !ok {
		return nil
	}
	return value.(*jsonschema.Schema)
}

// TrackedSessionCounts is retained for external test compatibility. Explicit
// handles have no connection-derived adapter state to track.
func (server *Server) TrackedSessionCounts() (connections int, started int) {
	return 0, 0
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
