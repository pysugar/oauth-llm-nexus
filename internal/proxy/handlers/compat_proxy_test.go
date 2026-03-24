package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
)

// fakeGenericCompatForwarder implements genericCompatForwarder for tests.
type fakeGenericCompatForwarder struct {
	capturedSubpath string
	resp            *http.Response
	err             error
}

func (f *fakeGenericCompatForwarder) ForwardRequest(
	_ context.Context,
	_ string,
	subpath string,
	_ url.Values,
	_ http.Header,
	_ []byte,
) (*http.Response, error) {
	f.capturedSubpath = subpath
	return f.resp, f.err
}

func setupGenericCompatCatalogForTest(t *testing.T) {
	t.Helper()
	catalog.ResetForTest()
	t.Cleanup(catalog.ResetForTest)

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "openai_compat_providers.yaml")
	cfg := `providers:
  - id: openrouter
    enabled: true
    base_url: https://openrouter.ai/api/v1
    auth_mode: bearer
    model_scope: all_models
    capabilities: [openai.chat, anthropic.messages]
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("NEXUS_OPENAI_COMPAT_PROVIDERS_FILE", cfgPath)
	t.Setenv("NEXUS_OPENROUTER_API_KEY", "or-test-key")
	if err := catalog.InitFromEnvAndConfig(); err != nil {
		t.Fatalf("init catalog: %v", err)
	}
}

func makeGenericRequest(method, path, provider string, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", provider)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestGenericCompatProxy_ChatCompletions(t *testing.T) {
	setupGenericCompatCatalogForTest(t)

	fake := &fakeGenericCompatForwarder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)),
		},
	}
	origFactory := newGenericCompatForwarder
	newGenericCompatForwarder = func(_ catalog.ProviderInfo, _ string, _ time.Duration) genericCompatForwarder {
		return fake
	}
	t.Cleanup(func() { newGenericCompatForwarder = origFactory })

	req := makeGenericRequest(http.MethodPost, "/openrouter/v1/chat/completions", "openrouter",
		`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	GenericCompatProxyHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.capturedSubpath != "/v1/chat/completions" {
		t.Fatalf("expected subpath /v1/chat/completions, got %q", fake.capturedSubpath)
	}
}

func TestGenericCompatProxy_AnthropicMessages(t *testing.T) {
	setupGenericCompatCatalogForTest(t)

	fake := &fakeGenericCompatForwarder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}]}`)),
		},
	}
	origFactory := newGenericCompatForwarder
	newGenericCompatForwarder = func(_ catalog.ProviderInfo, _ string, _ time.Duration) genericCompatForwarder {
		return fake
	}
	t.Cleanup(func() { newGenericCompatForwarder = origFactory })

	req := makeGenericRequest(http.MethodPost, "/openrouter/v1/messages", "openrouter",
		`{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	GenericCompatProxyHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.capturedSubpath != "/v1/messages" {
		t.Fatalf("expected subpath /v1/messages, got %q", fake.capturedSubpath)
	}
}

func TestGenericCompatProxy_UnknownProvider(t *testing.T) {
	catalog.ResetForTest()
	t.Cleanup(catalog.ResetForTest)

	req := makeGenericRequest(http.MethodPost, "/unknown/v1/chat/completions", "unknown",
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	GenericCompatProxyHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGenericCompatProxy_ProviderRuntimeDisabled(t *testing.T) {
	catalog.ResetForTest()
	t.Cleanup(catalog.ResetForTest)

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "openai_compat_providers.yaml")
	// openrouter enabled but no API key → RuntimeEnabled=false
	cfg := `providers:
  - id: openrouter
    enabled: true
    base_url: https://openrouter.ai/api/v1
    auth_mode: bearer
    model_scope: all_models
    capabilities: [openai.chat]
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("NEXUS_OPENAI_COMPAT_PROVIDERS_FILE", cfgPath)
	// Intentionally NOT setting NEXUS_OPENROUTER_API_KEY
	if err := catalog.InitFromEnvAndConfig(); err != nil {
		t.Fatalf("init catalog: %v", err)
	}

	req := makeGenericRequest(http.MethodPost, "/openrouter/v1/chat/completions", "openrouter",
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	GenericCompatProxyHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
}
