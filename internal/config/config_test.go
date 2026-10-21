package config

import (
	"fmt"
	"slices"
	"strings"
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
	t.Setenv(EnvTrustedProxies, "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "./data" || cfg.LogLevel != "info" || cfg.AllowedHosts != nil || cfg.TrustedProxies != nil {
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

func TestParseNetworks(t *testing.T) {
	got, err := ParseNetworks(EnvTrustedProxies, " 172.16.0.0/12, 192.0.2.10 ,::ffff:10.0.0.1, fd00::/8,::ffff:10.0.0.0/104")
	if err != nil {
		t.Fatal(err)
	}
	want := "[172.16.0.0/12 192.0.2.10/32 10.0.0.1/32 fd00::/8 10.0.0.0/8]"
	if fmt.Sprint(got) != want {
		t.Errorf("got %v, want %s", got, want)
	}
	if got, err := ParseNetworks(EnvTrustedProxies, ""); err != nil || got != nil {
		t.Errorf("empty: %v, %v", got, err)
	}
	if _, err := ParseNetworks(EnvTrustedProxies, "proxy.local"); err == nil || !strings.Contains(err.Error(), EnvTrustedProxies) {
		t.Errorf("host name: %v", err)
	}
}

func TestLoadTrustedProxies(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	t.Setenv(EnvTrustedProxies, "172.16.0.0/12")
	cfg, err := Load()
	if err != nil || fmt.Sprint(cfg.TrustedProxies) != "[172.16.0.0/12]" {
		t.Fatalf("Load: %+v, %v", cfg, err)
	}
	t.Setenv(EnvTrustedProxies, "nope")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvTrustedProxies) {
		t.Errorf("invalid: %v", err)
	}
}
