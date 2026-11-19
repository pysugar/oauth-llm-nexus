package middleware

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// AdminGuardConfig lists hostnames, besides loopback names and IP literals, under which the
// admin surface (/, /ui, /api, /tools, /monitor) may be reached.
type AdminGuardConfig struct {
	BindHost   string   // HOST the server listens on (a hostname here is allowed)
	ExtraHosts []string // NEXUS_ADMIN_HOSTS entries: exact names or "*.suffix"
}

// ParseAdminHosts splits a comma/space separated host list ("nexus.lan,*.home.arpa").
func ParseAdminHosts(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// AdminGuard protects the unauthenticated-by-default admin surface from browsers:
//   - DNS rebinding: the Host header must be loopback, an IP literal, the bind host or a configured
//     extra host (a rebinding attack always needs an attacker-controlled hostname);
//   - CSRF: state-changing requests are refused when Sec-Fetch-Site says cross-site or the Origin
//     does not match the Host. Requests without either header (curl, scripts) pass.
//
// It also marks /api answers as non-cacheable and forbids framing the console.
func AdminGuard(cfg AdminGuardConfig) func(http.Handler) http.Handler {
	bind := strings.ToLower(strings.TrimSpace(cfg.BindHost))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := hostOnly(r.Host)
			if !adminHostAllowed(host, bind, cfg.ExtraHosts) {
				log.Printf("🛡️ admin request refused: Host %q is not allowed (set NEXUS_ADMIN_HOSTS to permit it)", r.Host)
				http.Error(w, "Forbidden: host not allowed (set NEXUS_ADMIN_HOSTS)", http.StatusForbidden)
				return
			}
			if isUnsafeMethod(r.Method) && crossSite(r) {
				http.Error(w, "Forbidden: cross-site request", http.StatusForbidden)
				return
			}
			h := w.Header()
			if strings.HasPrefix(r.URL.Path, "/api/") {
				h.Set("Cache-Control", "no-store")
			} else {
				h.Set("X-Frame-Options", "DENY")
			}
			h.Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	}
}

func hostOnly(hostport string) string {
	h := strings.ToLower(strings.TrimSpace(hostport))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.Trim(h, "[]")
}

func adminHostAllowed(host, bind string, extra []string) bool {
	if host == "" {
		return true // HTTP/1.0 without Host: cannot be a rebinding browser request
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host) != nil {
		return true
	}
	if bind != "" && bind != "0.0.0.0" && bind != "::" && host == bind {
		return true
	}
	for _, e := range extra {
		if e == host || (strings.HasPrefix(e, "*.") && strings.HasSuffix(host, e[1:])) {
			return true
		}
	}
	return false
}

func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func crossSite(r *http.Request) bool {
	switch strings.ToLower(r.Header.Get("Sec-Fetch-Site")) {
	case "", "same-origin", "none":
	default:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if origin == "null" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return true
	}
	return !strings.EqualFold(u.Host, r.Host)
}
