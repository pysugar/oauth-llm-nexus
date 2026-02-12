package openaicompat

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestForwardChatCompletions_InjectsServerAuthAndStaticHeaders(t *testing.T) {
	var capturedAuth string
	var capturedClientAuth string
	var capturedReferer string
	var capturedBody string

	client := &http.Client{
		Timeout: time.Second,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			capturedAuth = r.Header.Get("Authorization")
			capturedClientAuth = r.Header.Get("X-Api-Key")
			capturedReferer = r.Header.Get("HTTP-Referer")
			body, _ := io.ReadAll(r.Body)
			capturedBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"ok"}`)),
			}, nil
		}),
	}

	provider := NewProviderWithClient(
		"openrouter",
		"server-key",
		"https://openrouter.ai/api/v1",
		10*time.Second,
		map[string]string{"HTTP-Referer": "https://example.local"},
		client,
	)

	resp, err := provider.ForwardChatCompletions(
		context.Background(),
		http.MethodPost,
		nil,
		http.Header{
			"Authorization": []string{"Bearer client-key"},
			"X-Api-Key":     []string{"client-key"},
			"Content-Type":  []string{"application/json"},
		},
		[]byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
	)
	if err != nil {
		t.Fatalf("forward error: %v", err)
	}
	defer resp.Body.Close()

	if capturedAuth != "Bearer server-key" {
		t.Fatalf("expected server auth header, got %q", capturedAuth)
	}
	if capturedClientAuth != "" {
		t.Fatalf("expected client x-api-key stripped, got %q", capturedClientAuth)
	}
	if capturedReferer != "https://example.local" {
		t.Fatalf("expected static header injected, got %q", capturedReferer)
	}
	if !strings.Contains(capturedBody, "openai/gpt-4o-mini") {
		t.Fatalf("unexpected forwarded body: %s", capturedBody)
	}
}

func TestForwardChatCompletions_RejectsNonPost(t *testing.T) {
	provider := NewProvider("openrouter", "server-key", "https://example.com/v1", 10*time.Second, nil)
	if _, err := provider.ForwardChatCompletions(context.Background(), http.MethodGet, nil, nil, nil); err == nil {
		t.Fatal("expected non-post request to be rejected")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
