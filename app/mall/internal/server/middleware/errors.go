package middleware

import (
	"context"
	stderrors "errors"
	"net/http"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

// PublicError preserves domain errors, not driver, SDK or codec diagnostics.
func PublicError(err error) error {
	if err == nil {
		return nil
	}
	var domain *errors.Error
	if stderrors.As(err, &domain) {
		if domain.Reason == "CODEC" {
			return errors.BadRequest("INVALID_ARGUMENT", "invalid request parameters")
		}
		// Return the domain error itself: a wrapping error may contain private data.
		return domain
	}
	return errors.InternalServer("INTERNAL", "internal error")
}

// SanitizeErrors also covers gRPC and recovery errors. Install outside recovery.
func SanitizeErrors(logger log.Logger) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			reply, err := next(ctx, req)
			if err != nil && errors.Code(err) >= 500 {
				log.NewHelper(logger).WithContext(ctx).Errorf("request failed: %v", err)
			}
			return reply, PublicError(err)
		}
	}
}

// Codec failures happen before middleware, so HTTP needs an encoder too.
func ErrorEncoder(w http.ResponseWriter, r *http.Request, err error) {
	khttp.DefaultErrorEncoder(w, r, PublicError(err))
}
