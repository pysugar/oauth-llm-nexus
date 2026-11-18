package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_ServesIndexAndAssets(t *testing.T) {
	h := Handler()

	for _, path := range []string{"/ui", "/ui/", "/ui/providers"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d", path, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s expected text/html content type, got %q", path, rec.Header().Get("Content-Type"))
		}
		if !strings.Contains(rec.Body.String(), "/ui/app.js") || !strings.Contains(rec.Body.String(), "/ui/app.css") ||
			!strings.Contains(rec.Body.String(), `id="clientKeysList"`) {
			t.Fatalf("GET %s missing asset references", path)
		}
	}

	for _, asset := range []struct {
		path string
		want string
	}{
		{path: "/ui/app.css", want: "--seg-track"},
		{path: "/ui/app.css", want: ".runtime-controls"},
		{path: "/ui/app.css", want: ".key-pills"},
		{path: "/ui/app.js", want: "/api/gateway/test"},
		{path: "/ui/app.js", want: "/api/gateway/stats"},
		{path: "/ui/app.js", want: "/api/gateway/stats/reset"},
		{path: "/ui/app.js", want: "/api/gateway/models/matrix"},
		{path: "/ui/app.js", want: `"PATCH"`},
		{path: "/ui/app.js", want: "/api/gateway/keys"},
		{path: "/ui/app.css", want: ".key-row"},
		{path: "/ui/favicon.svg", want: "<svg"},
	} {
		req := httptest.NewRequest(http.MethodGet, asset.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d", asset.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), asset.want) {
			t.Fatalf("GET %s missing expected content %q", asset.path, asset.want)
		}
	}
}
