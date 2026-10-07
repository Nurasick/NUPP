package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"
)

// deadlineRecorder is a ResponseWriter that supports write deadlines (like
// the real one) and records every deadline set on it.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadlines = append(d.deadlines, t)
	return nil
}

// HAC-12: api responses get a ~15 s write deadline and a 10 s context
// deadline (H-DB-3). The deadline is not cleared by our code: net/http clears
// it after flushing the response (see TestDeadlines_DoNotLeakAcrossKeepAliveRequests).
func TestDeadlines_APIRequests(t *testing.T) {
	var ctxDeadline time.Time
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler)}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxDeadline, _ = r.Context().Deadline()
	}))
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	start := time.Now()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/courses", nil))

	if len(rec.deadlines) != 1 {
		t.Fatalf("deadlines set = %v, want exactly one", rec.deadlines)
	}
	if d := rec.deadlines[0].Sub(start); d < 14*time.Second || d > 16*time.Second {
		t.Errorf("write deadline = now+%v, want ~15s", d)
	}
	if d := ctxDeadline.Sub(start); d < 9*time.Second || d > 11*time.Second {
		t.Errorf("context deadline = now+%v, want ~10s", d)
	}
}

// File downloads set their own (size-based) deadline in the handler, so the
// middleware must leave them alone; the health check isn't touched either.
func TestDeadlines_NotAppliedToFilesOrHealth(t *testing.T) {
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler)}, okHandler)
	for _, path := range []string{"/api/v1/files/x", "/healthz"} {
		rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if len(rec.deadlines) != 0 {
			t.Errorf("%s: middleware set deadlines %v", path, rec.deadlines)
		}
	}
}

// H-HTTP-5: writers without deadline support (like httptest's recorder) work.
func TestDeadlines_TolerateUnsupportedWriters(t *testing.T) {
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler)}, okHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/courses", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// HAC-12 / H-HTTP-4: on a real keep-alive connection, a second request that
// arrives after the first response's write deadline must still succeed.
// This guards against a stale deadline staying on the connection (which is
// what happened in older Go versions without a server-wide WriteTimeout).
func TestDeadlines_DoNotLeakAcrossKeepAliveRequests(t *testing.T) {
	h := withMiddleware(stack{logger: slog.New(slog.DiscardHandler), apiWriteTimeout: 100 * time.Millisecond},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := srv.Client()

	get := func() (reused bool, err error) {
		trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }}
		req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, srv.URL+"/api/v1/courses", nil)
		resp, err := client.Do(req)
		if err != nil {
			return reused, err
		}
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		return reused, err
	}

	if _, err := get(); err != nil {
		t.Fatalf("first request: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // well past the 100 ms deadline
	reused, err := get()
	if err != nil {
		t.Fatalf("second request on the kept-alive connection failed: %v", err)
	}
	if !reused {
		t.Fatal("test precondition: the second request should reuse the connection")
	}
}

// HAC-11
func TestNewHTTPServer_Limits(t *testing.T) {
	srv := NewHTTPServer(":0", okHandler)
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 15*time.Second ||
		srv.IdleTimeout != 60*time.Second || srv.MaxHeaderBytes != 32<<10 || srv.WriteTimeout != 0 {
		t.Fatalf("server limits = %+v", srv)
	}
}

// HAC-16: shutdown that runs out of time force-closes connections and errors.
func TestShutdown_ForceClosesAfterTimeout(t *testing.T) {
	stuck := make(chan struct{})
	defer close(stuck)
	srv := NewHTTPServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-stuck }))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()

	clientErr := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
		clientErr <- err
	}()
	time.Sleep(200 * time.Millisecond) // let the request reach the handler

	if err := Shutdown(srv, 100*time.Millisecond); err == nil {
		t.Fatal("Shutdown returned nil although a request was still running")
	}
	select {
	case err := <-clientErr:
		if err == nil {
			t.Error("client got a response; the connection should have been cut")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connection was not force-closed")
	}
}
