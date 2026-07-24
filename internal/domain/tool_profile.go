package domain

import (
	"fmt"
	"strings"
)

// ToolProfile selects which capability groups of the MCP tool catalog a
// server instance advertises at startup. It is an exposure and prompt-size
// control, not an authorization boundary: every tool still enforces its own
// domain-level validation regardless of whether a client can see it in
// tools/list.
type ToolProfile string

const (
	// ToolProfileFull advertises every supported tool. This is the default
	// when no profile is configured, preserving backward compatibility.
	ToolProfileFull ToolProfile = "full"
	// ToolProfileAgent advertises ordinary issue discovery, planning,
	// knowledge, review, and leased work lifecycle tools, excluding bulk
	// project transfer and incremental synchronization operations.
	ToolProfileAgent ToolProfile = "agent"
	// ToolProfileReadOnly advertises only operations guaranteed not to
	// write durable state, per the MCP tool annotation matrix.
	ToolProfileReadOnly ToolProfile = "read-only"
	// ToolProfileMigration advertises only the minimal project metadata,
	// export, validate, and apply import workflow needed for project
	// transfer.
	ToolProfileMigration ToolProfile = "migration"
)

// AllToolProfiles lists every supported profile name in the deterministic
// order used for error messages and documentation.
var AllToolProfiles = []ToolProfile{ToolProfileFull, ToolProfileAgent, ToolProfileReadOnly, ToolProfileMigration}

// Valid reports whether profile is one of the supported names.
func (profile ToolProfile) Valid() bool {
	switch profile {
	case ToolProfileFull, ToolProfileAgent, ToolProfileReadOnly, ToolProfileMigration:
		return true
	default:
		return false
	}
}

// ParseToolProfile parses a configured tool profile name. A blank value
// defaults to ToolProfileFull so that an unconfigured server keeps
// advertising the complete existing tool catalog. Any other unsupported
// value is a structured, actionable configuration error listing the valid
// names, suitable for rejecting server startup before any transport opens.
func ParseToolProfile(value string) (ToolProfile, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ToolProfileFull, nil
	}
	profile := ToolProfile(trimmed)
	if !profile.Valid() {
		names := make([]string, len(AllToolProfiles))
		for index, candidate := range AllToolProfiles {
			names[index] = string(candidate)
		}
		return "", NewError(CodeInvalidArgument,
			fmt.Sprintf("unsupported tool profile %q (valid profiles: %s)", value, strings.Join(names, ", ")), false,
			Detail{Field: "tool_profile", Code: "INVALID_ENUM"})
	}
	return profile, nil
}
