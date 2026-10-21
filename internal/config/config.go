// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/pklnx/paperless-go/internal/crypto"
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
	// EnvSecretKey is the 32-byte key (base64) that seals TOTP secrets and
	// keys the recovery code hashes; generate one with keygen. serve
	// refuses to start without it.
	EnvSecretKey = "PAPERLESS_GO_SECRET_KEY" //nolint:gosec // variable name, not a credential
	// EnvRequire2FA set to true makes two-factor login required for every
	// user; admins always need it.
	EnvRequire2FA = "PAPERLESS_GO_REQUIRE_2FA"
)

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
	// SecretKey seals TOTP secrets; nil if not configured. It is a
	// secret: never log it.
	SecretKey []byte
	// Require2FA makes two-factor login required for every user.
	Require2FA bool
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
	if raw := strings.TrimSpace(os.Getenv(EnvSecretKey)); raw != "" {
		key, err := crypto.ParseKey(raw)
		if err != nil {
			return nil, errors.New(EnvSecretKey + ": " + err.Error())
		}
		cfg.SecretKey = key
	}
	if cfg.Require2FA, err = parseBool(EnvRequire2FA, os.Getenv(EnvRequire2FA)); err != nil {
		return nil, err
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return cfg, nil
}

// Sealer returns a Sealer for the configured secret key, or an error that
// says how to create one.
func (c *Config) Sealer() (*crypto.Sealer, error) {
	if c.SecretKey == nil {
		return nil, errors.New(EnvSecretKey + " is not set: generate one with `paperless-go keygen` and set it in the environment (.env)")
	}
	return crypto.NewSealer(c.SecretKey)
}

// parseBool reads true or false (any case); empty is false.
func parseBool(env, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, fmt.Errorf("%s: want true or false, got %q", env, v)
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
