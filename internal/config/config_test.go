package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
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
	t.Setenv(EnvMaxUploadSize, "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "./data" || cfg.LogLevel != "info" || cfg.AllowedHosts != nil || cfg.TrustedProxies != nil ||
		cfg.MaxUploadSize != 100<<20 {
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
	// Shorter than /96 cannot become an IPv4 prefix; it must not silently
	// turn into a prefix that matches nothing.
	if _, err := ParseNetworks(EnvTrustedProxies, "::ffff:0:0/80"); err == nil || !strings.Contains(err.Error(), "/96") {
		t.Errorf("short IPv4-mapped prefix: %v", err)
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

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"": 7, "1": 1, " 104857600 ": 100 << 20, "100MiB": 100 << 20, "512 KiB": 512 << 10, "2GiB": 2 << 30,
	} {
		if got, err := ParseSize(EnvMaxUploadSize, in, 7); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "-1", "abc", "1.5MiB", "10MB", "MiB", "9223372036854775807", "8589934592GiB"} {
		if _, err := ParseSize(EnvMaxUploadSize, in, 7); err == nil || !strings.Contains(err.Error(), EnvMaxUploadSize) {
			t.Errorf("ParseSize(%q) error = %v, want one naming %s", in, err, EnvMaxUploadSize)
		}
	}
}

func TestLoadMaxUploadSize(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	t.Setenv(EnvMaxUploadSize, "20MiB")
	cfg, err := Load()
	if err != nil || cfg.MaxUploadSize != 20<<20 {
		t.Fatalf("Load() = %+v, %v", cfg, err)
	}
	t.Setenv(EnvMaxUploadSize, "lots")
	if _, err := Load(); err == nil {
		t.Error("Load() accepted an invalid size")
	}
}

func TestLoadOCR(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	for _, k := range []string{EnvOCRWorkers, EnvOCRLanguages, EnvOCRTimeout} {
		t.Setenv(k, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OCRWorkers != 1 || cfg.OCRLanguages != "deu+eng" || cfg.OCRTimeout != 30*time.Minute {
		t.Errorf("defaults: %d, %q, %s", cfg.OCRWorkers, cfg.OCRLanguages, cfg.OCRTimeout)
	}

	t.Setenv(EnvOCRWorkers, " 0 ")
	t.Setenv(EnvOCRLanguages, "deu+eng+chi_sim+deu_latf")
	t.Setenv(EnvOCRTimeout, "2h")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.OCRWorkers != 0 || cfg.OCRLanguages != "deu+eng+chi_sim+deu_latf" || cfg.OCRTimeout != 2*time.Hour {
		t.Errorf("set: %d, %q, %s", cfg.OCRWorkers, cfg.OCRLanguages, cfg.OCRTimeout)
	}

	for env, values := range map[string][]string{
		EnvOCRWorkers:   {"-1", "17", "two"},
		EnvOCRLanguages: {"deu eng", "deu,eng", "+deu", "deu+", "DEU", "deu;rm", "-l"},
		EnvOCRTimeout:   {"30", "59s", "-1h", "soon"},
	} {
		for _, v := range values {
			t.Setenv(EnvOCRWorkers, "")
			t.Setenv(EnvOCRLanguages, "")
			t.Setenv(EnvOCRTimeout, "")
			t.Setenv(env, v)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), env) {
				t.Errorf("%s=%q: %v", env, v, err)
			}
		}
	}
}

func TestLoadSecretKey(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	t.Setenv(EnvSecretKey, "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SecretKey != nil {
		t.Fatalf("SecretKey without the variable: %v", cfg.SecretKey)
	}
	if _, err := cfg.Sealer(); err == nil || !strings.Contains(err.Error(), "keygen") {
		t.Errorf("Sealer without a key: %v", err)
	}

	key := strings.Repeat("A", 43) + "=" // 32 zero bytes
	t.Setenv(EnvSecretKey, " "+key+"\n")
	if cfg, err = Load(); err != nil || len(cfg.SecretKey) != 32 {
		t.Fatalf("valid key: %v, %v", cfg, err)
	}
	if _, err := cfg.Sealer(); err != nil {
		t.Errorf("Sealer: %v", err)
	}
	for _, bad := range []string{"not base64!", "c2hvcnQ="} {
		t.Setenv(EnvSecretKey, bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvSecretKey) {
			t.Errorf("key %q: %v", bad, err)
		}
	}
}

func TestLoadRequire2FA(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	for v, want := range map[string]bool{"": false, "false": false, "TRUE": true, " true ": true} {
		t.Setenv(EnvRequire2FA, v)
		cfg, err := Load()
		if err != nil || cfg.Require2FA != want {
			t.Errorf("%q: %v, %v", v, cfg, err)
		}
	}
	t.Setenv(EnvRequire2FA, "yes")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvRequire2FA) {
		t.Errorf("yes: %v", err)
	}
}

func TestParsePublicURL(t *testing.T) {
	for _, c := range []struct{ in, origin, rpID string }{
		{"https://docs.example.ts.net", "https://docs.example.ts.net", "docs.example.ts.net"},
		{"https://Docs.Example.ts.net:8443/", "https://docs.example.ts.net:8443", "docs.example.ts.net"},
		{"https://docs.example.ts.net:443", "https://docs.example.ts.net", "docs.example.ts.net"},
		{"http://localhost:8080", "http://localhost:8080", "localhost"},
	} {
		origin, rpID, err := ParsePublicURL(c.in)
		if err != nil || origin != c.origin || rpID != c.rpID {
			t.Errorf("%s: %q %q %v", c.in, origin, rpID, err)
		}
	}
	for _, in := range []string{
		"http://docs.example.ts.net", "https://192.168.1.10", "https://[::1]:8080", "http://127.0.0.1:8080",
		"https://docs.example.ts.net/paperless", "https://docs.example.ts.net?x=1", "https://user@docs.example.ts.net",
		"docs.example.ts.net", "ftp://localhost", "https://docs.example.ts.net#x",
	} {
		if _, _, err := ParsePublicURL(in); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}

func TestLoadPublicURL(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	t.Setenv(EnvPublicURL, " https://Docs.Example.ts.net:443/ ")
	cfg, err := Load()
	if err != nil || cfg.PublicURL != "https://docs.example.ts.net" {
		t.Fatalf("public URL: %+v %v", cfg, err)
	}
	t.Setenv(EnvPublicURL, "http://docs.example.ts.net")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvPublicURL) {
		t.Fatalf("plain http accepted: %v", err)
	}
	t.Setenv(EnvPublicURL, "")
	if cfg, err := Load(); err != nil || cfg.PublicURL != "" {
		t.Fatalf("without: %+v %v", cfg, err)
	}
}
