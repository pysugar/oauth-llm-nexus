package keyproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// BuildUpstreamRequest creates an upstream request by:
// - cloning query params
// - removing client-provided key and injecting server-side key
// - copying sanitized headers
func BuildUpstreamRequest(
	ctx context.Context,
	method string,
	target *url.URL,
	incomingQuery url.Values,
	incomingHeaders http.Header,
	body []byte,
	apiKey string,
) (*http.Request, error) {
	if target == nil {
		return nil, fmt.Errorf("target URL is required")
	}

	targetCopy := *target
	query := CloneValues(incomingQuery)
	query.Del("key")
	query.Set("key", strings.TrimSpace(apiKey))
	targetCopy.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, method, targetCopy.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	CopyForwardHeaders(req.Header, incomingHeaders)
	return req, nil
}

func CloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for k, arr := range values {
		cp := make([]string, len(arr))
		copy(cp, arr)
		cloned[k] = cp
	}
	return cloned
}

func CopyForwardHeaders(dst, src http.Header) {
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

// CopyResponse streams upstream status/headers/body to downstream writer.
// Response body is flushed chunk-by-chunk when downstream supports http.Flusher.
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
