package keyproxy

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestBuildUpstreamRequest_InjectsServerKeyAndSanitizesHeaders(t *testing.T) {
	target, err := url.Parse("https://example.com/v1beta/models/gemini-3-flash-preview:generateContent")
	if err != nil {
		t.Fatalf("failed to parse target URL: %v", err)
	}

	incomingQuery := url.Values{
		"key": {"client-key"},
		"alt": {"sse"},
	}
	incomingHeaders := http.Header{}
	incomingHeaders.Set("Authorization", "Bearer client-token")
	incomingHeaders.Set("X-Goog-Api-Key", "client-key")
	incomingHeaders.Set("X-Custom", "hello")
	incomingHeaders.Set("Content-Type", "application/json")

	req, err := BuildUpstreamRequest(
		context.Background(),
		http.MethodPost,
		target,
		incomingQuery,
		incomingHeaders,
		[]byte(`{"hello":"world"}`),
		"server-key",
	)
	if err != nil {
		t.Fatalf("BuildUpstreamRequest() error = %v", err)
	}

	if got := req.URL.Query().Get("key"); got != "server-key" {
		t.Fatalf("expected injected key=server-key, got %q", got)
	}
	if got := req.URL.Query().Get("alt"); got != "sse" {
		t.Fatalf("expected preserved alt=sse, got %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("expected Authorization to be stripped, got %q", got)
	}
	if got := req.Header.Get("X-Goog-Api-Key"); got != "" {
		t.Fatalf("expected X-Goog-Api-Key to be stripped, got %q", got)
	}
	if got := req.Header.Get("X-Custom"); got != "hello" {
		t.Fatalf("expected X-Custom=hello, got %q", got)
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != `{"hello":"world"}` {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestCopyResponse_CopiesStatusHeadersBodyAndFlushes(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusAccepted,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Connection":   []string{"keep-alive"},
		},
		Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}
	defer resp.Body.Close()

	writer := &flushRecorder{header: make(http.Header)}
	if err := CopyResponse(writer, resp); err != nil {
		t.Fatalf("CopyResponse() error = %v", err)
	}

	if writer.status != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", writer.status)
	}
	if got := writer.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", got)
	}
	if got := writer.header.Get("Connection"); got != "" {
		t.Fatalf("expected Connection header to be skipped, got %q", got)
	}
	if writer.body.String() != `{"ok":true}` {
		t.Fatalf("unexpected body: %s", writer.body.String())
	}
	if writer.flushCount == 0 {
		t.Fatalf("expected Flush() to be called at least once")
	}
}

type flushRecorder struct {
	header     http.Header
	body       strings.Builder
	status     int
	flushCount int
}

func (r *flushRecorder) Header() http.Header {
	return r.header
}

func (r *flushRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
}

func (r *flushRecorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
}

func (r *flushRecorder) Flush() {
	r.flushCount++
}
