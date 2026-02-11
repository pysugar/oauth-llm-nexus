package vertexkey

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://aiplatform.googleapis.com"
	defaultTimeout = 5 * time.Minute
)

var allowedActions = map[string]struct{}{
	"generateContent":       {},
	"streamGenerateContent": {},
	"countTokens":           {},
}

// Provider transparently proxies Gemini-compatible requests to Vertex API using
// a server-side API key.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewProvider creates a Provider with explicit configuration.
func NewProvider(apiKey, baseURL string, timeout time.Duration) *Provider {
	return NewProviderWithClient(apiKey, baseURL, timeout, nil)
}

// NewProviderWithClient creates a Provider with an optional custom HTTP client.
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

// NewProviderFromEnv creates a Provider using environment variables.
func NewProviderFromEnv() *Provider {
	apiKey := strings.TrimSpace(getenv("NEXUS_VERTEX_API_KEY"))
	if apiKey == "" {
		return nil
	}

	baseURL := strings.TrimSpace(getenv("NEXUS_VERTEX_BASE_URL"))
	timeout := parseTimeoutFromEnv("NEXUS_VERTEX_PROXY_TIMEOUT")
	return NewProvider(apiKey, baseURL, timeout)
}

// IsEnabled indicates whether provider has valid API key.
func (p *Provider) IsEnabled() bool {
	return p != nil && p.apiKey != ""
}

// Forward proxies request to Vertex API:
// /v1beta/models/{model}:{action} -> /v1/publishers/google/models/{model}:{action}
func (p *Provider) Forward(
	ctx context.Context,
	method string,
	model string,
	action string,
	incomingQuery url.Values,
	incomingHeaders http.Header,
	body []byte,
) (*http.Response, error) {
	if !p.IsEnabled() {
		return nil, fmt.Errorf("vertex key provider is not enabled")
	}

	if _, ok := allowedActions[action]; !ok {
		return nil, fmt.Errorf("unsupported action: %s", action)
	}

	model = normalizeModel(model)
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}

	targetURL := fmt.Sprintf("%s/v1/publishers/google/models/%s:%s", p.baseURL, url.PathEscape(model), action)
	parsedTarget, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	query := cloneValues(incomingQuery)
	query.Del("key")
	query.Set("key", p.apiKey)
	parsedTarget.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, method, parsedTarget.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	copyForwardHeaders(req.Header, incomingHeaders)
	return p.httpClient.Do(req)
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for k, arr := range values {
		cp := make([]string, len(arr))
		copy(cp, arr)
		cloned[k] = cp
	}
	return cloned
}

func copyForwardHeaders(dst, src http.Header) {
	for k, values := range src {
		canonical := http.CanonicalHeaderKey(k)
		if shouldSkipRequestHeader(canonical) {
			continue
		}
		for _, v := range values {
			dst.Add(canonical, v)
		}
	}
}

func shouldSkipRequestHeader(header string) bool {
	switch header {
	case "Authorization",
		"X-Goog-Api-Key",
		"Accept-Encoding",
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Transfer-Encoding",
		"Te",
		"Trailer",
		"Upgrade",
		"Proxy-Authenticate",
		"Proxy-Authorization":
		return true
	default:
		return false
	}
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

func normalizeModel(model string) string {
	trimmed := strings.TrimSpace(model)
	trimmed = strings.TrimPrefix(trimmed, "google/")
	return strings.TrimSpace(trimmed)
}

var getenv = func(key string) string {
	return os.Getenv(key)
}

// CopyResponse streams upstream response headers/body to downstream writer.
func CopyResponse(w http.ResponseWriter, resp *http.Response) error {
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	return copyResponseBodyWithFlush(w, resp.Body)
}

func copyResponseHeaders(dst, src http.Header) {
	for k, values := range src {
		canonical := http.CanonicalHeaderKey(k)
		if shouldSkipResponseHeader(canonical) {
			continue
		}
		for _, v := range values {
			dst.Add(canonical, v)
		}
	}
}

func shouldSkipResponseHeader(header string) bool {
	switch header {
	case "Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Transfer-Encoding",
		"Te",
		"Trailer",
		"Upgrade",
		"Proxy-Authenticate",
		"Proxy-Authorization":
		return true
	default:
		return false
	}
}

func copyResponseBodyWithFlush(w http.ResponseWriter, src io.Reader) error {
	buf := make([]byte, 32*1024)
	flusher, canFlush := w.(http.Flusher)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
