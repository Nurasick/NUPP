// Package config reads the service's settings from environment variables.
//
// Following the "twelve-factor app" convention, configuration lives in the
// environment rather than in files committed to git. That keeps secrets such
// as the database password out of the repository and lets the same binary run
// unchanged on a laptop, in CI and in production.
package config

import "errors"

// Config holds everything the service needs at startup.
type Config struct {
	HTTPAddr    string // address to listen on, e.g. ":8080"
	DatabaseURL string // PostgreSQL connection URL (required)
	StorageDir  string // root directory for stored files
	Env         string // free-form label such as "development" or "production"
}

// Load builds a Config using getenv to look up variables.
//
// In production code getenv is os.Getenv; tests pass a fake. Taking the lookup
// function as a parameter (instead of calling os.Getenv inside) is a small
// form of dependency injection that keeps this function pure and testable.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:    valueOr(getenv("HTTP_ADDR"), ":8080"),
		DatabaseURL: getenv("DATABASE_URL"),
		StorageDir:  valueOr(getenv("STORAGE_DIR"), "./data/files"),
		Env:         valueOr(getenv("APP_ENV"), "development"),
	}
	// Fail fast: a service without a database can't do anything useful, so
	// refuse to start rather than failing on the first request.
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("config: DATABASE_URL is required")
	}
	return cfg, nil
}

// valueOr returns value, or fallback when value is empty.
func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
