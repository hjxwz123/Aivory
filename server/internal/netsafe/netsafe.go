// Package netsafe provides SSRF-resistant HTTP clients shared by the tools
// (web_fetch) and rag (MinerU zip download) packages. The core guarantee is a
// dial-time check of the *resolved* IP for every physical connection, which
// defeats DNS-rebinding and redirect-to-internal hops.
package netsafe

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var (
	ssrfClientDialTimeout = 10 * time.Second
	maxIdleConns          = 10
	idleConnTimeout       = 30 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	maxRedirects          = 5
)

// extraDeny lists ranges that Go's IP predicates don't classify as private but
// commonly front internal infrastructure / cloud metadata (§F4): carrier-grade
// NAT, NAT64 (which maps to link-local metadata on hosts with a NAT64 gateway),
// IETF protocol assignments, and the TEST-NET / benchmarking blocks.
var extraDeny = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",         // RFC 6890 this-network addresses
		"100.64.0.0/10",     // RFC 6598 CGNAT
		"192.0.0.0/24",      // RFC 6890 IETF protocol assignments
		"192.0.2.0/24",      // TEST-NET-1
		"198.18.0.0/15",     // RFC 2544 benchmarking
		"198.51.100.0/24",   // TEST-NET-2
		"203.0.113.0/24",    // TEST-NET-3
		"240.0.0.0/4",       // reserved, including limited broadcast
		"64:ff9b::/96",      // RFC 6052 NAT64 well-known prefix
		"64:ff9b:1::/48",    // RFC 8215 local-use NAT64 prefix
		"fd00:ec2::254/128", // AWS EC2 Instance Metadata Service (IPv6)
		"2001:db8::/32",     // documentation
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
	if !isPublicIPLike(ip) {
		return false
	}
	return !deniedByExtraRanges(ip)
}

// IsUserMCPDialAllowed is the dial-time gate for USER-configured MCP endpoints.
// It admits RFC1918/private LAN targets (the contract requires LAN access) but
// rejects loopback, link-local (incl. the 169.254.169.254 cloud-metadata
// convention), unspecified, multicast, and the extraDeny ranges — so no user
// server can reach the machine itself or cloud metadata, even through a
// DNS-rebinding collapse. Admin MCP servers keep the bare http.Client and go
// through no such gate.
func IsUserMCPDialAllowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return !deniedByExtraRanges(ip)
}

func deniedByExtraRanges(ip net.IP) bool {
	for _, n := range extraDeny {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func isPublicIPLike(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func makeClient(clientTimeout time.Duration, restrictPort bool, dialAllowed func(net.IP) bool) *http.Client {
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
			if ip := net.ParseIP(host); !dialAllowed(ip) {
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
func SafeClient(clientTimeout time.Duration) *http.Client {
	return makeClient(clientTimeout, true, IsPublicIP)
}

// PrivateBlockClient blocks private/internal IPs (DNS-rebind-safe) but allows
// any port — for semi-trusted server-to-server downloads (e.g. a MinerU-returned
// object-storage URL) that may use a non-standard port.
func PrivateBlockClient(clientTimeout time.Duration) *http.Client {
	return makeClient(clientTimeout, false, IsPublicIP)
}

// UserMCPAllowedClient is the runtime transport for USER-owned MCP endpoints. It
// allows RFC1918/private LAN hosts and any port, while still rejecting
// loopback/link-local/metadata at dial time (resolve-then-dial defeats DNS
// rebinding). Admin MCP endpoints deliberately use the bare client so the two
// trust domains cannot drift.
func UserMCPAllowedClient(clientTimeout time.Duration) *http.Client {
	client := makeClient(clientTimeout, false, IsUserMCPDialAllowed)
	// Unlike arbitrary web fetches, user MCP requests carry caller-supplied
	// credentials. Keep redirects on the same authority so net/http cannot copy
	// custom headers (Authorization, API keys, etc.) to an unrelated origin.
	// HTTP -> HTTPS on the same host is allowed; HTTPS -> HTTP is rejected to
	// avoid downgrading credentials onto a plaintext hop.
	client.CheckRedirect = checkUserMCPRedirect
	return client
}

func checkUserMCPRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	if req == nil || req.URL == nil || len(via) == 0 || via[len(via)-1] == nil || via[len(via)-1].URL == nil {
		return errors.New("invalid MCP redirect")
	}
	// via is ordered oldest first. Redirect security properties are per-hop: in
	// particular, an http -> https -> http chain must compare the final request
	// with the immediately preceding HTTPS request and reject the downgrade.
	from := via[len(via)-1].URL
	to := req.URL
	if !isHTTPURL(from) || !isHTTPURL(to) {
		return errors.New("unsupported MCP redirect scheme")
	}
	if strings.EqualFold(from.Scheme, "https") && strings.EqualFold(to.Scheme, "http") {
		return errors.New("insecure MCP redirect blocked")
	}
	if !sameMCPRedirectAuthority(from, to) {
		return errors.New("cross-origin MCP redirect blocked")
	}
	return nil
}

func isHTTPURL(value *url.URL) bool {
	if value == nil {
		return false
	}
	return strings.EqualFold(value.Scheme, "http") || strings.EqualFold(value.Scheme, "https")
}

func sameMCPRedirectAuthority(from, to *url.URL) bool {
	if from == nil || to == nil {
		return false
	}
	fromHost := strings.ToLower(strings.TrimSuffix(from.Hostname(), "."))
	toHost := strings.ToLower(strings.TrimSuffix(to.Hostname(), "."))
	if fromHost == "" || fromHost != toHost {
		return false
	}
	fromPort, fromExplicit := mcpURLPort(from)
	toPort, toExplicit := mcpURLPort(to)
	if fromPort == toPort {
		return true
	}
	// A default HTTP -> HTTPS transition has the conventional 80/443 pair.
	// It remains same-host and is the only implicit port change we permit.
	if !fromExplicit && !toExplicit && ((strings.EqualFold(from.Scheme, "http") && strings.EqualFold(to.Scheme, "https")) ||
		(strings.EqualFold(from.Scheme, "https") && strings.EqualFold(to.Scheme, "http"))) {
		return (fromPort == "80" && toPort == "443") || (fromPort == "443" && toPort == "80")
	}
	return false
}

func mcpURLPort(value *url.URL) (port string, explicit bool) {
	if value == nil {
		return "", false
	}
	if raw := value.Port(); raw != "" {
		return raw, true
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443", false
	}
	return "80", false
}
