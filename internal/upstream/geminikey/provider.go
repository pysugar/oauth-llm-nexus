package geminikey

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/upstream/keyproxy"
)

const (
	defaultBaseURL = "https://generativelanguage.googleapis.com"
	defaultTimeout = 5 * time.Minute
)

var allowedPostActions = map[string]struct{}{
	"generateContent":       {},
	"streamGenerateContent": {},
	"countTokens":           {},
	"embedContent":          {},
	"batchEmbedContents":    {},
}

// Provider transparently proxies Google AI Studio Gemini API requests using
// server-side API key injection. It supports multiple keys with 429-triggered
// sticky switching for quota expansion.
type Provider struct {
	apiKeys    []string
	activeIdx  atomic.Uint64
	baseURL    string
	httpClient *http.Client
}

// NewProvider creates a Provider with explicit configuration.
func NewProvider(apiKeys []string, baseURL string, timeout time.Duration) *Provider {
	return NewProviderWithClient(apiKeys, baseURL, timeout, nil)
}

// NewProviderWithClient creates a Provider with optional custom HTTP client.
func NewProviderWithClient(apiKeys []string, baseURL string, timeout time.Duration, httpClient *http.Client) *Provider {
	trimmedBaseURL := strings.TrimSpace(baseURL)
	if trimmedBaseURL == "" {
		trimmedBaseURL = defaultBaseURL
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	// Filter out empty keys
	var validKeys []string
	for _, k := range apiKeys {
		trimmed := strings.TrimSpace(k)
		if trimmed != "" {
			validKeys = append(validKeys, trimmed)
		}
	}

	return &Provider{
		apiKeys:    validKeys,
		baseURL:    strings.TrimRight(trimmedBaseURL, "/"),
		httpClient: httpClient,
	}
}

// NewProviderFromEnv creates a provider from environment variables.
// Priority: NEXUS_GEMINI_API_KEYS > NEXUS_GEMINI_API_KEY > GEMINI_API_KEY.
func NewProviderFromEnv() *Provider {
	// 1. Try NEXUS_GEMINI_API_KEYS (comma-separated multi-key list)
	multiKeys := strings.TrimSpace(getenv("NEXUS_GEMINI_API_KEYS"))
	if multiKeys != "" {
		keys := strings.Split(multiKeys, ",")
		baseURL := strings.TrimSpace(getenv("NEXUS_GEMINI_BASE_URL"))
		timeout := parseTimeoutFromEnv("NEXUS_GEMINI_PROXY_TIMEOUT")
		p := NewProvider(keys, baseURL, timeout)
		if p.IsEnabled() {
			return p
		}
	}

	// 2. Try NEXUS_GEMINI_API_KEY (single key)
	apiKey := strings.TrimSpace(getenv("NEXUS_GEMINI_API_KEY"))
	if apiKey == "" {
		// 3. Fallback to GEMINI_API_KEY
		apiKey = strings.TrimSpace(getenv("GEMINI_API_KEY"))
	}
	if apiKey == "" {
		return nil
	}

	baseURL := strings.TrimSpace(getenv("NEXUS_GEMINI_BASE_URL"))
	timeout := parseTimeoutFromEnv("NEXUS_GEMINI_PROXY_TIMEOUT")
	return NewProvider([]string{apiKey}, baseURL, timeout)
}

// IsEnabled indicates whether provider has at least one valid API key.
func (p *Provider) IsEnabled() bool {
	return p != nil && len(p.apiKeys) > 0
}

// KeyCount returns the number of configured API keys.
func (p *Provider) KeyCount() int {
	if p == nil {
		return 0
	}
	return len(p.apiKeys)
}

// MaskedKeys returns the configured keys in masked form (***...last6).
func (p *Provider) MaskedKeys() []string {
	if p == nil {
		return nil
	}
	masked := make([]string, len(p.apiKeys))
	for i, k := range p.apiKeys {
		masked[i] = MaskKey(k)
	}
	return masked
}

// MaskKey returns a masked representation of an API key, showing only the last 6 characters.
func MaskKey(key string) string {
	if len(key) <= 6 {
		return key
	}
	return "***..." + key[len(key)-6:]
}

// activeKey returns the currently active API key.
func (p *Provider) activeKey() string {
	idx := p.activeIdx.Load() % uint64(len(p.apiKeys))
	return p.apiKeys[idx]
}

// advanceKey atomically advances the active key pointer to the next key.
func (p *Provider) advanceKey() {
	p.activeIdx.Add(1)
}

// Forward proxies request to AI Studio endpoint with server-side API key.
// On 429 responses, it advances to the next key and retries, up to len(keys) attempts.
func (p *Provider) Forward(
	ctx context.Context,
	method string,
	requestPath string,
	incomingQuery url.Values,
	incomingHeaders http.Header,
	body []byte,
) (*http.Response, error) {
	if !p.IsEnabled() {
		return nil, fmt.Errorf("gemini key provider is not enabled")
	}

	normalizedPath, err := normalizeAndValidatePath(method, requestPath)
	if err != nil {
		return nil, err
	}

	targetURL := p.baseURL + normalizedPath
	parsedTarget, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	maxAttempts := len(p.apiKeys)
	var lastResp *http.Response

	for attempt := 0; attempt < maxAttempts; attempt++ {
		apiKey := p.activeKey()

		req, err := keyproxy.BuildUpstreamRequest(
			ctx,
			method,
			parsedTarget,
			keyproxy.CloneValues(incomingQuery),
			incomingHeaders,
			body,
			apiKey,
		)
		if err != nil {
			return nil, err
		}

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests || attempt == maxAttempts-1 {
			// Either success / non-429 error, or last attempt exhausted
			return resp, nil
		}

		// Got 429: close this response, advance key, retry
		resp.Body.Close()
		lastResp = resp
		p.advanceKey()
	}

	// Should not reach here, but return last response as safety
	return lastResp, nil
}

func normalizeAndValidatePath(method string, requestPath string) (string, error) {
	cleanPath := pathpkg.Clean("/" + strings.TrimSpace(requestPath))

	// Allow native OpenAI-compatible endpoint (POST only)
	if cleanPath == "/v1beta/openai/chat/completions" {
		if method != http.MethodPost {
			return "", fmt.Errorf("unsupported method for OpenAI compat endpoint: %s", method)
		}
		return cleanPath, nil
	}

	if !strings.HasPrefix(cleanPath, "/v1beta/models") {
		return "", fmt.Errorf("unsupported endpoint: %s", requestPath)
	}

	if method == http.MethodGet {
		if cleanPath == "/v1beta/models" {
			return cleanPath, nil
		}
		if strings.HasPrefix(cleanPath, "/v1beta/models/") {
			model := strings.TrimPrefix(cleanPath, "/v1beta/models/")
			if model != "" && !strings.Contains(model, ":") {
				return cleanPath, nil
			}
		}
		return "", fmt.Errorf("unsupported endpoint: %s", requestPath)
	}

	if method != http.MethodPost {
		return "", fmt.Errorf("unsupported method: %s", method)
	}

	if !strings.HasPrefix(cleanPath, "/v1beta/models/") {
		return "", fmt.Errorf("unsupported endpoint: %s", requestPath)
	}

	rest := strings.TrimPrefix(cleanPath, "/v1beta/models/")
	if rest == "" {
		return "", fmt.Errorf("unsupported endpoint: %s", requestPath)
	}

	colon := strings.LastIndex(rest, ":")
	if colon <= 0 || colon == len(rest)-1 {
		return "", fmt.Errorf("unsupported endpoint: %s", requestPath)
	}

	action := rest[colon+1:]
	if _, ok := allowedPostActions[action]; !ok {
		return "", fmt.Errorf("unsupported action: %s", action)
	}

	return cleanPath, nil
}

func parseTimeoutFromEnv(key string) time.Duration {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return defaultTimeout
	}

	dur, err := time.ParseDuration(raw)
	if err != nil {
		return defaultTimeout
	}
	return dur
}

var getenv = func(key string) string {
	return os.Getenv(key)
}

// CopyResponse streams upstream response headers/body to downstream writer.
func CopyResponse(w http.ResponseWriter, resp *http.Response) error {
	return keyproxy.CopyResponse(w, resp)
}
