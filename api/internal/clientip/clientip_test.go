package clientip_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/Nurasick/NUPP/api/internal/clientip"
)

// HAC-2
func TestResolver_ClientIP(t *testing.T) {
	proxyNet := []netip.Prefix{netip.MustParsePrefix("172.30.0.0/24"), netip.MustParsePrefix("fd00::/8")}

	cases := []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		xff     []string // one element per header line
		want    string
	}{
		{"remote addr only", nil, "203.0.113.7:51000", nil, "203.0.113.7"},
		{"XFF ignored without trusted proxies", nil, "203.0.113.7:51000", []string{"1.1.1.1"}, "203.0.113.7"},
		{"XFF ignored when peer is not a trusted proxy", proxyNet, "203.0.113.7:51000", []string{"1.1.1.1"}, "203.0.113.7"},
		{"trusted proxy: single XFF entry", proxyNet, "172.30.0.5:40000", []string{"198.51.100.9"}, "198.51.100.9"},
		{"trusted proxy: right-most untrusted wins over forged left entries", proxyNet, "172.30.0.5:40000",
			[]string{"6.6.6.6, 198.51.100.9"}, "198.51.100.9"},
		{"trusted proxy: trusted hops on the right are skipped", proxyNet, "172.30.0.5:40000",
			[]string{"198.51.100.9, 172.30.0.9"}, "198.51.100.9"},
		{"multiple header lines are read in order", proxyNet, "172.30.0.5:40000",
			[]string{"6.6.6.6", "198.51.100.9, 172.30.0.9"}, "198.51.100.9"},
		{"all entries trusted → fall back to remote", proxyNet, "172.30.0.5:40000", []string{"172.30.0.7"}, "172.30.0.5"},
		{"malformed right-most entry is skipped", proxyNet, "172.30.0.5:40000",
			[]string{"198.51.100.9, not-an-ip"}, "198.51.100.9"},
		{"only garbage → remote", proxyNet, "172.30.0.5:40000", []string{"garbage, , ???"}, "172.30.0.5"},
		{"empty XFF → remote", proxyNet, "172.30.0.5:40000", []string{""}, "172.30.0.5"},
		{"IPv4-mapped IPv6 is unmapped", nil, "[::ffff:203.0.113.7]:51000", nil, "203.0.113.7"},
		{"IPv6 remote with port", nil, "[2001:db8::1]:51000", nil, "2001:db8::1"},
		{"zone is dropped", nil, "[fe80::1%eth0]:51000", nil, "fe80::1"},
		{"XFF entry with port", proxyNet, "172.30.0.5:40000", []string{"198.51.100.9:1234"}, "198.51.100.9"},
		{"XFF entry bracketed IPv6 with port", proxyNet, "[fd00::5]:40000", []string{"[2001:db8::7]:443"}, "2001:db8::7"},
		{"XFF mapped IPv4 unmapped", proxyNet, "172.30.0.5:40000", []string{"::ffff:198.51.100.9"}, "198.51.100.9"},
		{"remote without port", nil, "203.0.113.7", nil, "203.0.113.7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remote
			for _, line := range c.xff {
				r.Header.Add("X-Forwarded-For", line)
			}
			got := clientip.NewResolver(c.trusted).ClientIP(r)
			if got.String() != c.want {
				t.Errorf("ClientIP = %v, want %s", got, c.want)
			}
		})
	}
}

func TestResolver_UnparseableRemoteAddrIsInvalid(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "nonsense"
	if got := clientip.NewResolver(nil).ClientIP(r); got.IsValid() {
		t.Fatalf("ClientIP = %v, want invalid address", got)
	}
}

func TestContextRoundTrip(t *testing.T) {
	addr := netip.MustParseAddr("198.51.100.9")
	got, ok := clientip.FromContext(clientip.WithAddr(context.Background(), addr))
	if !ok || got != addr {
		t.Fatalf("FromContext = %v, %v", got, ok)
	}
	if _, ok := clientip.FromContext(context.Background()); ok {
		t.Fatal("empty context must report no address")
	}
}
