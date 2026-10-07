package server

// Middleware that protects the server from overload: client identification,
// per-client rate limiting and concurrency caps (hardening spec §4–§6).

import (
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/Nurasick/NUPP/api/internal/clientip"
	"github.com/Nurasick/NUPP/api/internal/httpx"
	"github.com/Nurasick/NUPP/api/internal/ratelimit"
)

// Caps limits how many requests may be in progress at once (H-CC).
// A zero value means "no limit" for that field.
type Caps struct {
	API            int // concurrent JSON requests, all clients together
	Files          int // concurrent file downloads, all clients together
	FilesPerClient int // concurrent file downloads per client
}

// stack holds everything the middleware chain needs.
type stack struct {
	logger   *slog.Logger
	resolver *clientip.Resolver // nil: use RemoteAddr only
	limiter  *ratelimit.Limiter // nil: no rate limiting
	caps     Caps
}

// requestClass decides which budget a request uses (H-RL-1).
type requestClass int

const (
	classAPI requestClass = iota
	classFiles
	classExempt // /healthz: never limited, so monitors always get an answer
)

func classify(r *http.Request) requestClass {
	switch {
	case r.URL.Path == "/healthz":
		return classExempt
	case strings.HasPrefix(r.URL.Path, "/api/v1/files/"):
		return classFiles
	default:
		// Everything else, including unknown paths and odd methods, costs an
		// api token, otherwise a flood of 404s would be free.
		return classAPI
	}
}

// clientAddr returns the address stored by withClientIP.
func clientAddr(r *http.Request) netip.Addr {
	addr, _ := clientip.FromContext(r.Context())
	return addr
}

// withClientIP works out the client's address once per request and stores
// it in the request context for logging and limiting (H-IP-4).
func withClientIP(resolver *clientip.Resolver, next http.Handler) http.Handler {
	if resolver == nil {
		resolver = clientip.NewResolver(nil)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := clientip.WithAddr(r.Context(), resolver.ClientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// rateLimit rejects requests from clients that exceeded their budget (H-RL).
func rateLimit(limiter *ratelimit.Limiter, next http.Handler) http.Handler {
	if limiter == nil {
		return next // limiting disabled
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		class := classify(r)
		if class == classExempt {
			next.ServeHTTP(w, r)
			return
		}
		bucket := ratelimit.API
		if class == classFiles {
			bucket = ratelimit.Files
		}
		if ok, wait := limiter.Allow(bucket, clientAddr(r)); !ok {
			// Retry-After is in whole seconds; round up so a client that
			// obeys it never comes back too early.
			seconds := max(1, int(math.Ceil(wait.Seconds())))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			httpx.Fail(w, http.StatusTooManyRequests, httpx.CodeRateLimited, "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// semaphore is a counting semaphore: a buffered channel whose capacity is
// the number of slots. Sending takes a slot, receiving gives it back.
type semaphore chan struct{}

// tryAcquire takes a slot if one is free, without waiting (H-CC-3: we
// reject instead of queueing). A nil semaphore means "unlimited".
func (s semaphore) tryAcquire() bool {
	if s == nil {
		return true
	}
	select {
	case s <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s semaphore) release() {
	if s != nil {
		<-s
	}
}

func newSemaphore(n int) semaphore {
	if n <= 0 {
		return nil
	}
	return make(semaphore, n)
}

// perClientCounter counts concurrent downloads per client key (H-CC-2).
type perClientCounter struct {
	limit  int
	mu     sync.Mutex
	counts map[netip.Prefix]int
}

func (c *perClientCounter) tryAcquire(key netip.Prefix) bool {
	if c.limit <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts[key] >= c.limit {
		return false
	}
	c.counts[key]++
	return true
}

func (c *perClientCounter) release(key netip.Prefix) {
	if c.limit <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Delete at zero so the map only holds clients with downloads running.
	if c.counts[key]--; c.counts[key] <= 0 {
		delete(c.counts, key)
	}
}

// inflight enforces the concurrency caps (H-CC).
func inflight(caps Caps, next http.Handler) http.Handler {
	apiSlots := newSemaphore(caps.API)
	fileSlots := newSemaphore(caps.Files)
	perClient := &perClientCounter{limit: caps.FilesPerClient, counts: make(map[netip.Prefix]int)}

	overloaded := func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "1")
		httpx.Fail(w, http.StatusServiceUnavailable, httpx.CodeOverloaded, "server is busy, try again shortly")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch classify(r) {
		case classExempt:
			next.ServeHTTP(w, r)

		case classAPI:
			if !apiSlots.tryAcquire() {
				overloaded(w)
				return
			}
			defer apiSlots.release() // defer: released even if the handler panics
			next.ServeHTTP(w, r)

		case classFiles:
			// Per-client first: a greedy client gets 429 without touching
			// the shared slots that other clients need.
			key := ratelimit.Key(clientAddr(r))
			if !perClient.tryAcquire(key) {
				w.Header().Set("Retry-After", "1")
				httpx.Fail(w, http.StatusTooManyRequests, httpx.CodeRateLimited, "too many downloads at once")
				return
			}
			defer perClient.release(key)
			if !fileSlots.tryAcquire() {
				overloaded(w)
				return
			}
			defer fileSlots.release()
			next.ServeHTTP(w, r)
		}
	})
}
