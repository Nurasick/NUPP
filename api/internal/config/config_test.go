package config_test

import (
	"testing"

	"github.com/Nurasick/NUPP/api/internal/config"
)

// env builds a fake getenv from a map. Passing a function instead of reading
// os.Getenv directly lets tests run in parallel without touching the real
// process environment.
func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestLoad_RequiresDatabaseURL(t *testing.T) {
	if _, err := config.Load(env(nil)); err == nil {
		t.Fatal("expected an error when DATABASE_URL is missing")
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{"DATABASE_URL": "postgres://localhost/nupp"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.Config{
		HTTPAddr:    ":8080",
		DatabaseURL: "postgres://localhost/nupp",
		StorageDir:  "./data/files",
		Env:         "development",
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoad_ReadsOverrides(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{
		"DATABASE_URL": "postgres://db/nupp",
		"HTTP_ADDR":    ":9000",
		"STORAGE_DIR":  "/data/files",
		"APP_ENV":      "production",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":9000" || cfg.StorageDir != "/data/files" || cfg.Env != "production" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}
