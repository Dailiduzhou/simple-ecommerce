package middleware

import (
	"net/http"
	"net/url"
	"strings"
)

// DevelopmentCORS permits any origin, method and requested header, including
// credentialed requests. Never install this filter in production.
func DevelopmentCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		if origin := r.Header.Get("Origin"); origin != "" {
			// Wildcard origins cannot be used with credentials. Echoing the
			// origin permits both cookie-based and bearer-token clients.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			if method := r.Header.Get("Access-Control-Request-Method"); r.Method == http.MethodOptions && method != "" {
				w.Header().Set("Access-Control-Allow-Methods", method)
				if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
					w.Header().Set("Access-Control-Allow-Headers", headers)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// AllowlistCORS never accepts wildcard, opaque/null or malformed origins.
// Credentials are opt-in; bearer Authorization headers do not require them.
func AllowlistCORS(origins []string, credentials bool) func(http.Handler) http.Handler {
	allowed := make(map[string]bool)
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		u, err := url.Parse(origin)
		if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" &&
			u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !strings.Contains(origin, "*") {
			allowed[origin] = true
		}
	}
	methods := map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}
	headers := map[string]bool{"authorization": true, "content-type": true, "idempotency-key": true}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Origin")
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			origin := r.Header.Get("Origin")
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			if !allowed[origin] {
				if preflight {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if preflight {
				if !methods[r.Header.Get("Access-Control-Request-Method")] {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
					h = strings.ToLower(strings.TrimSpace(h))
					if h != "" && !headers[h] {
						w.WriteHeader(http.StatusForbidden)
						return
					}
				}
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if credentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			if preflight {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
