package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// HAC-14 / H-HC-1: however many monitors poll /healthz, the database is
// pinged at most once per TTL.
func TestHealthChecker_CachesResult(t *testing.T) {
	var pings atomic.Int32
	now := time.Unix(1_000_000, 0)
	h := &healthChecker{
		ping: func(context.Context) error { pings.Add(1); return nil },
		ttl:  2 * time.Second,
		now:  func() time.Time { return now },
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.check(context.Background()); err != nil {
				t.Errorf("check: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := pings.Load(); got != 1 {
		t.Fatalf("50 concurrent checks caused %d pings, want 1", got)
	}

	now = now.Add(2 * time.Second)
	_ = h.check(context.Background())
	if got := pings.Load(); got != 2 {
		t.Fatalf("after the TTL: %d pings, want 2", got)
	}
}

// A failure is cached too (so a down database isn't hammered), and a client
// that disconnects mid-check must not poison the cache with its cancellation.
func TestHealthChecker_FailureAndCancellation(t *testing.T) {
	down := errors.New("connection refused")
	now := time.Unix(1_000_000, 0)
	var sawCancelled bool
	h := &healthChecker{
		ping: func(ctx context.Context) error {
			if ctx.Err() != nil {
				sawCancelled = true
			}
			return down
		},
		ttl: 2 * time.Second,
		now: func() time.Time { return now },
	}

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.check(gone); !errors.Is(err, down) {
		t.Fatalf("check = %v, want the ping error", err)
	}
	if sawCancelled {
		t.Error("the ping saw the caller's cancellation; it must run detached from it")
	}
}

// H-HC-2: the probe used by Docker's healthcheck.
func TestProbeHealth(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probe requested %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()

	if err := ProbeHealth(srv.URL); err != nil {
		t.Errorf("healthy server: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := ProbeHealth(srv.URL); err == nil {
		t.Error("503 must count as unhealthy")
	}
	srv.Close()
	if err := ProbeHealth(srv.URL); err == nil {
		t.Error("an unreachable server must count as unhealthy")
	}
}

func TestLocalURL(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://127.0.0.1:8080",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		"127.0.0.1:8080": "http://127.0.0.1:8080",
		"[::]:8080":      "http://127.0.0.1:8080",
	}
	for addr, want := range cases {
		if got := LocalURL(addr); got != want {
			t.Errorf("LocalURL(%q) = %q, want %q", addr, got, want)
		}
	}
}
