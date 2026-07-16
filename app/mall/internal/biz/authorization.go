package biz

import "context"

type claimsContextKey struct{}

func WithClaims(ctx context.Context, claims *EcommerceClaims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

func ClaimsFromContext(ctx context.Context) (*EcommerceClaims, bool) {
	claims, ok := ctx.Value(claimsContextKey{}).(*EcommerceClaims)
	return claims, ok
}
