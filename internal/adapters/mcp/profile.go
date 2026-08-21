package mcp

import (
	"context"
	"encoding/json"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

type agentSessionContextKey struct{}

// toolCapabilityGroup is the one mandatory, explicit classification every
// registered tool must declare through registerTool. It is reviewed
// alongside the ISSUE-53 annotation hints whenever a tool is added: a new
// tool cannot be registered without picking a group, since registerTool
// requires one.
type toolCapabilityGroup string

const (
	// groupCore tools are advertised in every profile, including
	// migration and agent: open_project establishes explicit routing and
	// get_project reports the active profile, so clients retain both tools.
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
//
// The read-only check is evaluated before the groupCore bypass below, on
// purpose (ISSUE-99): groupCore's "always advertised" rule exists so a
// client can always open a project and reach get_project to diagnose a
// missing tool, not so a future mutating core tool could slip into the read-only profile
// without satisfying ReadOnlyHint. Every other profile still treats
// groupCore as unconditional.
func toolProfileIncludes(profile domain.ToolProfile, group toolCapabilityGroup, toolDef *sdkmcp.Tool) bool {
	if profile == domain.ToolProfileReadOnly {
		return toolDef != nil && toolDef.Annotations != nil && toolDef.Annotations.ReadOnlyHint
	}
	if group == groupCore {
		return true
	}
	switch profile {
	case domain.ToolProfileFull:
		return true
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

// touchSessionForMutatingTool wraps handler so it durably touches
// agent_sessions.last_seen_at only when toolDef's own readOnlyHint is
// false (ISSUE-53's annotation, the same one toolProfileIncludes uses for
// the read-only profile). A tool advertised as readOnlyHint: true must not
// perform any durable write as part of its invocation — that was the
// ISSUE-99 bug: every handler called touchSession first regardless of its
// own annotation, so "read-only" tools silently mutated
// agent_sessions.last_seen_at. Deriving the decision from toolDef here,
// exactly like the read-only profile filter does, means every tool is
// covered structurally: a newly added read-only tool is exempt
// automatically, and a newly added mutating tool keeps the existing
// touch-on-every-call behavior, so session activity tracking stays
// correlated with actual database writes rather than with every call
// including reads.
func touchSessionForMutatingTool[In, Out any](adapter *adapter, toolDef *sdkmcp.Tool, handler func(context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, Out, error)) func(context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, Out, error) {
	return func(ctx context.Context, request *sdkmcp.CallToolRequest, input In) (*sdkmcp.CallToolResult, Out, error) {
		var handle string
		if request != nil && request.Params != nil && len(request.Params.Arguments) != 0 {
			var arguments map[string]json.RawMessage
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				domainErr := domain.NewError(domain.CodeInvalidArgument, "agent_session_handle must be a string", false,
					domain.Detail{Field: "agent_session_handle", Code: "INVALID_HANDLE"})
				result, _, ferr := adapter.failure(domainErr)
				return result, *new(Out), ferr
			}
			if value, ok := arguments["agent_session_handle"]; ok && string(value) != "null" {
				if err := json.Unmarshal(value, &handle); err != nil {
					domainErr := domain.NewError(domain.CodeInvalidArgument, "agent_session_handle must be a string", false,
						domain.Detail{Field: "agent_session_handle", Code: "INVALID_HANDLE"})
					result, _, ferr := adapter.failure(domainErr)
					return result, *new(Out), ferr
				}
			}
		}
		if handle != "" {
			var sessionID string
			var err error
			if toolDef != nil && toolDef.Annotations != nil && toolDef.Annotations.ReadOnlyHint {
				sessionID, err = adapter.sessions.ResolveHandle(ctx, handle)
			} else {
				sessionID, err = adapter.sessions.ResolveAndTouch(ctx, handle)
			}
			if err != nil {
				result, _, ferr := adapter.failure(err)
				return result, *new(Out), ferr
			}
			ctx = context.WithValue(ctx, agentSessionContextKey{}, sessionID)
		}
		return handler(ctx, request, input)
	}
}
