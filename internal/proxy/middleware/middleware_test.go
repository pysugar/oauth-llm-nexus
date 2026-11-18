package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

func TestCORSPreflightAndOriginMatching(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := CORS(ParseCORSOrigins("http://localhost:*, tauri://localhost"), DefaultCORSPathPrefixes)(okHandler)

	pre := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	pre.Header.Set("Origin", "http://localhost:5173")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	pre.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent ||
		rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		rec.Header().Get("Access-Control-Allow-Headers") != "authorization, content-type" {
		t.Fatalf("unexpected preflight response %d %+v", rec.Code, rec.Header())
	}

	cases := []struct {
		origin, path string
		allowed      bool
	}{
		{"tauri://localhost", "/v1/embeddings", true},
		{"http://localhost", "/anthropic/v1/messages", true},
		{"http://localhost:8080.evil.com", "/v1/models", false},
		{"https://evil.example", "/v1/models", false},
		{"http://localhost:5173", "/api/gateway/providers", false}, // admin stays same-origin
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, c.path, nil)
		req.Header.Set("Origin", c.origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("Access-Control-Allow-Origin") != ""
		if got != c.allowed || rec.Code != http.StatusOK {
			t.Errorf("origin=%s path=%s: allowed=%v code=%d", c.origin, c.path, got, rec.Code)
		}
	}

	// Empty allowlist is a no-op.
	noop := CORS(nil, DefaultCORSPathPrefixes)(okHandler)
	rec = httptest.NewRecorder()
	noop.ServeHTTP(rec, pre)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("expected passthrough without CORS config, got %d %+v", rec.Code, rec.Header())
	}
}

func TestAPIKeyAuthCacheFollowsRegenerate(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Config{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb.Create(&models.Config{Key: "api_key", Value: "sk-old"})

	h := APIKeyAuth(gdb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func(header, value string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set(header, value)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if call("Authorization", "Bearer sk-old") != http.StatusOK || call("x-api-key", "sk-old") != http.StatusOK {
		t.Fatal("expected old key accepted")
	}
	if call("Authorization", "Bearer sk-wrong") != http.StatusUnauthorized {
		t.Fatal("expected wrong key rejected")
	}
	newKey := db.RegenerateAPIKey(gdb)
	if call("Authorization", "Bearer sk-old") != http.StatusUnauthorized {
		t.Fatal("old key must be rejected after regenerate")
	}
	if call("x-goog-api-key", newKey) != http.StatusOK {
		t.Fatal("new key must be accepted after regenerate")
	}
}

func TestAPIKeyAuthResolvesClientKeysAndGatesRawRoutes(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Config{}, &models.ClientKey{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var seen *clientkey.Caller
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = clientkey.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	rtr := chi.NewRouter()
	rtr.Route("/v1", func(r chi.Router) {
		r.Use(APIKeyAuth(gdb))
		r.Post("/chat/completions", ok)
	})
	rtr.Route("/{provider}", func(r chi.Router) {
		r.Use(APIKeyAuth(gdb))
		r.Use(RequireProvider(func(r *http.Request) string { return chi.URLParam(r, "provider") }))
		r.HandleFunc("/*", ok)
	})
	call := func(path, header, value string) *httptest.ResponseRecorder {
		seen = nil
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if header != "" {
			req.Header.Set(header, value)
		}
		rec := httptest.NewRecorder()
		rtr.ServeHTTP(rec, req)
		return rec
	}

	// No legacy key, but a client key exists: authentication is enforced.
	_, pkmKey, err := clientkey.Create(gdb, clientkey.Spec{Name: "pkm-app", AllowedProviders: []string{"ollama"}})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if rec := call("/v1/chat/completions", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", rec.Code)
	}
	if rec := call("/v1/chat/completions", "Authorization", "Bearer "+pkmKey); rec.Code != http.StatusOK || seen == nil || seen.Name != "pkm-app" {
		t.Fatalf("expected pkm-app caller, got %d %+v", rec.Code, seen)
	}
	if rec := call("/v1/chat/completions?key="+pkmKey, "", ""); rec.Code != http.StatusOK || seen.Name != "pkm-app" {
		t.Fatalf("expected ?key= client key accepted, got %d", rec.Code)
	}

	// Legacy key resolves to the unrestricted legacy caller.
	gdb.Create(&models.Config{Key: "api_key", Value: "sk-legacy"})
	db.InvalidateAPIKeyCache(gdb)
	if rec := call("/v1/chat/completions", "x-goog-api-key", "sk-legacy"); rec.Code != http.StatusOK || seen == nil || !seen.Legacy {
		t.Fatalf("expected legacy caller, got %d %+v", rec.Code, seen)
	}
	if rec := call("/v1/chat/completions", "x-api-key", "sk-nx-wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected wrong key rejected, got %d", rec.Code)
	}

	// Raw provider routes honor the provider allowlist.
	if rec := call("/openrouter/v1/chat/completions", "x-api-key", pkmKey); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "permission_error") {
		t.Fatalf("expected 403 for disallowed raw provider, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("/ollama/v1/chat/completions", "x-api-key", pkmKey); rec.Code != http.StatusOK {
		t.Fatalf("expected allowed raw provider, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("/openrouter/v1/chat/completions", "x-api-key", "sk-legacy"); rec.Code != http.StatusOK {
		t.Fatalf("legacy key must stay unrestricted, got %d", rec.Code)
	}
	// Keys with a model allowlist cannot use raw routes at all (model aliases are not checkable there).
	_, modelKey, _ := clientkey.Create(gdb, clientkey.Spec{Name: "agent", AllowedModels: []string{"gpt-4o"}})
	if rec := call("/ollama/v1/chat/completions", "x-api-key", modelKey); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for model-restricted key on raw route, got %d", rec.Code)
	}

	// Disabled keys are rejected with a distinct message.
	row, _ := clientkey.Get(gdb, "pkm-app")
	yes := true
	if _, err := clientkey.Update(gdb, row.ID, clientkey.Patch{Disabled: &yes}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if rec := call("/v1/chat/completions", "Authorization", "Bearer "+pkmKey); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("expected disabled 401, got %d %s", rec.Code, rec.Body.String())
	}
}
