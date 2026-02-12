package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSupportStatusHandler_IncludesOpenAICompatStatus(t *testing.T) {
	setupOpenAICompatProbeCatalogForTest(t, true)

	oldVertex := VertexAIStudioProvider
	oldGemini := GeminiAIStudioProvider
	oldCodex := CodexProvider
	VertexAIStudioProvider = nil
	GeminiAIStudioProvider = nil
	CodexProvider = nil
	t.Cleanup(func() {
		VertexAIStudioProvider = oldVertex
		GeminiAIStudioProvider = oldGemini
		CodexProvider = oldCodex
	})

	req := httptest.NewRequest(http.MethodGet, "/api/support-status", nil)
	rec := httptest.NewRecorder()
	SupportStatusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, rec.Body.String())
	}

	assertBoolField(t, payload, "openrouter_proxy_enabled", true)
	assertBoolField(t, payload, "nvidia_proxy_enabled", true)
	assertBoolField(t, payload, "codex_enabled", false)
}

func assertBoolField(t *testing.T, payload map[string]interface{}, key string, expected bool) {
	t.Helper()
	raw, ok := payload[key]
	if !ok {
		t.Fatalf("missing key %q in payload %#v", key, payload)
	}
	got, ok := raw.(bool)
	if !ok {
		t.Fatalf("key %q is not bool: %#v", key, raw)
	}
	if got != expected {
		t.Fatalf("key %q expected %v, got %v", key, expected, got)
	}
}
