package web

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// clientIP is the client's address: the peer, or behind a trusted proxy
// the last address in X-Forwarded-For that is not a trusted proxy. A
// missing or malformed header means the peer.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	peer := peerAddr(r)
	if !s.trustedProxy(peer) {
		return peer
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer
		}
		if a = a.Unmap().WithZone(""); !s.trustedProxy(a) {
			return a
		}
	}
	return peer
}

func peerAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	if a, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		return a.Unmap().WithZone("")
	}
	return netip.Addr{}
}

func (s *Server) trustedProxy(a netip.Addr) bool {
	return a.IsValid() && slices.ContainsFunc(s.trustedProxies, func(p netip.Prefix) bool { return p.Contains(a) })
}

// clientAddr is clientIP as a string, for the login limiter: behind a
// trusted proxy it counts per client again.
func (s *Server) clientAddr(r *http.Request) string {
	if a := s.clientIP(r); a.IsValid() {
		return a.String()
	}
	return r.RemoteAddr
}
