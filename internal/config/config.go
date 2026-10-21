// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"os"
	"strings"
)

// Environment variable names.
const (
	EnvDatabaseURL = "PAPERLESS_GO_DATABASE_URL"
	EnvDataDir     = "PAPERLESS_GO_DATA_DIR"
	EnvLogLevel    = "PAPERLESS_GO_LOG_LEVEL"
	// EnvAllowedHosts is a comma-separated list of host names the web server
	// accepts in the Host header (protection against DNS rebinding).
	EnvAllowedHosts = "PAPERLESS_GO_ALLOWED_HOSTS"
)

// Config holds the application configuration.
type Config struct {
	DatabaseURL string
	// DataDir holds the document files; metadata lives in PostgreSQL.
	DataDir  string
	LogLevel string
	// AllowedHosts for the web server; empty means the server's defaults.
	AllowedHosts []string
}

// DataDirFromEnv returns the data directory, ./data unless set.
func DataDirFromEnv() string {
	if d := os.Getenv(EnvDataDir); d != "" {
		return d
	}
	return "./data"
}

// Load reads the configuration from the environment.
func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: os.Getenv(EnvDatabaseURL),
		DataDir:     DataDirFromEnv(),
		LogLevel:    os.Getenv(EnvLogLevel),
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New(EnvDatabaseURL + " is not set")
	}
	for _, h := range strings.Split(os.Getenv(EnvAllowedHosts), ",") {
		if h = strings.TrimSpace(h); h != "" {
			cfg.AllowedHosts = append(cfg.AllowedHosts, h)
		}
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return cfg, nil
}
