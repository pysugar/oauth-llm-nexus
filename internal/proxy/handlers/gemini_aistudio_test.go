package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/upstream/geminikey"
)

func TestParseGeminiAIStudioPath(t *testing.T) {
	tests := []struct {
		method string
		path   string
		model  string
		action string
		ok     bool
	}{
		{
			method: http.MethodGet,
			path:   "/v1beta/models",
			model:  "",
			action: "models.list",
			ok:     true,
		},
		{
			method: http.MethodGet,
			path:   "/v1beta/models/gemini-2.5-flash",
			model:  "gemini-2.5-flash",
			action: "models.get",
			ok:     true,
		},
		{
			method: http.MethodPost,
			path:   "/v1beta/models/gemini-2.5-flash:generateContent",
			model:  "gemini-2.5-flash",
			action: "generateContent",
			ok:     true,
		},
		{
			method: http.MethodPost,
			path:   "/v1beta/models/gemini-2.5-flash:streamGenerateContent",
			model:  "gemini-2.5-flash",
			action: "streamGenerateContent",
			ok:     true,
		},
		{
			method: http.MethodPost,
			path:   "/v1beta/models/gemini-2.5-flash:embedContent",
			model:  "gemini-2.5-flash",
			action: "embedContent",
			ok:     true,
		},
		{
			method: http.MethodPost,
			path:   "/v1beta/models/gemini-2.5-flash:batchEmbedContents",
			model:  "gemini-2.5-flash",
			action: "batchEmbedContents",
			ok:     true,
		},
		{
			method: http.MethodPost,
			path:   "/v1beta/models/gemini-2.5-flash:files",
			ok:     false,
		},
	}

	for _, tt := range tests {
		model, action, ok := parseGeminiAIStudioPath(tt.method, tt.path)
		if ok != tt.ok {
			t.Fatalf("parseGeminiAIStudioPath(%q, %q) ok=%v, want=%v", tt.method, tt.path, ok, tt.ok)
		}
		if model != tt.model {
			t.Fatalf("parseGeminiAIStudioPath(%q, %q) model=%q, want=%q", tt.method, tt.path, model, tt.model)
		}
		if action != tt.action {
			t.Fatalf("parseGeminiAIStudioPath(%q, %q) action=%q, want=%q", tt.method, tt.path, action, tt.action)
		}
	}
}

func TestGeminiAIStudioProxyHandler_Disabled(t *testing.T) {
	oldProvider := GeminiAIStudioProvider
	GeminiAIStudioProvider = nil
	defer func() { GeminiAIStudioProvider = oldProvider }()

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", nil)
	w := httptest.NewRecorder()
	GeminiAIStudioProxyHandler().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

func TestGeminiAIStudioProxyHandler_PassthroughGenerate(t *testing.T) {
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
				Body: io.NopCloser(bytes.NewBufferString(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`)),
			}, nil
		}),
	}

	oldProvider := GeminiAIStudioProvider
	GeminiAIStudioProvider = geminikey.NewProviderWithClient("server-key", "https://generativelanguage.googleapis.com", time.Minute, client)
	defer func() { GeminiAIStudioProvider = oldProvider }()

	reqBody := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/gemini-2.5-flash:generateContent?key=client-key",
		strings.NewReader(reqBody),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("X-Goog-Api-Key", "client-key")

	w := httptest.NewRecorder()
	GeminiAIStudioProxyHandler().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", w.Code)
	}
	if gotPath != "/v1beta/models/gemini-2.5-flash:generateContent" {
		t.Fatalf("unexpected upstream path: %s", gotPath)
	}
	if gotQueryKey != "server-key" {
		t.Fatalf("expected upstream key=server-key, got %q", gotQueryKey)
	}
	if string(gotBody) != reqBody {
		t.Fatalf("unexpected forwarded body: %s", string(gotBody))
	}
}

func TestGeminiAIStudioProxyHandler_ModelsList(t *testing.T) {
	var gotPath string
	var gotMethod string
	var gotQueryKey string

	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			gotMethod = r.Method
			gotQueryKey = r.URL.Query().Get("key")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"models":[]}`)),
			}, nil
		}),
	}

	oldProvider := GeminiAIStudioProvider
	GeminiAIStudioProvider = geminikey.NewProviderWithClient("server-key", "https://generativelanguage.googleapis.com", time.Minute, client)
	defer func() { GeminiAIStudioProvider = oldProvider }()

	req := httptest.NewRequest(http.MethodGet, "/v1beta/models?key=client-key", nil)
	w := httptest.NewRecorder()
	GeminiAIStudioProxyHandler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("expected GET method, got %s", gotMethod)
	}
	if gotPath != "/v1beta/models" {
		t.Fatalf("unexpected upstream path: %s", gotPath)
	}
	if gotQueryKey != "server-key" {
		t.Fatalf("expected upstream key=server-key, got %q", gotQueryKey)
	}
}

func TestGeminiAIStudioProxyHandler_StreamProxyMode(t *testing.T) {
	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
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

	oldProvider := GeminiAIStudioProvider
	GeminiAIStudioProvider = geminikey.NewProviderWithClient("server-key", "https://generativelanguage.googleapis.com", time.Minute, client)
	defer func() { GeminiAIStudioProvider = oldProvider }()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/gemini-2.5-flash:streamGenerateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	GeminiAIStudioProxyHandler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("expected streamed DONE marker, got %s", w.Body.String())
	}
}
