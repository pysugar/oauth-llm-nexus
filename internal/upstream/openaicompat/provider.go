package openaicompat

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/upstream/keyproxy"
)

const defaultTimeout = 180 * time.Second

// Provider proxies OpenAI-compatible chat/completions requests to upstream.
type Provider struct {
	id            string
	apiKey        string
	baseURL       string
	staticHeaders map[string]string
	httpClient    *http.Client
}

func NewProvider(id, apiKey, baseURL string, timeout time.Duration, staticHeaders map[string]string) *Provider {
	return NewProviderWithClient(id, apiKey, baseURL, timeout, staticHeaders, nil)
}

func NewProviderWithClient(
	id, apiKey, baseURL string,
	timeout time.Duration,
	staticHeaders map[string]string,
	httpClient *http.Client,
) *Provider {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	headers := make(map[string]string, len(staticHeaders))
	for k, v := range staticHeaders {
		headers[k] = v
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Provider{
		id:            strings.ToLower(strings.TrimSpace(id)),
		apiKey:        strings.TrimSpace(apiKey),
		baseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		staticHeaders: headers,
		httpClient:    httpClient,
	}
}

func (p *Provider) IsEnabled() bool {
	return p != nil && p.id != "" && p.baseURL != "" && p.apiKey != ""
}

func (p *Provider) ForwardChatCompletions(
	ctx context.Context,
	method string,
	incomingQuery url.Values,
	incomingHeaders http.Header,
	body []byte,
) (*http.Response, error) {
	if !p.IsEnabled() {
		return nil, fmt.Errorf("openai compat provider is not enabled")
	}
	if method != http.MethodPost {
		return nil, fmt.Errorf("unsupported method: %s", method)
	}

	target, err := url.Parse(p.baseURL + "/chat/completions")
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	if incomingQuery != nil {
		target.RawQuery = keyproxy.CloneValues(incomingQuery).Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	copyForwardHeaders(req.Header, incomingHeaders)
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	for k, v := range p.staticHeaders {
		req.Header.Set(k, v)
	}
	if strings.TrimSpace(req.Header.Get("Content-Type")) == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return p.httpClient.Do(req)
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
		"Api-Key",
		"X-Api-Key",
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

func CopyResponse(w http.ResponseWriter, resp *http.Response) error {
	return keyproxy.CopyResponse(w, resp)
}
