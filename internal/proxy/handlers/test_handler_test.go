package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
	"gorm.io/gorm"
)

func TestTestHandler_MissingEndpoint(t *testing.T) {
	handler := TestHandler(nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing endpoint, got %d", rec.Code)
	}
}

func TestTestHandler_DispatchAndShape(t *testing.T) {
	oldRunner := runEndpointTest
	defer func() { runEndpointTest = oldRunner }()

	runEndpointTest = func(endpoint string, _ *gorm.DB, _ *token.Manager, _ *upstream.Client) (EndpointTestResult, error) {
		return EndpointTestResult{
			Endpoint:    endpoint,
			Path:        "/v1/chat/completions",
			Model:       "gemini-3-flash",
			StatusCode:  http.StatusOK,
			DurationMS:  12,
			ContentType: "application/json",
			Success:     true,
			Summary:     "HTTP 200",
			Snippet:     `{"ok":true}`,
		}, nil
	}

	handler := TestHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/test?endpoint=openai_chat", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got EndpointTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("expected json response, got err=%v body=%s", err, rec.Body.String())
	}
	if got.Endpoint != "openai_chat" {
		t.Fatalf("expected endpoint=openai_chat, got %q", got.Endpoint)
	}
	if got.Path == "" || got.Model == "" || got.ContentType == "" {
		t.Fatalf("expected structured fields to be present, got %#v", got)
	}
}

func TestTestHandler_InvalidEndpoint(t *testing.T) {
	oldRunner := runEndpointTest
	defer func() { runEndpointTest = oldRunner }()

	runEndpointTest = func(_ string, _ *gorm.DB, _ *token.Manager, _ *upstream.Client) (EndpointTestResult, error) {
		return EndpointTestResult{}, fmt.Errorf("Unsupported endpoint: bad")
	}

	handler := TestHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/test?endpoint=bad", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid endpoint, got %d", rec.Code)
	}
}

func TestExecuteEndpointTest_OpenAICompatProbeEndpoints(t *testing.T) {
	setupOpenAICompatProbeCatalogForTest(t, false)

	cases := []struct {
		endpoint         string
		expectedPath     string
		expectedModel    string
		expectedProvider string
		expectedReason   string
	}{
		{
			endpoint:         testEndpointOpenRouterChat,
			expectedPath:     "/openrouter/v1/chat/completions",
			expectedModel:    "openrouter/free",
			expectedProvider: "openrouter",
			expectedReason:   "openrouter_proxy_disabled",
		},
		{
			endpoint:         testEndpointNVIDIAChat,
			expectedPath:     "/nvidia/v1/chat/completions",
			expectedModel:    "z-ai/glm4.7",
			expectedProvider: "nvidia",
			expectedReason:   "nvidia_proxy_disabled",
		},
	}

	for _, tc := range cases {
		got, err := executeEndpointTest(tc.endpoint, nil, nil, nil)
		if err != nil {
			t.Fatalf("endpoint %s returned error: %v", tc.endpoint, err)
		}
		if !got.Skipped {
			t.Fatalf("endpoint %s expected skipped=true, got %#v", tc.endpoint, got)
		}
		if got.Path != tc.expectedPath {
			t.Fatalf("endpoint %s expected path %q, got %q", tc.endpoint, tc.expectedPath, got.Path)
		}
		if got.Model != tc.expectedModel {
			t.Fatalf("endpoint %s expected model %q, got %q", tc.endpoint, tc.expectedModel, got.Model)
		}
		if got.Provider != tc.expectedProvider {
			t.Fatalf("endpoint %s expected provider %q, got %q", tc.endpoint, tc.expectedProvider, got.Provider)
		}
		if got.Reason != tc.expectedReason {
			t.Fatalf("endpoint %s expected reason %q, got %q", tc.endpoint, tc.expectedReason, got.Reason)
		}
	}
}

func setupOpenAICompatProbeCatalogForTest(t *testing.T, withKeys bool) {
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
  - id: nvidia
    enabled: true
    base_url: https://integrate.api.nvidia.com/v1
    auth_mode: bearer
    model_scope: unknown_prefix_only
    capabilities: [openai.chat]
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("NEXUS_OPENAI_COMPAT_PROVIDERS_FILE", cfgPath)
	if withKeys {
		t.Setenv("NEXUS_OPENROUTER_API_KEY", "or-test-key")
		t.Setenv("NEXUS_NVIDIA_API_KEY", "nv-test-key")
	} else {
		t.Setenv("NEXUS_OPENROUTER_API_KEY", "")
		t.Setenv("NEXUS_NVIDIA_API_KEY", "")
	}

	if err := catalog.InitFromEnvAndConfig(); err != nil {
		t.Fatalf("init catalog: %v", err)
	}
}
