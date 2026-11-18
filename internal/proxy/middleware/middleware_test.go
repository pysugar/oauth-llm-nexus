package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
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
