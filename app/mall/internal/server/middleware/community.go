package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	kratoshttp "github.com/go-kratos/kratos/v2/transport/http"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

func observedOperation(op string) bool {
	return strings.HasPrefix(op, "/api.community.") || strings.HasPrefix(op, "/api.media.") ||
		strings.Contains(op, "BrowsingHistory") || strings.HasSuffix(op, "/RecordProductView")
}

func CommunityWriteLimit(l biz.WriteLimiter, clientIP *ClientIPResolver) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			op := ""
			if t, ok := transport.FromServerContext(ctx); ok {
				op = t.Operation()
			}
			observed := observedOperation(op)
			if observed {
				if m, ok := req.(proto.Message); ok && proto.Size(m) > 64<<10 {
					return nil, errors.BadRequest("REQUEST_TOO_LARGE", "request exceeds 64 KiB")
				}
			}
			// Login/register/refresh live in the JWT whitelist: they reach this
			// middleware without claims and must still be throttled, so a missing
			// identity no longer bypasses the limiter. Auth categories stay
			// fail-closed like the blacklist chain: without Redis the endpoint
			// is unavailable rather than unthrottled.
			var actorID int64
			if c, ok := biz.ClaimsFromContext(ctx); ok && c != nil {
				actorID = c.UserID
			}
			// The caller address is resolved for every operation (not only
			// rate-limited writes) because handlers forward it to channel risk
			// control. Forwarded headers are only trusted when the immediate peer
			// is a configured trusted proxy (server.http.trusted_proxies);
			// otherwise the peer address is used and a spoofed
			// X-Forwarded-For is ignored.
			clientAddr := ""
			if r, ok := kratoshttp.RequestFromServerContext(ctx); ok {
				clientAddr = clientIP.ClientIP(r.RemoteAddr, r.Header)
			} else if p, ok := peer.FromContext(ctx); ok {
				clientAddr = clientIP.ClientIP(p.Addr.String(), nil)
			}
			ctx = WithClientIP(ctx, clientAddr)
			// Reads never touch the limiter: a missing or failing Redis must not take
			// down history, feed or detail endpoints.
			if biz.RateLimitCategory(op) != "" {
				if l == nil {
					return nil, errors.ServiceUnavailable("RATE_LIMIT_UNAVAILABLE", "write limiter is not configured")
				}
				address := clientAddr
				if address == "" {
					address = "unknown"
				}
				if e := l.Allow(ctx, actorID, address, op); e != nil {
					return nil, e
				}
			}
			reply, err := next(ctx, req)
			if observed {
				result := "success"
				if err != nil {
					result = "error"
				}
				observability.CommunityEvent(ctx, op, result)
			}
			return reply, err
		}
	}
}

func CommunityBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Every JSON write path the API exposes is capped, including the
		// /v1/users/* profile and password writes that previously slipped past
		// this filter (payment callbacks keep their provider-specific bodies).
		if strings.HasPrefix(path, "/v1/posts") || strings.HasPrefix(path, "/v1/media/") ||
			strings.HasPrefix(path, "/v1/users") || strings.HasPrefix(path, "/v1/orders") {
			if r.ContentLength > 64<<10 {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		next.ServeHTTP(w, r)
	})
}
