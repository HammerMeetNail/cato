package auth

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type clientKey struct{}

type clientInfo struct {
	ip    string
	https bool
}

// ResolveClient records the client address and scheme on the request.
// X-Forwarded-For and X-Forwarded-Proto are believed only when the direct
// peer is inside a trusted prefix (CATO_TRUSTED_PROXIES); otherwise they are
// client-controlled and would let anyone rotate rate-limit identities.
// X-Forwarded-For is walked right to left, skipping trusted hops, so entries
// a client prepends itself are never used.
func ResolveClient(r *http.Request, trusted []netip.Prefix) *http.Request {
	peer := peerAddr(r.RemoteAddr)
	info := clientInfo{ip: peer, https: r.TLS != nil}
	if peerIP, err := netip.ParseAddr(peer); err == nil && isTrusted(peerIP, trusted) {
		if ip := forwardedClient(r.Header.Values("X-Forwarded-For"), trusted); ip != "" {
			info.ip = ip
		}
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			info.https = strings.EqualFold(strings.TrimSpace(strings.Split(proto, ",")[0]), "https")
		}
	}
	return r.WithContext(context.WithValue(r.Context(), clientKey{}, info))
}

// ClientIP returns the resolved client IP, or the peer address when
// ResolveClient has not run.
func ClientIP(r *http.Request) string {
	if info, ok := r.Context().Value(clientKey{}).(clientInfo); ok {
		return info.ip
	}
	return peerAddr(r.RemoteAddr)
}

// RequestIsHTTPS reports whether the browser used HTTPS for this request.
func RequestIsHTTPS(r *http.Request) bool {
	if info, ok := r.Context().Value(clientKey{}).(clientInfo); ok {
		return info.https
	}
	return r.TLS != nil
}

func peerAddr(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}

func isTrusted(ip netip.Addr, trusted []netip.Prefix) bool {
	ip = ip.Unmap()
	for _, p := range trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func forwardedClient(headers []string, trusted []netip.Prefix) string {
	var hops []netip.Addr
	for _, h := range headers {
		for _, part := range strings.Split(h, ",") {
			ip, err := netip.ParseAddr(strings.TrimSpace(part))
			if err != nil {
				// An unparseable hop means the chain can't be trusted.
				return ""
			}
			hops = append(hops, ip.Unmap())
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !isTrusted(hops[i], trusted) {
			return hops[i].String()
		}
	}
	if len(hops) > 0 {
		return hops[0].String()
	}
	return ""
}
