package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Nurasick/NUPP/api/internal/httpx"
)

const (
	// healthTimeout bounds one database ping so a hung database makes the
	// check fail quickly instead of hanging the caller (R-EP-1).
	healthTimeout = 2 * time.Second
	// healthCacheTTL is how long a ping result is reused (H-HC-1).
	healthCacheTTL = 2 * time.Second
)

// healthChecker pings the database at most once per ttl, however often
// /healthz is called. /healthz is exempt from rate limiting (monitors must
// always get an answer), so without this cache anyone could use it to send
// unlimited queries to the database.
type healthChecker struct {
	ping func(context.Context) error
	ttl  time.Duration
	now  func() time.Time

	// mu is held during the ping itself: concurrent callers wait for the one
	// ping in progress and then reuse its result, instead of each pinging.
	mu        sync.Mutex
	checkedAt time.Time
	lastErr   error
}

func (h *healthChecker) check(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.checkedAt.IsZero() && h.now().Sub(h.checkedAt) < h.ttl {
		return h.lastErr
	}
	// WithoutCancel: the ping must not inherit the caller's cancellation.
	// Otherwise one monitor disconnecting mid-check would cache a bogus
	// failure that every other caller then sees for the next ttl.
	pingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), healthTimeout)
	defer cancel()
	h.lastErr = h.ping(pingCtx)
	h.checkedAt = h.now()
	return h.lastErr
}

// handler serves GET /healthz.
func (h *healthChecker) handler(w http.ResponseWriter, r *http.Request) {
	if err := h.check(r.Context()); err != nil {
		slog.ErrorContext(r.Context(), "health check failed", "err", err)
		httpx.Fail(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, "database unreachable")
		return
	}
	httpx.OK(w, map[string]string{"status": "ok"})
}

// LocalURL turns a listen address such as ":8080" or "0.0.0.0:8080" into the
// URL a process on the same machine uses to reach it: http://127.0.0.1:8080.
func LocalURL(listenAddr string) string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		port = "8080"
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port)
}

// ProbeHealth requests baseURL + "/healthz" and returns nil only for 200 OK.
// The server binary runs it for `server -healthcheck` (H-HC-2), which Docker
// uses as the container healthcheck: the distroless image has no curl.
func ProbeHealth(baseURL string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return fmt.Errorf("health probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health probe: status %d", resp.StatusCode)
	}
	return nil
}
