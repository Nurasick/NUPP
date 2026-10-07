// Package config reads the service's settings from environment variables.
//
// Following the "twelve-factor app" convention, configuration lives in the
// environment rather than in files committed to git. That keeps secrets such
// as the database password out of the repository and lets the same binary run
// unchanged on a laptop, in CI and in production.
package config

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
)

// Config holds everything the service needs at startup.
type Config struct {
	HTTPAddr    string // address to listen on, e.g. ":8080"
	DatabaseURL string // PostgreSQL connection URL (required)
	StorageDir  string // root directory for stored files
	Env         string // free-form label such as "development" or "production"

	// TrustedProxies are the networks whose X-Forwarded-For header we
	// believe (hardening spec H-IP-2). Empty means "trust nobody".
	TrustedProxies []netip.Prefix

	RateLimit RateLimit

	// Concurrency caps (H-CC).
	MaxInflightAPI            int
	MaxInflightFiles          int
	MaxInflightFilesPerClient int

	// DBMaxConns is the size of the serving connection pool (H-DB-2).
	// int32 because that's the type pgxpool uses.
	DBMaxConns int32
}

// RateLimit configures the per-client token buckets (H-RL).
type RateLimit struct {
	Enabled    bool
	APIRate    float64 // tokens per second for JSON endpoints
	APIBurst   int     // bucket size for JSON endpoints
	FilesRate  float64 // tokens per second for file downloads
	FilesBurst int     // bucket size for file downloads
}

// Load builds a Config using getenv to look up variables.
//
// In production code getenv is os.Getenv; tests pass a fake. Taking the lookup
// function as a parameter (instead of calling os.Getenv inside) is a small
// form of dependency injection that keeps this function pure and testable.
//
// All problems are collected and reported together (errors.Join), so a
// misconfigured deployment shows every mistake at once, not one per restart.
func Load(getenv func(string) string) (Config, error) {
	p := parser{getenv: getenv}
	cfg := Config{
		HTTPAddr:    valueOr(getenv("HTTP_ADDR"), ":8080"),
		DatabaseURL: getenv("DATABASE_URL"),
		StorageDir:  valueOr(getenv("STORAGE_DIR"), "./data/files"),
		Env:         valueOr(getenv("APP_ENV"), "development"),

		TrustedProxies: p.prefixes("TRUSTED_PROXIES"),
		RateLimit: RateLimit{
			Enabled:    p.boolean("RATE_LIMIT_ENABLED", true),
			APIRate:    p.positiveFloat("RATE_LIMIT_API_RPS", 20),
			APIBurst:   p.positiveInt("RATE_LIMIT_API_BURST", 100),
			FilesRate:  p.positiveFloat("RATE_LIMIT_FILES_RPS", 30),
			FilesBurst: p.positiveInt("RATE_LIMIT_FILES_BURST", 300),
		},
		MaxInflightAPI:            p.positiveInt("MAX_INFLIGHT_API", 32),
		MaxInflightFiles:          p.positiveInt("MAX_INFLIGHT_FILES", 64),
		MaxInflightFilesPerClient: p.positiveInt("MAX_INFLIGHT_FILES_PER_CLIENT", 8),
		DBMaxConns:                int32(p.positiveIntMax("DB_MAX_CONNS", 10, math.MaxInt32)), // #nosec G115 -- bounded by positiveIntMax
	}
	// Fail fast: a service without a database can't do anything useful, so
	// refuse to start rather than failing on the first request.
	if cfg.DatabaseURL == "" {
		p.errs = append(p.errs, errors.New("DATABASE_URL is required"))
	}
	if err := errors.Join(p.errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// Warnings returns configuration that is valid but probably a mistake.
func (c Config) Warnings() []string {
	var w []string
	if c.Env == "production" && len(c.TrustedProxies) == 0 {
		w = append(w, "APP_ENV=production but TRUSTED_PROXIES is empty: behind a reverse proxy "+
			"every user would share one rate-limit bucket (set TRUSTED_PROXIES to the proxy network)")
	}
	return w
}

// parser reads typed values and remembers every error it meets.
type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) fail(name, raw, want string) {
	p.errs = append(p.errs, fmt.Errorf("%s=%q: must be %s", name, raw, want))
}

func (p *parser) boolean(name string, def bool) bool {
	raw := p.getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw) // accepts true/false, 1/0, t/f …
	if err != nil {
		p.fail(name, raw, "true or false")
		return def
	}
	return v
}

func (p *parser) positiveFloat(name string, def float64) float64 {
	raw := p.getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	// v > 0 is false for NaN, so NaN is rejected too; IsInf rejects "Inf".
	if err != nil || !(v > 0) || math.IsInf(v, 0) {
		p.fail(name, raw, "a positive number")
		return def
	}
	return v
}

func (p *parser) positiveInt(name string, def int) int {
	return p.positiveIntMax(name, def, math.MaxInt)
}

func (p *parser) positiveIntMax(name string, def, maxValue int) int {
	raw := p.getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > maxValue {
		p.fail(name, raw, fmt.Sprintf("a whole number between 1 and %d", maxValue))
		return def
	}
	return v
}

// prefixes parses a comma-separated list of CIDRs. A bare IP address is
// accepted and means exactly that one address (/32 or /128).
func (p *parser) prefixes(name string) []netip.Prefix {
	raw := p.getenv(name)
	var out []netip.Prefix
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			out = append(out, prefix.Masked()) // normalise 10.1.2.3/8 → 10.0.0.0/8
			continue
		}
		if addr, err := netip.ParseAddr(item); err == nil {
			out = append(out, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		p.fail(name, item, "a comma-separated list of IP addresses or CIDRs")
	}
	return out
}

// valueOr returns value, or fallback when value is empty.
func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
