package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

type clientIPKey struct{}

// WithClientIP records the resolved caller address so handlers can forward it
// to channel risk control instead of trusting a request field.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIPFromContext returns the address resolved by the transport middleware,
// or "" when the request did not pass through it.
func ClientIPFromContext(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

// ClientIPResolver maps a transport peer to the rate-limiting identity of the
// caller.
//
// Forwarded headers are attacker-controlled, so X-Forwarded-For is only
// consulted when the immediate peer is a configured trusted proxy. Without a
// trusted-proxy entry (the default) the peer address is used verbatim, which
// keeps a deployment behind an unconfigured proxy from letting any client
// spoof its way into another quota bucket.
type ClientIPResolver struct {
	trusted []*net.IPNet
}

func NewClientIPResolver(trustedProxies []string) (*ClientIPResolver, error) {
	resolver := &ClientIPResolver{}
	for _, raw := range trustedProxies {
		cidr := strings.TrimSpace(raw)
		if cidr == "" {
			continue
		}
		if !strings.Contains(cidr, "/") {
			ip := net.ParseIP(cidr)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", raw)
			}
			if ip.To4() != nil {
				cidr += "/32"
			} else {
				cidr += "/128"
			}
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: %w", raw, err)
		}
		resolver.trusted = append(resolver.trusted, network)
	}
	return resolver, nil
}

func (r *ClientIPResolver) Trusts(ip net.IP) bool {
	if r == nil || ip == nil {
		return false
	}
	for _, network := range r.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address that identifies the caller for rate limiting.
func (r *ClientIPResolver) ClientIP(peer string, header http.Header) string {
	host := peer
	if h, _, err := net.SplitHostPort(peer); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if !r.Trusts(ip) {
		return host
	}
	forwarded := header.Get("X-Forwarded-For")
	if forwarded == "" {
		return host
	}
	entries := strings.Split(forwarded, ",")
	// The rightmost entry is the hop the proxy itself saw; walk left until an
	// address that is not a trusted proxy is found, which is the client.
	for i := len(entries) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(entries[i])
		if candidate == "" {
			continue
		}
		parsed := net.ParseIP(candidate)
		if parsed == nil {
			return host
		}
		if !r.Trusts(parsed) {
			return candidate
		}
	}
	return host
}
