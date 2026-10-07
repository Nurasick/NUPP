package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nurasick/NUPP/api/internal/clientip"
	"github.com/Nurasick/NUPP/api/internal/ratelimit"
)

// okHandler answers 200 for any path, so these tests exercise only middleware.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func limiter(api, files ratelimit.Limit) *ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{API: api, Files: files, MaxKeys: 1000, IdleAfter: time.Minute})
}

// call sends a request from remote (an "ip:port") through h.
func call(h http.Handler, method, path, remote string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Add(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error *struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == nil {
		t.Fatalf("not an error envelope: %s", rec.Body)
	}
	return body.Error.Code
}

const (
	alice = "198.51.100.1:5000"
	bob   = "198.51.100.2:5000"
)

// HAC-3: burst+1 → 429 with Retry-After and the usual headers; other clients unaffected.
func TestRateLimit_RejectsAfterBurst(t *testing.T) {
	h := withMiddleware(stack{
		logger:  slog.New(slog.DiscardHandler),
		limiter: limiter(ratelimit.Limit{Rate: 0.1, Burst: 2}, ratelimit.Limit{Rate: 1, Burst: 1}),
	}, okHandler)

	for i := range 2 {
		if rec := call(h, http.MethodGet, "/api/v1/courses", alice); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, rec.Code)
		}
	}
	rec := call(h, http.MethodGet, "/api/v1/courses", alice)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "rate_limited" {
		t.Fatalf("status = %d body = %s, want 429 rate_limited", rec.Code, rec.Body)
	}
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 1 {
		t.Errorf("Retry-After = %q, want whole seconds ≥ 1", rec.Header().Get("Retry-After"))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("429 lacks security/no-store headers: %v", rec.Header())
	}
	if rec := call(h, http.MethodGet, "/api/v1/courses", bob); rec.Code != http.StatusOK {
		t.Errorf("bob limited by alice's traffic: %d", rec.Code)
	}
}

// HAC-5: classes, exempt health check, unknown paths count as api.
func TestRateLimit_Classes(t *testing.T) {
	h := withMiddleware(stack{
		logger:  slog.New(slog.DiscardHandler),
		limiter: limiter(ratelimit.Limit{Rate: 0.1, Burst: 1}, ratelimit.Limit{Rate: 0.1, Burst: 1}),
	}, okHandler)

	call(h, http.MethodGet, "/api/v1/nope", alice) // unknown path uses the api token
	if rec := call(h, http.MethodGet, "/api/v1/courses", alice); rec.Code != http.StatusTooManyRequests {
		t.Errorf("unknown /api path didn't count against the api bucket: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/anything", alice); rec.Code != http.StatusTooManyRequests {
		t.Errorf("non-api path / other method must also be api class: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/api/v1/files/x", alice); rec.Code != http.StatusOK {
		t.Errorf("files bucket drained by api requests: %d", rec.Code)
	}
	for range 5 {
		if rec := call(h, http.MethodGet, "/healthz", alice); rec.Code != http.StatusOK {
			t.Fatalf("/healthz was rate limited: %d", rec.Code)
		}
	}
}

// HAC-2 + HAC-3: behind a trusted proxy, each forwarded client has its own bucket.
func TestRateLimit_UsesForwardedClientBehindTrustedProxy(t *testing.T) {
	h := withMiddleware(stack{
		logger:   slog.New(slog.DiscardHandler),
		resolver: clientip.NewResolver([]netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")}),
		limiter:  limiter(ratelimit.Limit{Rate: 0.1, Burst: 1}, ratelimit.Limit{Rate: 1, Burst: 1}),
	}, okHandler)
	proxy := "172.30.0.5:40000"

	call(h, http.MethodGet, "/api/v1/courses", proxy, "X-Forwarded-For", "203.0.113.1")
	if rec := call(h, http.MethodGet, "/api/v1/courses", proxy, "X-Forwarded-For", "203.0.113.2"); rec.Code != http.StatusOK {
		t.Errorf("second client behind the proxy was limited: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/api/v1/courses", proxy, "X-Forwarded-For", "203.0.113.1"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("first client wasn't limited on its second request: %d", rec.Code)
	}
}

// HAC-7: no limiter configured → no limiting.
func TestRateLimit_Disabled(t *testing.T) {
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler)}, okHandler)
	for range 50 {
		if rec := call(h, http.MethodGet, "/api/v1/courses", alice); rec.Code != http.StatusOK {
			t.Fatalf("status %d with limiting disabled", rec.Code)
		}
	}
}

// blockingHandler holds every request whose X-Block header is set until
// release is closed, so tests can fill the concurrency slots deterministically.
type blockingHandler struct {
	started chan struct{}
	release chan struct{}
}

func newBlocking() *blockingHandler {
	return &blockingHandler{started: make(chan struct{}, 100), release: make(chan struct{})}
}

func (b *blockingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Block") != "" {
		b.started <- struct{}{}
		<-b.release
	}
	w.WriteHeader(http.StatusOK)
}

// startBlocked sends a blocking request in the background and waits until
// it holds its slot.
func startBlocked(t *testing.T, h http.Handler, b *blockingHandler, wg *sync.WaitGroup, path, remote string) {
	t.Helper()
	wg.Add(1)
	go func() {
		defer wg.Done()
		call(h, http.MethodGet, path, remote, "X-Block", "1")
	}()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked request never started")
	}
}

// HAC-8: global cap reached → immediate 503 overloaded.
func TestInflight_GlobalCapRejectsImmediately(t *testing.T) {
	b := newBlocking()
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler), caps: Caps{API: 1, Files: 1, FilesPerClient: 1}}, b)
	var wg sync.WaitGroup
	defer func() { close(b.release); wg.Wait() }()

	startBlocked(t, h, b, &wg, "/api/v1/courses", alice)
	rec := call(h, http.MethodGet, "/api/v1/courses", bob)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "overloaded" || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("status = %d Retry-After = %q body = %s, want 503 overloaded", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if rec := call(h, http.MethodGet, "/healthz", bob); rec.Code != http.StatusOK {
		t.Errorf("/healthz must not count against the api cap: %d", rec.Code)
	}
}

// HAC-8 / H-CC-2: one client can't hold every download slot.
func TestInflight_PerClientFileCap(t *testing.T) {
	b := newBlocking()
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler), caps: Caps{API: 10, Files: 10, FilesPerClient: 1}}, b)
	var wg sync.WaitGroup
	defer func() { close(b.release); wg.Wait() }()

	startBlocked(t, h, b, &wg, "/api/v1/files/a", alice)
	rec := call(h, http.MethodGet, "/api/v1/files/b", alice)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "rate_limited" {
		t.Fatalf("second concurrent download by alice: status %d, want 429", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/api/v1/files/b", bob); rec.Code != http.StatusOK {
		t.Errorf("bob's download blocked by alice's: %d", rec.Code)
	}
}

// Slots are released when requests finish (otherwise the server would
// slowly lock itself out).
func TestInflight_SlotsAreReleased(t *testing.T) {
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler), caps: Caps{API: 1, Files: 1, FilesPerClient: 1}}, okHandler)
	for range 20 {
		if rec := call(h, http.MethodGet, "/api/v1/files/a", alice); rec.Code != http.StatusOK {
			t.Fatalf("status %d: a slot leaked", rec.Code)
		}
		if rec := call(h, http.MethodGet, "/api/v1/courses", alice); rec.Code != http.StatusOK {
			t.Fatalf("status %d: a slot leaked", rec.Code)
		}
	}
}

// HAC-13: default CSP and CORP on everything; a handler's stricter CSP wins.
func TestHeaders_CSPAndCORP(t *testing.T) {
	sandboxed := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/files/", sandboxed)
	mux.Handle("/", okHandler)
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler)}, mux)

	json := call(h, http.MethodGet, "/api/v1/courses", alice)
	if got := json.Header().Get("Content-Security-Policy"); got != "default-src 'none'; frame-ancestors 'none'" {
		t.Errorf("JSON CSP = %q", got)
	}
	file := call(h, http.MethodGet, "/api/v1/files/x", alice)
	if got := file.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Errorf("file CSP = %q", got)
	}
	for _, rec := range []*httptest.ResponseRecorder{json, file} {
		if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
			t.Errorf("CORP = %q", got)
		}
	}
}

// HAC-15: the log line carries the derived client IP.
func TestLogRequests_IncludesClientIP(t *testing.T) {
	var buf bytes.Buffer
	h := withMiddleware(stack{
		logger:   slog.New(slog.NewJSONHandler(&buf, nil)),
		resolver: clientip.NewResolver([]netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")}),
	}, okHandler)
	call(h, http.MethodGet, "/api/v1/courses", "172.30.0.5:1", "X-Forwarded-For", "203.0.113.9")
	if !strings.Contains(buf.String(), `"client_ip":"203.0.113.9"`) {
		t.Fatalf("log line lacks client_ip: %s", buf.String())
	}
}
