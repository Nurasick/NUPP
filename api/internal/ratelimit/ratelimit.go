// Package ratelimit limits how many requests each client may make
// (hardening spec §5).
//
// It uses the "token bucket" algorithm from golang.org/x/time/rate: every
// client has a bucket holding up to Burst tokens that refills at Rate tokens
// per second. Each request takes one token; when the bucket is empty the
// request is rejected and the client is told how long to wait. Bursts (a page
// loading several resources at once) are fine; sustained floods are not.
package ratelimit

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Class separates kinds of traffic that get separate budgets.
type Class int

const (
	API   Class = iota // JSON endpoints
	Files              // file downloads
	numClasses
)

// inlineSweepInterval is the minimum gap between sweeps triggered by a full
// table (see Allow).
const inlineSweepInterval = time.Second

// Limit is one token bucket's settings.
type Limit struct {
	Rate  float64 // tokens added per second
	Burst int     // maximum tokens in the bucket
}

// Config configures a Limiter.
type Config struct {
	API, Files Limit
	MaxKeys    int              // most clients tracked at once (H-RL-4)
	IdleAfter  time.Duration    // forget a client after this long without requests
	Now        func() time.Time // time source; nil means time.Now (tests inject a fake)
	Logger     *slog.Logger     // nil means slog.Default()
}

// entry holds one client's buckets.
type entry struct {
	buckets  [numClasses]*rate.Limiter
	lastSeen time.Time
}

// Limiter tracks token buckets per client. It is safe for concurrent use.
type Limiter struct {
	cfg    Config
	limits [numClasses]Limit

	// mu guards everything below. One mutex is plenty here: a normal Allow
	// holds it for well under a microsecond. Only a sweep holds it longer
	// (a few ms for 100 000 keys), which is why inline sweeps are throttled.
	mu           sync.Mutex
	entries      map[netip.Prefix]*entry
	lastFullWarn time.Time
	lastSweep    time.Time
	sweeps       int // number of sweeps run (observed by tests)
}

// New creates a Limiter.
func New(cfg Config) *Limiter {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Limiter{
		cfg:     cfg,
		limits:  [numClasses]Limit{API: cfg.API, Files: cfg.Files},
		entries: make(map[netip.Prefix]*entry),
	}
}

// Key returns the bucket key for addr: the address itself for IPv4, its /64
// network for IPv6 (H-RL-3). An ISP typically hands each customer a whole
// /64, so keying on single IPv6 addresses would let one client rotate through
// billions of addresses to dodge the limit.
func Key(addr netip.Addr) netip.Prefix {
	if !addr.IsValid() {
		return netip.Prefix{} // all unparseable peers share one bucket
	}
	bits := 32
	if addr.Is6() {
		bits = 64
	}
	prefix, _ := addr.Prefix(bits) // can't fail for 32 on v4 / 64 on v6
	return prefix
}

// Allow reports whether a request of the given class from addr may proceed.
// If not, retryAfter says how long until the next token is available.
func (l *Limiter) Allow(class Class, addr netip.Addr) (ok bool, retryAfter time.Duration) {
	now := l.cfg.Now()
	key := Key(addr)

	l.mu.Lock()
	defer l.mu.Unlock()

	e := l.entries[key]
	if e == nil {
		// Table full: try to make room, but at most once per second. A
		// sweep walks the whole map while holding the lock every request
		// needs, so sweeping for each new client would let someone who
		// floods us with new addresses stall all requests (review fix).
		// The background Run sweep keeps cleaning up regardless.
		if len(l.entries) >= l.cfg.MaxKeys && now.Sub(l.lastSweep) >= inlineSweepInterval {
			l.sweepLocked(now)
		}
		if len(l.entries) >= l.cfg.MaxKeys {
			// Still full: someone is generating huge numbers of client keys.
			// We let new keys through unlimited rather than rejecting every
			// new visitor; the global concurrency caps still bound the total
			// load (decision in spec §19).
			if now.Sub(l.lastFullWarn) >= time.Minute {
				l.lastFullWarn = now
				l.cfg.Logger.Warn("rate limiter table full; new clients pass unlimited", "max_keys", l.cfg.MaxKeys)
			}
			return true, 0
		}
		e = &entry{}
		for c := range numClasses {
			e.buckets[c] = rate.NewLimiter(rate.Limit(l.limits[c].Rate), l.limits[c].Burst)
		}
		l.entries[key] = e
	}
	e.lastSeen = now

	// Reserve takes a token now if one is available, or books one in the
	// future. If it's in the future we cancel the booking (so a rejected
	// request costs nothing, H-RL-2) and report the wait.
	res := e.buckets[class].ReserveN(now, 1)
	if delay := res.DelayFrom(now); delay > 0 {
		res.CancelAt(now)
		return false, delay
	}
	return true, 0
}

// Sweep forgets clients idle for at least IdleAfter. A bucket that has been
// idle that long has refilled completely, so forgetting it changes nothing
// for the client; it only frees memory.
func (l *Limiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(l.cfg.Now())
}

func (l *Limiter) sweepLocked(now time.Time) {
	l.sweeps++
	l.lastSweep = now
	for key, e := range l.entries {
		if now.Sub(e.lastSeen) >= l.cfg.IdleAfter {
			delete(l.entries, key) // deleting while ranging is allowed in Go
		}
	}
}

// Len returns how many clients are currently tracked.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Run sweeps periodically until ctx is cancelled (H-RL-6). Start it in its
// own goroutine: go limiter.Run(ctx).
func (l *Limiter) Run(ctx context.Context) {
	ticker := time.NewTicker(l.cfg.IdleAfter / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Sweep()
		}
	}
}
