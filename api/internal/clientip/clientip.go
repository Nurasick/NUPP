// Package clientip works out which client sent a request (hardening spec §4).
//
// The obvious source, the TCP peer address (r.RemoteAddr), is the reverse
// proxy's address once Caddy or the Next.js server sits in front of the API.
// Proxies report the original client in the X-Forwarded-For header, but any
// client can also send that header with made-up values. So we only believe
// it when the request really came from a proxy we trust, and even then only
// the part of it our own proxies added.
package clientip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver derives client addresses using a list of trusted proxy networks.
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver returns a Resolver that trusts X-Forwarded-For only from peers
// inside the given networks. With no networks, X-Forwarded-For is ignored.
func NewResolver(trusted []netip.Prefix) *Resolver {
	return &Resolver{trusted: trusted}
}

// ClientIP returns the client address for r. The result is invalid
// (IsValid() == false) only if even RemoteAddr can't be parsed.
//
// X-Forwarded-For is a list that each proxy appends to:
//
//	X-Forwarded-For: <whatever the client claimed>, <what proxy 1 saw>, <what proxy 2 saw>
//
// Entries on the left may be forged by the client; entries on the right were
// added by proxies. So, walking from the right, we skip our own trusted
// proxies and take the first address that isn't one of them: that is the
// last hop our infrastructure actually observed (spec H-IP-2).
func (res *Resolver) ClientIP(r *http.Request) netip.Addr {
	peer, ok := parse(r.RemoteAddr)
	if !ok || !res.isTrusted(peer) {
		// Not from a trusted proxy: the header can't be believed at all.
		return peer
	}

	// A request may carry several X-Forwarded-For header lines; together
	// they form one list, in order.
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, ok := parse(strings.TrimSpace(hops[i]))
		if !ok {
			continue // malformed entries are skipped, never trusted (H-IP-3)
		}
		if !res.isTrusted(addr) {
			return addr
		}
	}
	return peer
}

func (res *Resolver) isTrusted(addr netip.Addr) bool {
	for _, p := range res.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// parse accepts "1.2.3.4", "1.2.3.4:80", "2001:db8::1" and "[2001:db8::1]:80",
// and normalises the result (spec H-IP-3):
//   - IPv4-mapped IPv6 ("::ffff:1.2.3.4") becomes plain IPv4, so the same
//     client always gets the same key whichever form it arrives in;
//   - IPv6 zones ("fe80::1%eth0") are dropped; they're local interface names.
func parse(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host // had a port (and brackets, for IPv6)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// ctxKey is unexported so no other package can collide with our context key.
type ctxKey struct{}

// WithAddr returns a copy of ctx carrying addr.
func WithAddr(ctx context.Context, addr netip.Addr) context.Context {
	return context.WithValue(ctx, ctxKey{}, addr)
}

// FromContext returns the address stored by WithAddr.
func FromContext(ctx context.Context) (netip.Addr, bool) {
	addr, ok := ctx.Value(ctxKey{}).(netip.Addr)
	return addr, ok
}
