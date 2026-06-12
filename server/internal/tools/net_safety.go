package tools

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// isPublicIP rejects loopback, private (RFC1918 + ULA fc00::/7), link-local and
// unspecified addresses — the destinations an SSRF attack tries to reach.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return true
}

// ssrfSafeClient returns an http.Client that, per design.md §4.4:
//   - only connects to ports 80/443,
//   - validates the *resolved* IP at dial time (defeats DNS-rebinding and any
//     redirect hop, because Control runs for every physical connection),
//   - caps redirects.
func ssrfSafeClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if port != "80" && port != "443" {
				return errors.New("blocked non-web port: " + port)
			}
			if ip := net.ParseIP(host); !isPublicIP(ip) {
				return errors.New("blocked private/loopback host")
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: 25 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil // each hop's IP+port is re-validated by Dialer.Control
		},
	}
}
