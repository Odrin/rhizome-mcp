package domain

import (
	"fmt"
	"strings"
)

// ContentMode controls how MCP tool results populate CallToolResult.Content
// and CallToolResult.StructuredContent (and whether tools/list advertises
// OutputSchema).
type ContentMode string

const (
	// ContentModeAuto populates StructuredContent with the output DTO,
	// advertises OutputSchema in tools/list, and populates Content[0].Text
	// with the compact serialized JSON payload whenever the serialized
	// payload fits within the 256 KiB text cap, falling back to the enriched
	// human-readable summary string when an opt-in response exceeds 256 KiB.
	// This is the default when no mode is configured.
	ContentModeAuto ContentMode = "auto"
	// ContentModeStructured populates StructuredContent with the output DTO,
	// advertises OutputSchema in tools/list, and sets Content[0].Text to
	// only the enriched human-readable summary string (zero JSON duplication
	// on the wire).
	ContentModeStructured ContentMode = "structured"
	// ContentModeText populates Content[0].Text with the compact serialized
	// JSON payload, omits StructuredContent, and omits OutputSchema from
	// tools/list for Content-only MCP hosts.
	ContentModeText ContentMode = "text"
)

// AllContentModes lists every supported content mode name in the
// deterministic order used for error messages and documentation.
var AllContentModes = []ContentMode{ContentModeAuto, ContentModeStructured, ContentModeText}

// Valid reports whether mode is one of the supported names.
func (mode ContentMode) Valid() bool {
	switch mode {
	case ContentModeAuto, ContentModeStructured, ContentModeText:
		return true
	default:
		return false
	}
}

// ParseContentMode parses a configured content mode name. A blank value
// defaults to ContentModeAuto so an unconfigured server serves both
// Content-only and StructuredContent-native MCP hosts out of the box. Any
// other unsupported value returns a structured configuration error listing
// the valid names.
func ParseContentMode(value string) (ContentMode, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ContentModeAuto, nil
	}
	mode := ContentMode(trimmed)
	if !mode.Valid() {
		names := make([]string, len(AllContentModes))
		for index, candidate := range AllContentModes {
			names[index] = string(candidate)
		}
		return "", NewError(CodeInvalidArgument,
			fmt.Sprintf("unsupported content mode %q (valid modes: %s)", value, strings.Join(names, ", ")), false,
			Detail{Field: "content_mode", Code: "INVALID_ENUM"})
	}
	return mode, nil
}
