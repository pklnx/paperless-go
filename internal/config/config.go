// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	// EnvOCRWorkers is how many documents serve processes at once; 0 turns
	// processing in serve off.
	EnvOCRWorkers = "PAPERLESS_GO_OCR_WORKERS"
	// EnvOCRLanguages are the Tesseract languages, joined with "+".
	EnvOCRLanguages = "PAPERLESS_GO_OCR_LANGUAGES"
	// EnvOCRTimeout limits each processing attempt (a Go duration).
	EnvOCRTimeout = "PAPERLESS_GO_OCR_TIMEOUT"
)

// Processing defaults.
const (
	DefaultOCRWorkers   = 1
	DefaultOCRLanguages = "deu+eng"
	DefaultOCRTimeout   = 30 * time.Minute
	// MaxOCRWorkers bounds PAPERLESS_GO_OCR_WORKERS: each worker runs its
	// tools on every CPU.
	MaxOCRWorkers = 16
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
	// OCRWorkers is how many documents serve processes at once (0: none).
	OCRWorkers int
	// OCRLanguages are Tesseract language codes joined with "+".
	OCRLanguages string
	// OCRTimeout limits each processing attempt.
	OCRTimeout time.Duration
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
	if cfg.OCRWorkers, err = parseWorkers(os.Getenv(EnvOCRWorkers)); err != nil {
		return nil, err
	}
	if cfg.OCRLanguages, err = ParseLanguages(os.Getenv(EnvOCRLanguages)); err != nil {
		return nil, err
	}
	if cfg.OCRTimeout, err = parseTimeout(os.Getenv(EnvOCRTimeout)); err != nil {
		return nil, err
	}
	return cfg, nil
}

func parseWorkers(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultOCRWorkers, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > MaxOCRWorkers {
		return 0, fmt.Errorf("%s: want a number from 0 to %d, got %q", EnvOCRWorkers, MaxOCRWorkers, v)
	}
	return n, nil
}

// languages matches Tesseract languages (deu, eng, chi_sim, deu_latf) joined with +.
var languages = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}(\+[a-z][a-z0-9_]{1,31})*$`)

// ParseLanguages checks a list of Tesseract languages joined with "+",
// such as deu+eng; empty means DefaultOCRLanguages. Whether Tesseract has
// them is checked when serve starts.
func ParseLanguages(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultOCRLanguages, nil
	}
	if !languages.MatchString(v) {
		return "", fmt.Errorf("%s: want Tesseract languages joined with +, such as deu+eng, got %q", EnvOCRLanguages, v)
	}
	return v, nil
}

func parseTimeout(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultOCRTimeout, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("%s: want a duration of at least 1m, such as 30m or 2h, got %q", EnvOCRTimeout, v)
	}
	return d, nil
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
