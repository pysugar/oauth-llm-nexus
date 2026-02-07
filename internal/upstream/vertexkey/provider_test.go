package vertexkey

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

	provider := NewProviderWithClient("server-key", "https://aiplatform.googleapis.com", time.Minute, client)
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
		"gemini-3-flash-preview",
		"streamGenerateContent",
		query,
		headers,
		[]byte(`{"input":"test"}`),
	)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d", resp.StatusCode)
	}

	if gotPath != "/v1/publishers/google/models/gemini-3-flash-preview:streamGenerateContent" {
		t.Fatalf("Unexpected upstream path: %s", gotPath)
	}

	if gotQuery.Get("key") != "server-key" {
		t.Fatalf("Expected injected key=server-key, got %q", gotQuery.Get("key"))
	}
	if gotQuery.Get("alt") != "sse" {
		t.Fatalf("Expected preserved alt=sse, got %q", gotQuery.Get("alt"))
	}
	if gotAuth != "" {
		t.Fatalf("Expected Authorization to be stripped, got %q", gotAuth)
	}
	if gotGoogKey != "" {
		t.Fatalf("Expected X-Goog-Api-Key to be stripped, got %q", gotGoogKey)
	}
	if gotCustom != "hello" {
		t.Fatalf("Expected X-Custom=hello, got %q", gotCustom)
	}
	if string(gotBody) != `{"input":"test"}` {
		t.Fatalf("Unexpected body: %s", string(gotBody))
	}
}

func TestForward_RejectsUnsupportedAction(t *testing.T) {
	provider := NewProvider("server-key", "https://aiplatform.googleapis.com", time.Minute)
	_, err := provider.Forward(
		context.Background(),
		http.MethodPost,
		"gemini-3-flash-preview",
		"files.upload",
		nil,
		http.Header{},
		nil,
	)
	if err == nil {
		t.Fatal("Expected error for unsupported action, got nil")
	}
}

func TestForward_NormalizeGoogleModelPrefix(t *testing.T) {
	var gotPath string
	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
				Header:     http.Header{},
			}, nil
		}),
	}

	provider := NewProviderWithClient("server-key", "https://aiplatform.googleapis.com", time.Minute, client)
	_, err := provider.Forward(
		context.Background(),
		http.MethodPost,
		"google/gemini-3-pro-preview",
		"generateContent",
		nil,
		http.Header{},
		nil,
	)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}

	want := "/v1/publishers/google/models/gemini-3-pro-preview:generateContent"
	if gotPath != want {
		t.Fatalf("Unexpected path: got %s, want %s", gotPath, want)
	}
}

func TestNewProviderFromEnv_DisabledWhenNoKey(t *testing.T) {
	oldGetenv := getenv
	defer func() { getenv = oldGetenv }()

	getenv = func(key string) string {
		return ""
	}

	provider := NewProviderFromEnv()
	if provider != nil {
		t.Fatal("Expected nil provider when NEXUS_VERTEX_API_KEY is empty")
	}
}
