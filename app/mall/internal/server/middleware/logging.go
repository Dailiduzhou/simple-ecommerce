package middleware

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
)

// SafeLogging deliberately omits request arguments. Generated protobuf String
// methods include passwords, tokens, phone numbers and callback signatures, so
// generic request logging is not a safe redaction boundary.
func SafeLogging(logger log.Logger) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			started := time.Now()
			operation, kind := "", ""
			if info, ok := transport.FromServerContext(ctx); ok {
				operation, kind = info.Operation(), info.Kind().String()
			}
			reply, err := next(ctx, req)
			code, reason := int32(200), ""
			level := log.LevelInfo
			if err != nil {
				converted := errors.FromError(err)
				code, reason, level = converted.Code, converted.Reason, log.LevelError
			}
			log.NewHelper(log.WithContext(ctx, logger)).Log(level, "kind", "server", "component", kind, "operation", operation, "code", code, "reason", reason, "latency", time.Since(started).Seconds())
			return reply, err
		}
	}
}
