package middleware

import (
	"context"
	"net"
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

func CommunityWriteLimit(l biz.WriteLimiter) middleware.Middleware {
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
			c, ok := biz.ClaimsFromContext(ctx)
			if !ok || c == nil {
				return next(ctx, req)
			} // public login/register keep their existing path
			// Reads never touch the limiter: a missing or failing Redis must not take
			// down history, feed or detail endpoints.
			if biz.RateLimitCategory(op) != "" {
				if l == nil {
					return nil, errors.ServiceUnavailable("RATE_LIMIT_UNAVAILABLE", "write limiter is not configured")
				}
				address := "unknown"
				if r, ok := kratoshttp.RequestFromServerContext(ctx); ok {
					address = r.RemoteAddr
				} else if p, ok := peer.FromContext(ctx); ok {
					address = p.Addr.String()
				}
				if host, _, e := net.SplitHostPort(address); e == nil {
					address = host
				}
				// Forwarded headers are untrusted. A proxy must enforce its own IP quota or
				// supply a separately configured trusted-proxy policy, not arbitrary XFF.
				if e := l.Allow(ctx, c.UserID, address, op); e != nil {
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
		if strings.HasPrefix(path, "/v1/posts") || strings.HasPrefix(path, "/v1/media/") || strings.HasPrefix(path, "/v1/users/me/browsing-history") {
			if r.ContentLength > 64<<10 {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		next.ServeHTTP(w, r)
	})
}
