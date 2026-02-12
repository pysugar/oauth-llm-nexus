package handlers

import (
	"context"
	"encoding/json"
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
	dbpkg "github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
)

func setupOpenAICompatCatalogForTest(t *testing.T) {
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
    capabilities: [openai.chat]
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

type fakeOpenAICompatForwarder struct {
	forward func(method string, query url.Values, headers http.Header, body []byte) (*http.Response, error)
}

func (f *fakeOpenAICompatForwarder) ForwardChatCompletions(
	_ context.Context,
	method string,
	incomingQuery url.Values,
	incomingHeaders http.Header,
	body []byte,
) (*http.Response, error) {
	return f.forward(method, incomingQuery, incomingHeaders, body)
}

func TestOpenAICompatChatProxyHandler_ExplicitProviderPath(t *testing.T) {
	setupOpenAICompatCatalogForTest(t)

	var receivedModel string
	originalFactory := newOpenAICompatForwarder
	newOpenAICompatForwarder = func(_ catalog.ProviderInfo, _ string, _ time.Duration) openAICompatForwarder {
		return &fakeOpenAICompatForwarder{forward: func(_ string, _ url.Values, _ http.Header, body []byte) (*http.Response, error) {
			var payload map[string]interface{}
			_ = json.Unmarshal(body, &payload)
			receivedModel, _ = payload["model"].(string)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)),
			}, nil
		}}
	}
	t.Cleanup(func() { newOpenAICompatForwarder = originalFactory })

	reqBody := `{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/openrouter/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "openrouter")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	OpenAICompatChatProxyHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedModel != "openai/gpt-4o-mini" {
		t.Fatalf("expected model passthrough, got %q", receivedModel)
	}
}

func TestOpenAICompatChatProxyHandler_UnknownProvider(t *testing.T) {
	catalog.ResetForTest()
	t.Cleanup(catalog.ResetForTest)

	reqBody := `{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/unknown/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "unknown")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	OpenAICompatChatProxyHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOpenAIChatHandler_RoutesToOpenAICompatProvider(t *testing.T) {
	setupOpenAICompatCatalogForTest(t)

	var receivedModel string
	originalFactory := newOpenAICompatForwarder
	newOpenAICompatForwarder = func(_ catalog.ProviderInfo, _ string, _ time.Duration) openAICompatForwarder {
		return &fakeOpenAICompatForwarder{forward: func(_ string, _ url.Values, _ http.Header, body []byte) (*http.Response, error) {
			var payload map[string]interface{}
			_ = json.Unmarshal(body, &payload)
			receivedModel, _ = payload["model"].(string)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)),
			}, nil
		}}
	}
	t.Cleanup(func() { newOpenAICompatForwarder = originalFactory })

	database := newTestDB(t)
	if err := dbpkg.CreateModelRoute(database, &models.ModelRoute{
		ClientModel:    "gpt-4o",
		TargetProvider: "openrouter",
		TargetModel:    "openai/gpt-4o-mini",
		IsActive:       true,
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	OpenAIChatHandler(nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedModel != "openai/gpt-4o-mini" {
		t.Fatalf("expected mapped model openai/gpt-4o-mini, got %q", receivedModel)
	}
}

func TestOpenAIResponsesHandler_RejectsOpenAICompatProvider(t *testing.T) {
	setupOpenAICompatCatalogForTest(t)

	database := newTestDB(t)
	if err := dbpkg.CreateModelRoute(database, &models.ModelRoute{
		ClientModel:    "gpt-4o",
		TargetProvider: "openrouter",
		TargetModel:    "openai/gpt-4o-mini",
		IsActive:       true,
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}

	reqBody := `{"model":"gpt-4o","input":"hello","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	OpenAIResponsesHandler(database, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "does not support /v1/responses") {
		t.Fatalf("expected /v1/responses unsupported message, got %s", rec.Body.String())
	}
}
