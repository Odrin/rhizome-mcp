// Package config resolves rhizome-mcp's external process-environment
// inputs (server name, log level, HTTP transport address, tool exposure
// profile, plus the platform data-directory and project-root discovery
// inputs) into one immutable Config, so this is the single place that
// describes every environment variable the server reads.
package config

import (
	"fmt"
	"log/slog"
	"strings"
)

// Getenv is the environment lookup rhizome-mcp injects into Load, so it can
// be replaced with a deterministic fake in tests instead of mutating the
// real process environment.
type Getenv func(string) string

type Config struct {
	ServerName    string
	Version       string
	VersionCommit string
	VersionDate   string
	LogLevel      slog.Level
	HTTPAddress   string
	// ToolProfile selects which capability groups of the MCP tool catalog
	// this server instance advertises (full, agent, read-only, migration).
	// Defaults to "full" so an unconfigured server keeps the complete
	// existing tool catalog.
	ToolProfile string

	// HTTPAddressFromEnv and ToolProfileFromEnv report whether the field
	// was populated from an environment variable (namespaced or the
	// deprecated legacy fallback), rather than left at its built-in
	// default. A caller (serve) uses these to warn when transport
	// selection or catalog narrowing was implicit rather than passed as
	// an explicit flag -- the two environment inputs capable of silently
	// changing what a client sees.
	HTTPAddressFromEnv bool
	ToolProfileFromEnv bool

	// XDGDataHome, LocalAppData, and ProjectRoot are read here so every
	// external environment input has one source of truth, even though
	// they are consumed by projectconfig, not this package. XDGDataHome
	// and LocalAppData follow their platforms' own (unprefixed) naming;
	// ProjectRoot is RHIZOME_PROJECT_ROOT, already namespaced.
	XDGDataHome  string
	LocalAppData string
	ProjectRoot  string
}

// envFallback is one environment input with a namespaced primary key and a
// deprecated unprefixed legacy key kept as a fallback for one release.
type envFallback struct {
	namespacedKey string
	legacyKey     string
}

// lookup resolves one envFallback's value and reports whether it came from
// either environment key, plus a ready-to-print deprecation warning when
// the legacy key was the one that supplied it.
func (fallback envFallback) lookup(getenv Getenv) (value string, fromEnv bool, warning string) {
	if v := getenv(fallback.namespacedKey); v != "" {
		return v, true, ""
	}
	if v := getenv(fallback.legacyKey); v != "" {
		return v, true, fmt.Sprintf(
			"warning: %s is deprecated and will stop working in a future release; use %s instead",
			fallback.legacyKey, fallback.namespacedKey,
		)
	}
	return "", false, ""
}

// Load resolves Config from getenv with precedence flag > RHIZOME_* >
// legacy unprefixed name > built-in default (the flag step happens later,
// in the CLI layer, once command-line flags are parsed). It returns
// deprecation warnings for any legacy unprefixed variable actually used,
// ready to print to stderr.
func Load(getenv Getenv) (*Config, []string) {
	var warnings []string
	take := func(fallback envFallback) (string, bool) {
		value, fromEnv, warning := fallback.lookup(getenv)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		return value, fromEnv
	}

	serverName, _ := take(envFallback{"RHIZOME_SERVER_NAME", "SERVER_NAME"})
	if serverName == "" {
		serverName = "rhizome-mcp"
	}
	logLevelText, _ := take(envFallback{"RHIZOME_LOG_LEVEL", "LOG_LEVEL"})
	httpAddress, httpFromEnv := take(envFallback{"RHIZOME_HTTP_ADDRESS", "HTTP_ADDRESS"})
	toolProfile, toolProfileFromEnv := take(envFallback{"RHIZOME_TOOL_PROFILE", "TOOL_PROFILE"})
	if toolProfile == "" {
		toolProfile = "full"
	}

	return &Config{
		ServerName:         serverName,
		LogLevel:           parseLevel(logLevelText),
		HTTPAddress:        httpAddress,
		HTTPAddressFromEnv: httpFromEnv,
		ToolProfile:        toolProfile,
		ToolProfileFromEnv: toolProfileFromEnv,
		XDGDataHome:        getenv("XDG_DATA_HOME"),
		LocalAppData:       getenv("LOCALAPPDATA"),
		ProjectRoot:        getenv("RHIZOME_PROJECT_ROOT"),
	}, warnings
}

func parseLevel(v string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
