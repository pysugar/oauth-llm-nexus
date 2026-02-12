package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/upstream/vertexkey"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestParseVertexAIStudioModelAction(t *testing.T) {
	tests := []struct {
		path   string
		model  string
		action string
		ok     bool
	}{
		{
			path:   "/v1/publishers/google/models/gemini-2.5-flash-lite:generateContent",
			model:  "gemini-2.5-flash-lite",
			action: "generateContent",
			ok:     true,
		},
		{
			path:   "/v1/publishers/google/models/gemini-3-flash-preview:streamGenerateContent",
			model:  "gemini-3-flash-preview",
			action: "streamGenerateContent",
			ok:     true,
		},
		{
			path:   "/v1/publishers/google/models/google/gemini-3-pro-preview:countTokens",
			model:  "google/gemini-3-pro-preview",
			action: "countTokens",
			ok:     true,
		},
		{
			path: "/v1/publishers/google/models/gemini-3-flash-preview:files",
			ok:   false,
		},
	}

	for _, tt := range tests {
		model, action, ok := parseVertexAIStudioModelAction(tt.path)
		if ok != tt.ok {
			t.Fatalf("parseVertexAIStudioModelAction(%q) ok = %v, want %v", tt.path, ok, tt.ok)
		}
		if model != tt.model {
			t.Fatalf("parseVertexAIStudioModelAction(%q) model = %q, want %q", tt.path, model, tt.model)
		}
		if action != tt.action {
			t.Fatalf("parseVertexAIStudioModelAction(%q) action = %q, want %q", tt.path, action, tt.action)
		}
	}
}

func TestVertexAIStudioProxyHandler_Disabled(t *testing.T) {
	oldProvider := VertexAIStudioProvider
	VertexAIStudioProvider = nil
	defer func() { VertexAIStudioProvider = oldProvider }()

	req := httptest.NewRequest(http.MethodPost, "/v1/publishers/google/models/gemini-2.5-flash-lite:generateContent", nil)
	w := httptest.NewRecorder()
	VertexAIStudioProxyHandler().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("Expected status 503, got %d", w.Code)
	}
}

func TestVertexAIStudioProxyHandler_Passthrough(t *testing.T) {
	var gotPath string
	var gotQueryKey string
	var gotBody []byte

	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			gotQueryKey = r.URL.Query().Get("key")
			gotBody, _ = io.ReadAll(r.Body)
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"countTokensResult":{"totalTokens":42}}`)),
			}, nil
		}),
	}

	oldProvider := VertexAIStudioProvider
	VertexAIStudioProvider = vertexkey.NewProviderWithClient("server-key", "https://aiplatform.googleapis.com", time.Minute, client)
	defer func() { VertexAIStudioProvider = oldProvider }()

	reqBody := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/publishers/google/models/gemini-2.5-flash-lite:countTokens?key=client-key",
		strings.NewReader(reqBody),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("X-Goog-Api-Key", "client-key")

	w := httptest.NewRecorder()
	VertexAIStudioProxyHandler().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("Expected status 202, got %d", w.Code)
	}
	if w.Body.String() != `{"countTokensResult":{"totalTokens":42}}` {
		t.Fatalf("Unexpected response body: %s", w.Body.String())
	}
	if gotPath != "/v1/publishers/google/models/gemini-2.5-flash-lite:countTokens" {
		t.Fatalf("Unexpected upstream path: %s", gotPath)
	}
	if gotQueryKey != "server-key" {
		t.Fatalf("Expected upstream key=server-key, got %q", gotQueryKey)
	}
	if string(gotBody) != reqBody {
		t.Fatalf("Unexpected forwarded body: %s", string(gotBody))
	}
}

func TestVertexAIStudioProxyHandler_StreamProxyMode(t *testing.T) {
	var gotPath string

	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
				},
				Body: io.NopCloser(bytes.NewBufferString(
					"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n" +
						"data: [DONE]\n\n",
				)),
			}, nil
		}),
	}

	oldProvider := VertexAIStudioProvider
	VertexAIStudioProvider = vertexkey.NewProviderWithClient("server-key", "https://aiplatform.googleapis.com", time.Minute, client)
	defer func() { VertexAIStudioProvider = oldProvider }()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/publishers/google/models/gemini-2.5-flash-lite:streamGenerateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	VertexAIStudioProxyHandler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("expected streamed DONE marker, got %s", w.Body.String())
	}
	if gotPath != "/v1/publishers/google/models/gemini-2.5-flash-lite:streamGenerateContent" {
		t.Fatalf("unexpected upstream stream path: %s", gotPath)
	}
}
