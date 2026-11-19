package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminGuardHostAndCrossSite(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := AdminGuard(AdminGuardConfig{BindHost: "0.0.0.0", ExtraHosts: ParseAdminHosts("nexus.lan, *.home.arpa")})(ok)

	cases := []struct {
		name, method, host, path string
		hdr                      map[string]string
		want                     int
	}{
		{"loopback name", "GET", "localhost:8080", "/api/version", nil, 200},
		{"ipv6 literal", "GET", "[::1]:8080", "/api/version", nil, 200},
		{"lan ip", "POST", "192.168.1.5:8086", "/api/refresh", nil, 200},
		{"extra host", "GET", "nexus.lan:8086", "/ui", nil, 200},
		{"wildcard host", "GET", "box.home.arpa", "/ui", nil, 200},
		{"rebinding host", "GET", "evil.example:8080", "/api/config/apikey", nil, 403},
		{"cross-site fetch", "POST", "localhost:8080", "/api/config/apikey/regenerate", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"foreign origin", "POST", "localhost:8080", "/api/model-routes/reset", map[string]string{"Origin": "https://evil.example"}, 403},
		{"null origin", "PATCH", "localhost:8080", "/api/gateway/providers/x", map[string]string{"Origin": "null"}, 403},
		{"same origin", "POST", "localhost:8080", "/api/refresh", map[string]string{"Origin": "http://localhost:8080", "Sec-Fetch-Site": "same-origin"}, 200},
		{"cross-site GET ok", "GET", "localhost:8080", "/ui", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"curl post", "POST", "127.0.0.1:8080", "/api/refresh", nil, 200},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Host = c.host
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, rec.Code, c.want)
		}
		if c.want == 200 && strings.HasPrefix(c.path, "/api/") && rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: missing no-store", c.name)
		}
	}
}
