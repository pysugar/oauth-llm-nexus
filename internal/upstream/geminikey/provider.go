package geminikey

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"strings"
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
// server-side API key injection.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewProvider creates a Provider with explicit configuration.
func NewProvider(apiKey, baseURL string, timeout time.Duration) *Provider {
	return NewProviderWithClient(apiKey, baseURL, timeout, nil)
}

// NewProviderWithClient creates a Provider with optional custom HTTP client.
func NewProviderWithClient(apiKey, baseURL string, timeout time.Duration, httpClient *http.Client) *Provider {
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

	return &Provider{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    strings.TrimRight(trimmedBaseURL, "/"),
		httpClient: httpClient,
	}
}

// NewProviderFromEnv creates a provider from environment variables.
// Priority: NEXUS_GEMINI_API_KEY > GEMINI_API_KEY.
func NewProviderFromEnv() *Provider {
	apiKey := strings.TrimSpace(getenv("NEXUS_GEMINI_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(getenv("GEMINI_API_KEY"))
	}
	if apiKey == "" {
		return nil
	}

	baseURL := strings.TrimSpace(getenv("NEXUS_GEMINI_BASE_URL"))
	timeout := parseTimeoutFromEnv("NEXUS_GEMINI_PROXY_TIMEOUT")
	return NewProvider(apiKey, baseURL, timeout)
}

// IsEnabled indicates whether provider has valid API key.
func (p *Provider) IsEnabled() bool {
	return p != nil && p.apiKey != ""
}

// Forward proxies request to AI Studio endpoint with server-side API key.
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

	req, err := keyproxy.BuildUpstreamRequest(
		ctx,
		method,
		parsedTarget,
		keyproxy.CloneValues(incomingQuery),
		incomingHeaders,
		body,
		p.apiKey,
	)
	if err != nil {
		return nil, err
	}
	return p.httpClient.Do(req)
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
