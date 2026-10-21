// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
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
	// EnvTrustedProxies lists the reverse proxies (CIDR or single addresses)
	// whose X-Forwarded-For names the client; empty trusts none.
	EnvTrustedProxies = "PAPERLESS_GO_TRUSTED_PROXIES"
	// EnvOperator names who runs a command, for the audit log (not
	// verified). ./pgo sets it to the host user.
	EnvOperator = "PAPERLESS_GO_OPERATOR"
	// EnvMaxUploadSize is the largest file accepted by the upload, the
	// consume folder and the CLI import: bytes, or a number with KiB, MiB
	// or GiB.
	EnvMaxUploadSize = "PAPERLESS_GO_MAX_UPLOAD_SIZE"
)

// DefaultMaxUploadSize is the size limit when EnvMaxUploadSize is unset.
const DefaultMaxUploadSize int64 = 100 << 20

// Config holds the application configuration.
type Config struct {
	DatabaseURL string
	// DataDir holds the document files; metadata lives in PostgreSQL.
	DataDir  string
	LogLevel string
	// AllowedHosts for the web server; empty means the server's defaults.
	AllowedHosts []string
	// TrustedProxies may name the client in X-Forwarded-For, for the login
	// limits.
	TrustedProxies []netip.Prefix
	// MaxUploadSize is the largest file in bytes that is imported; a
	// file of exactly this size passes.
	MaxUploadSize int64
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
	var err error
	if cfg.TrustedProxies, err = ParseNetworks(EnvTrustedProxies, os.Getenv(EnvTrustedProxies)); err != nil {
		return nil, err
	}
	if cfg.MaxUploadSize, err = ParseSize(EnvMaxUploadSize, os.Getenv(EnvMaxUploadSize), DefaultMaxUploadSize); err != nil {
		return nil, err
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return cfg, nil
}

// sizeUnits are the suffixes ParseSize accepts.
var sizeUnits = []struct {
	suffix string
	shift  uint
}{{"KiB", 10}, {"MiB", 20}, {"GiB", 30}}

// ParseSize reads a positive size: a number of bytes, or a number followed
// by KiB, MiB or GiB (for example 100MiB). An empty value gives def; env
// names the variable in errors.
func ParseSize(env, v string, def int64) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	num, shift := v, uint(0)
	for _, u := range sizeUnits {
		if n, ok := strings.CutSuffix(v, u.suffix); ok {
			num, shift = strings.TrimSpace(n), u.shift
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 || n > (1<<62)>>shift {
		return 0, fmt.Errorf("%s: want a positive size in bytes or with KiB, MiB or GiB like 100MiB, got %q", env, v)
	}
	return n << shift, nil
}

// ParseNetworks reads a comma-separated list of CIDR networks and single
// addresses; env names the variable in errors. IPv4-mapped IPv6 addresses
// become IPv4.
func ParseNetworks(env, v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, n := range strings.Split(v, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		p, err := netip.ParsePrefix(n)
		if err != nil {
			a, aerr := netip.ParseAddr(n)
			if aerr != nil {
				return nil, fmt.Errorf("%s: want CIDR networks or addresses like 172.16.0.0/12 or 192.0.2.10, got %q", env, n)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		if p.Addr().Is4In6() && p.Bits() < 96 {
			return nil, fmt.Errorf("%s: %q is an IPv4-mapped prefix shorter than /96; write the IPv4 network instead", env, n)
		}
		out = append(out, netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-unmapBits(p)).Masked())
	}
	return out, nil
}

// unmapBits is how many prefix bits an IPv4-mapped IPv6 prefix loses when
// it is turned into an IPv4 prefix.
func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		return 96
	}
	return 0
}
