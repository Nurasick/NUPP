package ratelimit_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Nurasick/NUPP/api/internal/ratelimit"
)

// clock is a fake time source the tests move forward by hand, so no test
// ever sleeps or depends on how fast the machine is.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newLimiter(c *clock, maxKeys int) *ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{
		API:       ratelimit.Limit{Rate: 2, Burst: 3}, // 3 at once, then 2 per second
		Files:     ratelimit.Limit{Rate: 1, Burst: 1},
		MaxKeys:   maxKeys,
		IdleAfter: time.Minute,
		Now:       c.now,
	})
}

var (
	alice = netip.MustParseAddr("198.51.100.1")
	bob   = netip.MustParseAddr("198.51.100.2")
)

// HAC-3: the burst is allowed, the next request is rejected with a wait time.
func TestAllow_BurstThenReject(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 100)

	for i := range 3 {
		if ok, _ := l.Allow(ratelimit.API, alice); !ok {
			t.Fatalf("request %d within burst was rejected", i+1)
		}
	}
	ok, retry := l.Allow(ratelimit.API, alice)
	if ok {
		t.Fatal("request beyond burst was allowed")
	}
	// At 2 tokens/s the next token is 500 ms away.
	if retry != 500*time.Millisecond {
		t.Errorf("retryAfter = %v, want 500ms", retry)
	}
}

// HAC-3: rejected requests don't consume tokens, so a client hammering the
// API doesn't push its own recovery further into the future.
func TestAllow_RejectionConsumesNothing(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 100)
	for range 3 {
		l.Allow(ratelimit.API, alice)
	}
	for range 50 {
		l.Allow(ratelimit.API, alice) // all rejected
	}
	c.advance(500 * time.Millisecond)
	if ok, _ := l.Allow(ratelimit.API, alice); !ok {
		t.Fatal("after waiting retryAfter the request must be allowed")
	}
}

// HAC-3: one client's exhausted bucket doesn't affect another client.
func TestAllow_ClientsAreIndependent(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 100)
	for range 10 {
		l.Allow(ratelimit.API, alice)
	}
	if ok, _ := l.Allow(ratelimit.API, bob); !ok {
		t.Fatal("bob was limited because of alice")
	}
}

// HAC-5: api and files buckets are independent.
func TestAllow_ClassesAreIndependent(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 100)
	for range 10 {
		l.Allow(ratelimit.API, alice)
	}
	if ok, _ := l.Allow(ratelimit.Files, alice); !ok {
		t.Fatal("files bucket was drained by api requests")
	}
	if ok, _ := l.Allow(ratelimit.Files, alice); ok {
		t.Fatal("files burst of 1 should reject the second request")
	}
}

// HAC-4
func TestKey_GroupsIPv6By64(t *testing.T) {
	same1 := ratelimit.Key(netip.MustParseAddr("2001:db8:1:2:aaaa::1"))
	same2 := ratelimit.Key(netip.MustParseAddr("2001:db8:1:2:ffff::9"))
	other := ratelimit.Key(netip.MustParseAddr("2001:db8:1:3::1"))
	if same1 != same2 {
		t.Errorf("same /64 got different keys: %v vs %v", same1, same2)
	}
	if same1 == other {
		t.Errorf("different /64s share a key: %v", same1)
	}
	if got := ratelimit.Key(alice); got != netip.MustParsePrefix("198.51.100.1/32") {
		t.Errorf("IPv4 key = %v, want /32", got)
	}
}

// HAC-4: the limiter itself applies the /64 grouping.
func TestAllow_SameIPv6NetworkSharesBucket(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 100)
	for i := range 3 {
		l.Allow(ratelimit.API, netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 15: byte(i)}))
	}
	if ok, _ := l.Allow(ratelimit.API, netip.MustParseAddr("2001:db8::ffff")); ok {
		t.Fatal("rotating addresses inside one /64 evaded the limit")
	}
}

// HAC-6: idle keys are forgotten after IdleAfter.
func TestSweep_EvictsIdleKeys(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 100)
	l.Allow(ratelimit.API, alice)
	c.advance(30 * time.Second)
	l.Allow(ratelimit.API, bob)

	c.advance(31 * time.Second) // alice idle 61 s, bob 31 s
	l.Sweep()
	if l.Len() != 1 {
		t.Fatalf("Len = %d after sweep, want 1 (only bob)", l.Len())
	}
}

// HAC-6 / H-RL-4: a full table never grows, and new clients are let through
// rather than denied (the concurrency caps still protect the server).
func TestAllow_FullTableLetsNewKeysThroughWithoutGrowing(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 2)
	l.Allow(ratelimit.API, alice)
	l.Allow(ratelimit.API, bob)

	stranger := netip.MustParseAddr("203.0.113.50")
	for i := range 10 {
		if ok, _ := l.Allow(ratelimit.API, stranger); !ok {
			t.Fatalf("new key rejected on request %d while table full", i+1)
		}
	}
	if l.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (cap)", l.Len())
	}

	// Once old keys are idle, the full-table check sweeps them and the new
	// key gets a real bucket again.
	c.advance(2 * time.Minute)
	for range 3 {
		l.Allow(ratelimit.API, stranger)
	}
	if ok, _ := l.Allow(ratelimit.API, stranger); ok {
		t.Fatal("after the sweep the stranger should be limited normally")
	}
}

// Review fix: with the table full, a stream of new keys must not trigger a
// full sweep on every request (each sweep walks the whole map under the one
// lock every request needs). Inline sweeps run at most once per second.
func TestAllow_FullTableSweepsAreThrottled(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 2)
	l.Allow(ratelimit.API, alice)
	l.Allow(ratelimit.API, bob)
	before := l.Sweeps()

	for i := range 1000 {
		l.Allow(ratelimit.API, netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}))
	}
	if got := l.Sweeps() - before; got > 1 {
		t.Fatalf("1000 new keys in the same second caused %d sweeps, want at most 1", got)
	}

	c.advance(time.Second)
	l.Allow(ratelimit.API, netip.MustParseAddr("10.9.9.9"))
	if got := l.Sweeps() - before; got != 2 {
		t.Fatalf("after a second, sweeps = %d, want 2", got)
	}
}
