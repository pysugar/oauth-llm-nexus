package geminikey

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestForward_InjectsServerKeyAndSanitizesHeaders(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	var gotAuth string
	var gotGoogKey string
	var gotCustom string
	var gotBody []byte

	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			gotQuery = r.URL.Query()
			gotAuth = r.Header.Get("Authorization")
			gotGoogKey = r.Header.Get("X-Goog-Api-Key")
			gotCustom = r.Header.Get("X-Custom")
			gotBody, _ = io.ReadAll(r.Body)

			return &http.Response{
				StatusCode: http.StatusCreated,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
			}, nil
		}),
	}

	provider := NewProviderWithClient("server-key", "https://generativelanguage.googleapis.com", time.Minute, client)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer client-token")
	headers.Set("X-Goog-Api-Key", "client-key")
	headers.Set("X-Custom", "hello")
	headers.Set("Content-Type", "application/json")

	query := url.Values{
		"key": {"client-key"},
		"alt": {"sse"},
	}

	resp, err := provider.Forward(
		context.Background(),
		http.MethodPost,
		"/v1beta/models/gemini-2.5-flash:streamGenerateContent",
		query,
		headers,
		[]byte(`{"input":"test"}`),
	)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", resp.StatusCode)
	}
	if gotPath != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
		t.Fatalf("unexpected upstream path: %s", gotPath)
	}
	if gotQuery.Get("key") != "server-key" {
		t.Fatalf("expected key=server-key, got %q", gotQuery.Get("key"))
	}
	if gotQuery.Get("alt") != "sse" {
		t.Fatalf("expected preserved alt=sse, got %q", gotQuery.Get("alt"))
	}
	if gotAuth != "" {
		t.Fatalf("expected Authorization to be stripped, got %q", gotAuth)
	}
	if gotGoogKey != "" {
		t.Fatalf("expected X-Goog-Api-Key to be stripped, got %q", gotGoogKey)
	}
	if gotCustom != "hello" {
		t.Fatalf("expected X-Custom=hello, got %q", gotCustom)
	}
	if string(gotBody) != `{"input":"test"}` {
		t.Fatalf("unexpected body: %s", string(gotBody))
	}
}

func TestForward_SupportsModelsListAndGet(t *testing.T) {
	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"models":[]}`)),
			}, nil
		}),
	}

	provider := NewProviderWithClient("server-key", "https://generativelanguage.googleapis.com", time.Minute, client)

	if _, err := provider.Forward(context.Background(), http.MethodGet, "/v1beta/models", nil, http.Header{}, nil); err != nil {
		t.Fatalf("models list should be supported: %v", err)
	}
	if _, err := provider.Forward(context.Background(), http.MethodGet, "/v1beta/models/gemini-2.5-flash", nil, http.Header{}, nil); err != nil {
		t.Fatalf("models get should be supported: %v", err)
	}
}

func TestForward_RejectsUnsupportedEndpoints(t *testing.T) {
	provider := NewProvider("server-key", "https://generativelanguage.googleapis.com", time.Minute)

	cases := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/v1beta/models"},
		{method: http.MethodPost, path: "/v1beta/models/gemini-2.5-flash:files"},
		{method: http.MethodDelete, path: "/v1beta/models/gemini-2.5-flash"},
		{method: http.MethodGet, path: "/v1/other"},
	}

	for _, tc := range cases {
		_, err := provider.Forward(context.Background(), tc.method, tc.path, nil, http.Header{}, nil)
		if err == nil {
			t.Fatalf("expected error for method=%s path=%s", tc.method, tc.path)
		}
	}
}

func TestNewProviderFromEnv_DisabledWhenNoKey(t *testing.T) {
	oldGetenv := getenv
	defer func() { getenv = oldGetenv }()

	getenv = func(string) string { return "" }
	provider := NewProviderFromEnv()
	if provider != nil {
		t.Fatal("expected nil provider when no key is configured")
	}
}

func TestNewProviderFromEnv_FallbackToGeminiAPIKey(t *testing.T) {
	oldGetenv := getenv
	defer func() { getenv = oldGetenv }()

	getenv = func(key string) string {
		switch key {
		case "NEXUS_GEMINI_API_KEY":
			return ""
		case "GEMINI_API_KEY":
			return "gemini-key"
		default:
			return ""
		}
	}
	provider := NewProviderFromEnv()
	if provider == nil || !provider.IsEnabled() {
		t.Fatal("expected provider enabled with GEMINI_API_KEY fallback")
	}
}
