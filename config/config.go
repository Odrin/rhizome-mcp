package config

import (
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	ServerName  string
	Version     string
	LogLevel    slog.Level
	HTTPAddress string
}

func Load() *Config {
	return &Config{
		ServerName:  getEnv("SERVER_NAME", "rhizome-mcp"),
		Version:     getEnv("VERSION", "v1.0.0"),
		LogLevel:    parseLevel(getEnv("LOG_LEVEL", "info")),
		HTTPAddress: getEnv("HTTP_ADDRESS", ""),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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
