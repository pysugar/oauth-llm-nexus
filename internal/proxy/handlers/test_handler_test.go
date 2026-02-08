package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
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
