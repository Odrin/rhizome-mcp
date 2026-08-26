package domain

import (
	"fmt"
	"strings"
)

// Toolset names one capability group of the MCP tool catalog. Every
// registered tool declares exactly one Toolset (internal/adapters/mcp
// registration), and `serve --toolsets` selects a free-form combination of
// them to advertise instead of a named ToolProfile. Like ToolProfile, a
// toolset selection is an exposure and prompt-size control, not an
// authorization boundary: every tool still enforces its own domain-level
// validation regardless of whether a client can see it in tools/list.
type Toolset string

const (
	// ToolsetCore is project opening and metadata (open_project,
	// get_project). It is always advertised regardless of the configured
	// selection, so a client can establish explicit routing and diagnose a
	// missing tool; naming it in a selection is valid but redundant.
	ToolsetCore Toolset = "core"
	// ToolsetIssues is issue CRUD, labels, relations, and graph reads.
	ToolsetIssues Toolset = "issues"
	// ToolsetPlanning is batch issue plan validation and apply.
	ToolsetPlanning Toolset = "planning"
	// ToolsetReview is review request management.
	ToolsetReview Toolset = "review"
	// ToolsetKnowledge is comments, decisions, activity, and search.
	ToolsetKnowledge Toolset = "knowledge"
	// ToolsetLifecycle is the leased work attempt lifecycle, resource
	// reservations, and gate evidence/evaluation.
	ToolsetLifecycle Toolset = "lifecycle"
	// ToolsetGovernance is project-wide workflow policy administration.
	ToolsetGovernance Toolset = "governance"
	// ToolsetMigration is the bulk project transfer workflow: export,
	// validate, and apply a logical project document.
	ToolsetMigration Toolset = "migration"
	// ToolsetSync is incremental synchronization (get_changes).
	ToolsetSync Toolset = "sync"
)

// AllToolsets lists every capability group in the deterministic, canonical
// order used for error messages, documentation, and reporting.
var AllToolsets = []Toolset{
	ToolsetCore,
	ToolsetIssues,
	ToolsetPlanning,
	ToolsetReview,
	ToolsetKnowledge,
	ToolsetLifecycle,
	ToolsetGovernance,
	ToolsetMigration,
	ToolsetSync,
}

// Valid reports whether toolset is one of the supported group names.
func (toolset Toolset) Valid() bool {
	for _, candidate := range AllToolsets {
		if toolset == candidate {
			return true
		}
	}
	return false
}

// toolsetNames renders AllToolsets for error messages.
func toolsetNames() string {
	names := make([]string, len(AllToolsets))
	for index, candidate := range AllToolsets {
		names[index] = string(candidate)
	}
	return strings.Join(names, ", ")
}

// ParseToolsets parses a comma-separated toolset selection, in input order.
// A blank value returns nil (no selection configured), so callers can
// distinguish "unconfigured" from an explicit selection. Surrounding
// whitespace per entry is tolerated; an empty entry, an unsupported name,
// or a duplicate is a structured, actionable configuration error suitable
// for rejecting server startup before any transport opens.
func ParseToolsets(value string) ([]Toolset, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	entries := strings.Split(value, ",")
	selected := make([]Toolset, 0, len(entries))
	seen := make(map[Toolset]bool, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return nil, NewError(CodeInvalidArgument,
				fmt.Sprintf("toolset list %q contains an empty entry (valid toolsets: %s)", value, toolsetNames()), false,
				Detail{Field: "toolsets", Code: "INVALID_ENUM"})
		}
		toolset := Toolset(trimmed)
		if !toolset.Valid() {
			return nil, NewError(CodeInvalidArgument,
				fmt.Sprintf("unsupported toolset %q (valid toolsets: %s)", trimmed, toolsetNames()), false,
				Detail{Field: "toolsets", Code: "INVALID_ENUM"})
		}
		if seen[toolset] {
			return nil, NewError(CodeInvalidArgument,
				fmt.Sprintf("toolset %q is listed more than once", trimmed), false,
				Detail{Field: "toolsets", Code: "DUPLICATE_VALUE"})
		}
		seen[toolset] = true
		selected = append(selected, toolset)
	}
	return selected, nil
}
