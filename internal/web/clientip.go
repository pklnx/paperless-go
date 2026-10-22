package web

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/pklnx/paperless-go/internal/store"
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

// clientAddr is the key the login limits and the passkey caps count per
// client: clientIP, behind a trusted proxy per client again. An IPv6
// address counts per /64, the block one host or network usually gets, so
// that changing addresses within it does not escape the limits. IPv4 (also
// IPv4-mapped IPv6) counts per address. Logs and the audit log keep the
// full address (clientIP).
func (s *Server) clientAddr(r *http.Request) string {
	return s.clientKeys(r).Addr
}

// clientKeys are clientAddr and the client's networks, for the caps on
// open passkey logins. Without an address there are no networks.
func (s *Server) clientKeys(r *http.Request) store.ClientKeys {
	if a := s.clientIP(r); a.IsValid() {
		return limitKeys(a)
	}
	return store.ClientKeys{Addr: r.RemoteAddr}
}

// limitKeys groups an address for the limits: IPv6 by /64, with its /56
// and /48 as networks; IPv4 by address, with its /24 as the middle network
// and no wide one.
func limitKeys(a netip.Addr) store.ClientKeys {
	a = a.Unmap().WithZone("")
	prefix := func(bits int) string { return netip.PrefixFrom(a, bits).Masked().String() }
	if a.Is6() {
		return store.ClientKeys{Addr: prefix(64), NetMid: prefix(56), NetWide: prefix(48)}
	}
	return store.ClientKeys{Addr: a.String(), NetMid: prefix(24)}
}
