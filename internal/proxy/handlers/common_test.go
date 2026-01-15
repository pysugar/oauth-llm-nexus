package handlers

import (
	"net/http/httptest"
	"testing"
)

func TestGetOrGenerateRequestID_WithHeader(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("X-Request-ID", "client-provided-id")

	result := GetOrGenerateRequestID(req)
	if result != "client-provided-id" {
		t.Errorf("Expected 'client-provided-id', got '%s'", result)
	}
}

func TestGetOrGenerateRequestID_GenerateNew(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)

	result := GetOrGenerateRequestID(req)
	if len(result) < 10 {
		t.Errorf("Expected generated UUID, got '%s'", result)
	}
	if result[:6] != "agent-" {
		t.Errorf("Expected prefix 'agent-', got '%s'", result[:6])
	}
}

func TestSetSSEHeaders(t *testing.T) {
	w := httptest.NewRecorder()

	SetSSEHeaders(w)

	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("Expected Content-Type 'text/event-stream', got '%s'", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("Expected Cache-Control 'no-cache', got '%s'", w.Header().Get("Cache-Control"))
	}
	if w.Header().Get("Connection") != "keep-alive" {
		t.Errorf("Expected Connection 'keep-alive', got '%s'", w.Header().Get("Connection"))
	}
}
