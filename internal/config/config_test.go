package config

import (
	"slices"
	"testing"
)

func TestLoadNeedsDatabaseURL(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "")
	if _, err := Load(); err == nil || err.Error() != EnvDatabaseURL+" is not set" {
		t.Errorf("Load() error = %v, want %s is not set", err, EnvDatabaseURL)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvLogLevel, "")
	t.Setenv(EnvAllowedHosts, "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "./data" || cfg.LogLevel != "info" || cfg.AllowedHosts != nil {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestLoadAllowedHosts(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	t.Setenv(EnvAllowedHosts, " localhost, ,docs.example.ts.net ,")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"localhost", "docs.example.ts.net"}; !slices.Equal(cfg.AllowedHosts, want) {
		t.Errorf("AllowedHosts = %q, want %q", cfg.AllowedHosts, want)
	}
}

func TestDataDirFromEnv(t *testing.T) {
	t.Setenv(EnvDataDir, "/data")
	if got := DataDirFromEnv(); got != "/data" {
		t.Errorf("DataDirFromEnv() = %q", got)
	}
}
