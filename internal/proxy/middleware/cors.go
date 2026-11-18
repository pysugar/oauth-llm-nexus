package middleware

import (
	"net/http"
	"strings"
)

// DefaultCORSPathPrefixes limits cross-origin access to inference APIs; admin (/api, /ui) stays same-origin.
var DefaultCORSPathPrefixes = []string{"/v1/", "/v1beta/", "/anthropic/"}

const corsExposeHeaders = "X-Nexus-Provider, X-Nexus-Upstream-Model, X-Nexus-Route, X-Nexus-Route-Source"

// ParseCORSOrigins splits a comma/space separated origin allowlist such as
// "http://localhost:*,tauri://localhost,https://app.example.com" (or "*").
func ParseCORSOrigins(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimRight(strings.TrimSpace(f), "/"); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// CORS answers preflight requests and decorates responses for allowed origins on inference paths.
// It must be installed on the root router so OPTIONS preflights bypass API-key auth and route
// method matching. With an empty allowlist it is a no-op.
func CORS(allowed []string, pathPrefixes []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || !hasAnyPrefix(r.URL.Path, pathPrefixes) || !originAllowed(origin, allowed) {
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", corsExposeHeaders)

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
					h.Set("Access-Control-Allow-Headers", reqHeaders)
				}
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func hasAnyPrefix(path string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// originAllowed matches exact origins, "*", and "scheme://host:*" port wildcards.
func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		switch {
		case a == "*" || a == origin:
			return true
		case strings.HasSuffix(a, ":*"):
			base := strings.TrimSuffix(a, "*")
			if origin == strings.TrimSuffix(base, ":") {
				return true
			}
			if strings.HasPrefix(origin, base) && isDigits(origin[len(base):]) {
				return true
			}
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
