package upstream

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

type trackingBody struct {
	io.ReadCloser
	closed *atomic.Int32
}

func (t *trackingBody) Close() error {
	t.closed.Add(1)
	return t.ReadCloser.Close()
}

type scriptedRoundTripper struct {
	statusCodes []int
	calls       int
	closed      []*atomic.Int32
}

func (s *scriptedRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	code := s.statusCodes[s.calls]
	counter := &atomic.Int32{}
	s.closed = append(s.closed, counter)
	s.calls++
	return &http.Response{
		StatusCode: code,
		Body:       &trackingBody{ReadCloser: io.NopCloser(bytes.NewBufferString(`{}`)), closed: counter},
		Header:     make(http.Header),
	}, nil
}

func TestDoRequestWithFallback_ClosesIntermediateResponses(t *testing.T) {
	origBaseURLs := BaseURLs
	BaseURLs = []string{
		"https://endpoint-1/v1internal",
		"https://endpoint-2/v1internal",
		"https://endpoint-3/v1internal",
	}
	defer func() { BaseURLs = origBaseURLs }()

	rt := &scriptedRoundTripper{statusCodes: []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusOK}}
	client := &Client{httpClient: &http.Client{Transport: rt}}

	resp, err := client.doRequestWithFallback("generateContent", "", "token", map[string]interface{}{
		"request": map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("doRequestWithFallback error: %v", err)
	}
	_ = resp.Body.Close()

	if got := rt.closed[0].Load(); got != 1 {
		t.Fatalf("expected first response body to be closed once, got %d", got)
	}
	if got := rt.closed[1].Load(); got != 1 {
		t.Fatalf("expected second response body to be closed once, got %d", got)
	}
}
