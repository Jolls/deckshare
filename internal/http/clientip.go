package http

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// proxySet is a parsed TRUSTED_PROXY value: the set of CIDR prefixes whose X-Forwarded-For
// header this server trusts. A nil proxySet means "trust nothing" -- the header is never read
// and clientIP behaves exactly like remoteHost.
type proxySet []netip.Prefix

// parseTrustedProxies parses a comma-separated list of CIDR prefixes (the TRUSTED_PROXY env
// value). Whitespace around each element is trimmed and empty elements are skipped. A bare IP
// (no "/32" or "/128") is a parse error -- one accepted form, one code path. An empty or
// whitespace-only input returns a nil proxySet and no error.
func parseTrustedProxies(s string) (proxySet, error) {
	var out proxySet
	for _, elem := range strings.Split(s, ",") {
		elem = strings.TrimSpace(elem)
		if elem == "" {
			continue
		}
		p, err := netip.ParsePrefix(elem)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w (did you mean to add /32 or /128?)", elem, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// contains reports whether a is inside any prefix in t.
func (t proxySet) contains(a netip.Addr) bool {
	for _, p := range t {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP returns the request's client IP for use as a rate-limit key.
//
// If no trusted proxies are configured, X-Forwarded-For is never read and this returns the
// same value as remoteHost -- today's behaviour, unchanged. Otherwise, if the immediate peer
// (r.RemoteAddr) is one of the trusted proxies, X-Forwarded-For is walked right to left,
// skipping hops that are themselves trusted proxies (an internal chain may have more than one),
// and the first untrusted hop found is returned as the client IP. A malformed hop stops the
// walk and falls back to the peer, since everything to the right of a trusted hop was appended
// by our own proxies and is expected to be well-formed -- garbage can only come from the
// attacker-controlled left portion, and the trust decision has already been spent by the time
// we'd reach it. If every hop is trusted, or the header is absent/empty, or the peer itself
// isn't trusted, this falls back to the peer.
func (t proxySet) clientIP(r *http.Request) string {
	peer := remoteHost(r)
	if len(t) == 0 {
		return peer
	}

	peerAddr, err := netip.ParseAddr(peer)
	if err != nil {
		return peer
	}
	peerAddr = peerAddr.Unmap().WithZone("")
	if !t.contains(peerAddr) {
		return peer
	}

	joined := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if joined == "" {
		return peer
	}
	hops := strings.Split(joined, ",")

	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		addr, err := netip.ParseAddr(hop)
		if err != nil {
			return peer
		}
		addr = addr.Unmap().WithZone("")
		if t.contains(addr) {
			continue
		}
		return addr.String()
	}

	return peer
}

// remoteHost is today's clientIP body: the host portion of r.RemoteAddr, or r.RemoteAddr
// verbatim if it isn't a host:port pair.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
