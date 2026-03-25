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

	provider := NewProviderWithClient([]string{"server-key"}, "https://generativelanguage.googleapis.com", time.Minute, client)
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

	provider := NewProviderWithClient([]string{"server-key"}, "https://generativelanguage.googleapis.com", time.Minute, client)

	if _, err := provider.Forward(context.Background(), http.MethodGet, "/v1beta/models", nil, http.Header{}, nil); err != nil {
		t.Fatalf("models list should be supported: %v", err)
	}
	if _, err := provider.Forward(context.Background(), http.MethodGet, "/v1beta/models/gemini-2.5-flash", nil, http.Header{}, nil); err != nil {
		t.Fatalf("models get should be supported: %v", err)
	}
}

func TestForward_RejectsUnsupportedEndpoints(t *testing.T) {
	provider := NewProvider([]string{"server-key"}, "https://generativelanguage.googleapis.com", time.Minute)

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

func TestNewProviderFromEnv_MultiKeys(t *testing.T) {
	oldGetenv := getenv
	defer func() { getenv = oldGetenv }()

	getenv = func(key string) string {
		switch key {
		case "NEXUS_GEMINI_API_KEYS":
			return "key-alpha,key-bravo,key-charlie"
		default:
			return ""
		}
	}
	provider := NewProviderFromEnv()
	if provider == nil || !provider.IsEnabled() {
		t.Fatal("expected provider enabled with NEXUS_GEMINI_API_KEYS")
	}
	if provider.KeyCount() != 3 {
		t.Fatalf("expected 3 keys, got %d", provider.KeyCount())
	}
}

func TestNewProviderFromEnv_MultiKeys_SkipsEmpty(t *testing.T) {
	oldGetenv := getenv
	defer func() { getenv = oldGetenv }()

	getenv = func(key string) string {
		switch key {
		case "NEXUS_GEMINI_API_KEYS":
			return "key-alpha,,  ,key-bravo"
		default:
			return ""
		}
	}
	provider := NewProviderFromEnv()
	if provider == nil || !provider.IsEnabled() {
		t.Fatal("expected provider enabled")
	}
	if provider.KeyCount() != 2 {
		t.Fatalf("expected 2 keys after filtering, got %d", provider.KeyCount())
	}
}

func TestNewProviderFromEnv_MultiKeysPriority(t *testing.T) {
	oldGetenv := getenv
	defer func() { getenv = oldGetenv }()

	getenv = func(key string) string {
		switch key {
		case "NEXUS_GEMINI_API_KEYS":
			return "multi-key-1,multi-key-2"
		case "NEXUS_GEMINI_API_KEY":
			return "single-key"
		case "GEMINI_API_KEY":
			return "fallback-key"
		default:
			return ""
		}
	}
	provider := NewProviderFromEnv()
	if provider == nil || !provider.IsEnabled() {
		t.Fatal("expected provider enabled")
	}
	// NEXUS_GEMINI_API_KEYS should take priority
	if provider.KeyCount() != 2 {
		t.Fatalf("expected 2 keys from NEXUS_GEMINI_API_KEYS, got %d", provider.KeyCount())
	}
}

func TestForward_429TriggeredKeySwitching(t *testing.T) {
	var usedKeys []string

	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			key := r.URL.Query().Get("key")
			usedKeys = append(usedKeys, key)

			// First key always returns 429, second key succeeds
			if key == "key-alpha" {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"rate limited"}}`)),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
			}, nil
		}),
	}

	provider := NewProviderWithClient(
		[]string{"key-alpha", "key-bravo", "key-charlie"},
		"https://generativelanguage.googleapis.com",
		time.Minute,
		client,
	)

	// Request 1: starts with key-alpha (429), switches to key-bravo (200)
	resp, err := provider.Forward(
		context.Background(),
		http.MethodPost,
		"/v1beta/models/gemini-2.5-flash:generateContent",
		nil, http.Header{}, []byte(`{}`),
	)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after key switch, got %d", resp.StatusCode)
	}
	if len(usedKeys) != 2 || usedKeys[0] != "key-alpha" || usedKeys[1] != "key-bravo" {
		t.Fatalf("expected [key-alpha, key-bravo], got %v", usedKeys)
	}

	// Request 2: should start with key-bravo (sticky pointer)
	usedKeys = nil
	resp, err = provider.Forward(
		context.Background(),
		http.MethodPost,
		"/v1beta/models/gemini-2.5-flash:generateContent",
		nil, http.Header{}, []byte(`{}`),
	)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	resp.Body.Close()
	if len(usedKeys) != 1 || usedKeys[0] != "key-bravo" {
		t.Fatalf("expected sticky on key-bravo, got %v", usedKeys)
	}
}

func TestForward_429AllKeysExhausted(t *testing.T) {
	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"rate limited"}}`)),
			}, nil
		}),
	}

	provider := NewProviderWithClient(
		[]string{"key-1", "key-2", "key-3"},
		"https://generativelanguage.googleapis.com",
		time.Minute,
		client,
	)

	resp, err := provider.Forward(
		context.Background(),
		http.MethodPost,
		"/v1beta/models/gemini-2.5-flash:generateContent",
		nil, http.Header{}, []byte(`{}`),
	)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	resp.Body.Close()
	// All keys exhausted, should return 429
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when all keys exhausted, got %d", resp.StatusCode)
	}
}

func TestMaskKey(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"AIzaSyFAKE-TestKey1ForUnitTestOnly_xyzABC", "***...xyzABC"},
		{"short", "short"},
		{"abcdef", "abcdef"},
		{"abcdefg", "***...bcdefg"},
	}
	for _, tc := range cases {
		got := MaskKey(tc.input)
		if got != tc.expected {
			t.Errorf("MaskKey(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestMaskedKeys(t *testing.T) {
	provider := NewProvider(
		[]string{"AIzaSyFAKE-TestKey1ForUnitTestOnly_xyzABC", "AIzaSyFAKE-TestKey2ForUnitTestOnly_uvwDEF"},
		"", 0,
	)
	masked := provider.MaskedKeys()
	if len(masked) != 2 {
		t.Fatalf("expected 2 masked keys, got %d", len(masked))
	}
	if masked[0] != "***...xyzABC" {
		t.Errorf("masked[0] = %q, want %q", masked[0], "***...xyzABC")
	}
	if masked[1] != "***...uvwDEF" {
		t.Errorf("masked[1] = %q, want %q", masked[1], "***...uvwDEF")
	}
}
