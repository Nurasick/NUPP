package config_test

import (
	"maps"
	"net/netip"
	"strings"
	"testing"

	"github.com/Nurasick/NUPP/api/internal/config"
)

// env builds a fake getenv from a map. Passing a function instead of reading
// os.Getenv directly keeps tests independent of the real environment.
func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// withDB returns vars plus the one required variable.
func withDB(vars map[string]string) map[string]string {
	out := map[string]string{"DATABASE_URL": "postgres://localhost/nupp"}
	maps.Copy(out, vars)
	return out
}

func TestLoad_RequiresDatabaseURL(t *testing.T) {
	if _, err := config.Load(env(nil)); err == nil {
		t.Fatal("expected an error when DATABASE_URL is missing")
	}
}

// AC-32 and spec 1.5 §16: every default.
func TestLoad_AppliesDefaults(t *testing.T) {
	cfg, err := config.Load(env(withDB(nil)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.StorageDir != "./data/files" || cfg.Env != "development" {
		t.Errorf("basic defaults: %+v", cfg)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want none", cfg.TrustedProxies)
	}
	wantRL := config.RateLimit{Enabled: true, APIRate: 20, APIBurst: 100, FilesRate: 30, FilesBurst: 300}
	if cfg.RateLimit != wantRL {
		t.Errorf("RateLimit = %+v, want %+v", cfg.RateLimit, wantRL)
	}
	if cfg.MaxInflightAPI != 32 || cfg.MaxInflightFiles != 64 || cfg.MaxInflightFilesPerClient != 8 {
		t.Errorf("caps = %d/%d/%d, want 32/64/8", cfg.MaxInflightAPI, cfg.MaxInflightFiles, cfg.MaxInflightFilesPerClient)
	}
	if cfg.DBMaxConns != 10 {
		t.Errorf("DBMaxConns = %d, want 10", cfg.DBMaxConns)
	}
}

func TestLoad_ReadsOverrides(t *testing.T) {
	cfg, err := config.Load(env(withDB(map[string]string{
		"HTTP_ADDR":                     ":9000",
		"STORAGE_DIR":                   "/data/files",
		"APP_ENV":                       "production",
		"TRUSTED_PROXIES":               "172.30.0.0/24, 10.0.0.5, fd00::/8",
		"RATE_LIMIT_ENABLED":            "false",
		"RATE_LIMIT_API_RPS":            "2.5",
		"RATE_LIMIT_API_BURST":          "5",
		"RATE_LIMIT_FILES_RPS":          "7",
		"RATE_LIMIT_FILES_BURST":        "70",
		"MAX_INFLIGHT_API":              "4",
		"MAX_INFLIGHT_FILES":            "6",
		"MAX_INFLIGHT_FILES_PER_CLIENT": "2",
		"DB_MAX_CONNS":                  "3",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":9000" || cfg.StorageDir != "/data/files" || cfg.Env != "production" {
		t.Errorf("basic overrides: %+v", cfg)
	}
	wantProxies := []netip.Prefix{
		netip.MustParsePrefix("172.30.0.0/24"),
		netip.MustParsePrefix("10.0.0.5/32"), // a bare IP means exactly that address
		netip.MustParsePrefix("fd00::/8"),
	}
	if len(cfg.TrustedProxies) != len(wantProxies) {
		t.Fatalf("TrustedProxies = %v, want %v", cfg.TrustedProxies, wantProxies)
	}
	for i := range wantProxies {
		if cfg.TrustedProxies[i] != wantProxies[i] {
			t.Errorf("TrustedProxies[%d] = %v, want %v", i, cfg.TrustedProxies[i], wantProxies[i])
		}
	}
	wantRL := config.RateLimit{Enabled: false, APIRate: 2.5, APIBurst: 5, FilesRate: 7, FilesBurst: 70}
	if cfg.RateLimit != wantRL {
		t.Errorf("RateLimit = %+v, want %+v", cfg.RateLimit, wantRL)
	}
	if cfg.MaxInflightAPI != 4 || cfg.MaxInflightFiles != 6 || cfg.MaxInflightFilesPerClient != 2 || cfg.DBMaxConns != 3 {
		t.Errorf("caps/conns not applied: %+v", cfg)
	}
}

// HAC-17: invalid values stop startup with a message naming the variable.
func TestLoad_RejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"RATE_LIMIT_API_RPS":            "fast",
		"RATE_LIMIT_FILES_RPS":          "0",
		"RATE_LIMIT_API_BURST":          "-1",
		"RATE_LIMIT_FILES_BURST":        "1.5",
		"RATE_LIMIT_ENABLED":            "maybe",
		"MAX_INFLIGHT_API":              "0",
		"MAX_INFLIGHT_FILES":            "lots",
		"MAX_INFLIGHT_FILES_PER_CLIENT": "-3",
		"DB_MAX_CONNS":                  "99999999999",
		"TRUSTED_PROXIES":               "10.0.0.0/33",
	}
	for name, value := range cases {
		t.Run(name+"="+value, func(t *testing.T) {
			_, err := config.Load(env(withDB(map[string]string{name: value})))
			if err == nil {
				t.Fatalf("expected an error for %s=%q", name, value)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q should name %s", err, name)
			}
		})
	}
}

func TestLoad_RejectsGarbageInProxyList(t *testing.T) {
	if _, err := config.Load(env(withDB(map[string]string{"TRUSTED_PROXIES": "10.0.0.0/8,not-an-ip"}))); err == nil {
		t.Fatal("expected an error for a malformed TRUSTED_PROXIES entry")
	}
}

// H-IP-6: behind a proxy without TRUSTED_PROXIES, every user would share
// one rate-limit bucket, so production warns about it.
func TestWarnings_ProductionWithoutTrustedProxies(t *testing.T) {
	prod, _ := config.Load(env(withDB(map[string]string{"APP_ENV": "production"})))
	if len(prod.Warnings()) == 0 {
		t.Error("production without TRUSTED_PROXIES should warn")
	}
	ok, _ := config.Load(env(withDB(map[string]string{"APP_ENV": "production", "TRUSTED_PROXIES": "172.30.0.0/24"})))
	dev, _ := config.Load(env(withDB(nil)))
	if len(ok.Warnings()) != 0 || len(dev.Warnings()) != 0 {
		t.Errorf("unexpected warnings: %v / %v", ok.Warnings(), dev.Warnings())
	}
}
