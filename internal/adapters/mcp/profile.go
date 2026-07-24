package mcp

import (
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

// toolCapabilityGroup is the one mandatory, explicit classification every
// registered tool must declare through registerTool. It is reviewed
// alongside the ISSUE-53 annotation hints whenever a tool is added: a new
// tool cannot be registered without picking a group, since registerTool
// requires one.
type toolCapabilityGroup string

const (
	// groupCore tools are advertised in every profile, including
	// migration and agent: get_project reports the active profile itself,
	// so it can never be the tool a client loses when narrowing exposure.
	groupCore toolCapabilityGroup = "core"
	// groupMigration is the bulk project transfer workflow: export,
	// validate, and apply a logical project document. Excluded from the
	// agent profile; the entire point of the migration profile.
	groupMigration toolCapabilityGroup = "migration"
	// groupSync is incremental synchronization (get_changes). Excluded
	// from the agent profile alongside groupMigration.
	groupSync      toolCapabilityGroup = "sync"
	groupIssues    toolCapabilityGroup = "issues"
	groupPlanning  toolCapabilityGroup = "planning"
	groupReview    toolCapabilityGroup = "review"
	groupKnowledge toolCapabilityGroup = "knowledge"
	groupLifecycle toolCapabilityGroup = "lifecycle"
)

// toolProfileIncludes reports whether group (and, for the read-only
// profile, a tool's own readOnlyHint) belongs in profile's advertised
// catalog. read-only is deliberately derived from the same annotation
// hints ISSUE-53 established, rather than from a hand-maintained tool
// list, so the two can never drift apart: a tool's readOnlyHint is its own
// read-only profile membership decision.
func toolProfileIncludes(profile domain.ToolProfile, group toolCapabilityGroup, toolDef *sdkmcp.Tool) bool {
	if group == groupCore {
		return true
	}
	switch profile {
	case domain.ToolProfileFull:
		return true
	case domain.ToolProfileReadOnly:
		return toolDef != nil && toolDef.Annotations != nil && toolDef.Annotations.ReadOnlyHint
	case domain.ToolProfileMigration:
		return group == groupMigration
	case domain.ToolProfileAgent:
		return group != groupMigration && group != groupSync
	default:
		return false
	}
}

// registerTool builds toolDef exactly once and registers it with server
// through addFn only if the adapter's active profile includes group. A
// newly added tool call is required to pass a group here — there is no
// group-less registration path — so omitting a profile decision does not
// compile.
func (adapter *adapter) registerTool(server *sdkmcp.Server, group toolCapabilityGroup, toolDef *sdkmcp.Tool, addFn func(*sdkmcp.Tool)) {
	if !toolProfileIncludes(adapter.toolProfile, group, toolDef) {
		return
	}
	addFn(toolDef)
}
