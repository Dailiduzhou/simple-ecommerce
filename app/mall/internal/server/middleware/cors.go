package middleware

import (
	"net/http"
	"strings"
)

// CORS returns a filter that answers cross-origin preflights and adds CORS
// headers for the comma-separated origins from server.http.cors_allowed_origins.
// An empty value disables CORS entirely. "*" allows every origin by echoing the
// request origin, because wildcard origins cannot be combined with credentials.
// The frontend is deployed separately from this API, so this filter is the only
// CORS policy: the reverse proxy must not add competing headers.
func CORS(origins string) func(http.Handler) http.Handler {
	allowAll := false
	allowed := make(map[string]struct{})
	for _, origin := range strings.Split(origins, ",") {
		origin = strings.TrimSpace(origin)
		switch origin {
		case "":
		case "*":
			allowAll = true
		default:
			allowed[origin] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		if !allowAll && len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Caches must not reuse a response across origins, even when this
			// particular request is not allowed.
			w.Header().Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			_, listed := allowed[origin]
			if origin == "" || (!allowAll && !listed) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			if method := r.Header.Get("Access-Control-Request-Method"); r.Method == http.MethodOptions && method != "" {
				w.Header().Set("Access-Control-Allow-Methods", method)
				w.Header().Set("Access-Control-Max-Age", "600")
				if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
					w.Header().Set("Access-Control-Allow-Headers", headers)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
