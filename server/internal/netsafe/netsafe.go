// Package netsafe provides SSRF-resistant HTTP clients shared by the tools
// (web_fetch) and rag (MinerU zip download) packages. The core guarantee is a
// dial-time check of the *resolved* IP for every physical connection, which
// defeats DNS-rebinding and redirect-to-internal hops.
package netsafe

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"

	"aurelia/server/internal/envcfg"
)

var (
	ssrfClientDialTimeout = envcfg.Dur("AURELIA_NETSAFE_NETSAFE_SSRF_CLIENT_DIAL_TIMEOUT", 10*time.Second)
	maxIdleConns          = envcfg.Int("AURELIA_NETSAFE_MAX_IDLE_CONNS", 10)
	idleConnTimeout       = envcfg.Dur("AURELIA_NETSAFE_IDLE_CONN_TIMEOUT", 30*time.Second)
	tlsHandshakeTimeout   = envcfg.Dur("AURELIA_NETSAFE_TLSHANDSHAKE_TIMEOUT", 10*time.Second)
	maxRedirects          = envcfg.Int("AURELIA_NETSAFE_NETSAFE_REDIRECTS", 5)
)

// extraDeny lists ranges that Go's IP predicates don't classify as private but
// commonly front internal infrastructure / cloud metadata (§F4): carrier-grade
// NAT, NAT64 (which maps to link-local metadata on hosts with a NAT64 gateway),
// IETF protocol assignments, and the TEST-NET / benchmarking blocks.
var extraDeny = func() []*net.IPNet {
	cidrs := []string{
		"100.64.0.0/10",   // RFC 6598 CGNAT
		"192.0.0.0/24",    // RFC 6890 IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // RFC 2544 benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"64:ff9b::/96",    // RFC 6052 NAT64 well-known prefix
		"2001:db8::/32",   // documentation
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// IsPublicIP rejects loopback, RFC1918/ULA private, link-local, unspecified,
// multicast — plus the extraDeny ranges above. Go normalizes IPv4-mapped IPv6
// (e.g. ::ffff:169.254.169.254) inside the predicate methods, so those are
// caught too.
func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	for _, n := range extraDeny {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

func makeClient(clientTimeout time.Duration, restrictPort bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: ssrfClientDialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if restrictPort && port != "80" && port != "443" {
				return errors.New("blocked non-web port: " + port)
			}
			if ip := net.ParseIP(host); !IsPublicIP(ip) {
				return errors.New("blocked private/loopback host")
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: clientTimeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			MaxIdleConns:        maxIdleConns,
			IdleConnTimeout:     idleConnTimeout,
			TLSHandshakeTimeout: tlsHandshakeTimeout,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			return nil // each hop's IP+port is re-validated by Dialer.Control
		},
	}
}

// SafeClient is the strict client for arbitrary user/model-driven fetches:
// public IPs and ports 80/443 only.
func SafeClient(clientTimeout time.Duration) *http.Client { return makeClient(clientTimeout, true) }

// PrivateBlockClient blocks private/internal IPs (DNS-rebind-safe) but allows
// any port — for semi-trusted server-to-server downloads (e.g. a MinerU-returned
// object-storage URL) that may use a non-standard port.
func PrivateBlockClient(clientTimeout time.Duration) *http.Client {
	return makeClient(clientTimeout, false)
}
