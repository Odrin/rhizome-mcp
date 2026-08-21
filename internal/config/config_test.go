package config_test

import (
	"log/slog"
	"testing"

	"rhizome-mcp/internal/config"
)

// fakeEnv builds a config.Getenv backed by a fixed map, so precedence and
// fallback behavior are testable without mutating the real process
// environment (t.Setenv would work too, but a pure function taking an
// injected lookup is the point of this package -- see docs/04 §15's
// broader injected-dependency convention, applied here to external
// process inputs rather than domain time).
func fakeEnv(values map[string]string) config.Getenv {
	return func(key string) string { return values[key] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, warnings := config.Load(fakeEnv(nil))
	if cfg.ServerName != "rhizome-mcp" {
		t.Errorf("ServerName = %q, want rhizome-mcp", cfg.ServerName)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want Info", cfg.LogLevel)
	}
	if cfg.HTTPAddress != "" || cfg.HTTPAddressFromEnv {
		t.Errorf("HTTPAddress = %q fromEnv=%v, want empty/false", cfg.HTTPAddress, cfg.HTTPAddressFromEnv)
	}
	if cfg.ToolProfile != "full" || cfg.ToolProfileFromEnv {
		t.Errorf("ToolProfile = %q fromEnv=%v, want full/false", cfg.ToolProfile, cfg.ToolProfileFromEnv)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestLoadNamespacedKeysTakePrecedenceOverLegacy(t *testing.T) {
	cfg, warnings := config.Load(fakeEnv(map[string]string{
		"RHIZOME_SERVER_NAME":  "namespaced-name",
		"SERVER_NAME":          "legacy-name",
		"RHIZOME_LOG_LEVEL":    "debug",
		"LOG_LEVEL":            "error",
		"RHIZOME_HTTP_ADDRESS": "127.0.0.1:9000",
		"HTTP_ADDRESS":         "127.0.0.1:1",
		"RHIZOME_TOOL_PROFILE": "agent",
		"TOOL_PROFILE":         "read-only",
	}))
	if cfg.ServerName != "namespaced-name" {
		t.Errorf("ServerName = %q, want the namespaced value", cfg.ServerName)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want Debug", cfg.LogLevel)
	}
	if cfg.HTTPAddress != "127.0.0.1:9000" || !cfg.HTTPAddressFromEnv {
		t.Errorf("HTTPAddress = %q fromEnv=%v, want the namespaced value/true", cfg.HTTPAddress, cfg.HTTPAddressFromEnv)
	}
	if cfg.ToolProfile != "agent" || !cfg.ToolProfileFromEnv {
		t.Errorf("ToolProfile = %q fromEnv=%v, want the namespaced value/true", cfg.ToolProfile, cfg.ToolProfileFromEnv)
	}
	// Namespaced keys present means the legacy fallback was never reached,
	// so no deprecation warning fires.
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none (namespaced keys supplied)", warnings)
	}
}

func TestLoadFallsBackToLegacyAndWarns(t *testing.T) {
	cfg, warnings := config.Load(fakeEnv(map[string]string{
		"HTTP_ADDRESS": "127.0.0.1:1",
		"TOOL_PROFILE": "read-only",
	}))
	if cfg.HTTPAddress != "127.0.0.1:1" || !cfg.HTTPAddressFromEnv {
		t.Errorf("HTTPAddress = %q fromEnv=%v, want the legacy value/true", cfg.HTTPAddress, cfg.HTTPAddressFromEnv)
	}
	if cfg.ToolProfile != "read-only" || !cfg.ToolProfileFromEnv {
		t.Errorf("ToolProfile = %q fromEnv=%v, want the legacy value/true", cfg.ToolProfile, cfg.ToolProfileFromEnv)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want exactly 2 (one per legacy variable used)", warnings)
	}
	for _, warning := range warnings {
		if warning == "" {
			t.Errorf("warning is empty: %v", warnings)
		}
	}
}

func TestLoadConsolidatesExternalInputs(t *testing.T) {
	cfg, _ := config.Load(fakeEnv(map[string]string{
		"XDG_DATA_HOME":        "/xdg",
		"LOCALAPPDATA":         `C:\local`,
		"RHIZOME_PROJECT_ROOT": "/project",
	}))
	if cfg.XDGDataHome != "/xdg" {
		t.Errorf("XDGDataHome = %q, want /xdg", cfg.XDGDataHome)
	}
	if cfg.LocalAppData != `C:\local` {
		t.Errorf("LocalAppData = %q", cfg.LocalAppData)
	}
	if cfg.ProjectRoot != "/project" {
		t.Errorf("ProjectRoot = %q, want /project", cfg.ProjectRoot)
	}
}

func TestLoadLogLevelParsing(t *testing.T) {
	tests := []struct {
		value string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"nonsense", slog.LevelInfo},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			cfg, _ := config.Load(fakeEnv(map[string]string{"RHIZOME_LOG_LEVEL": test.value}))
			if cfg.LogLevel != test.want {
				t.Errorf("LogLevel(%q) = %v, want %v", test.value, cfg.LogLevel, test.want)
			}
		})
	}
}
