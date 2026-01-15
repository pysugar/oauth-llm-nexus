package upstream

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// mockSSEBody creates a mock SSE response body
func mockSSEBody(chunks []string) io.ReadCloser {
	var buf bytes.Buffer
	for _, chunk := range chunks {
		buf.WriteString("data: ")
		buf.WriteString(chunk)
		buf.WriteString("\n\n")
	}
	buf.WriteString("data: [DONE]\n\n")
	return io.NopCloser(&buf)
}

func TestConsumeAndMergeSSE_TextOnly(t *testing.T) {
	client := NewClient()

	chunks := []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":" world!"}]}}]}}`,
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockSSEBody(chunks),
	}

	merged, err := client.consumeAndMergeSSE(resp)
	if err != nil {
		t.Fatalf("consumeAndMergeSSE failed: %v", err)
	}

	// Should contain merged text
	if !bytes.Contains(merged, []byte("Hello world!")) {
		t.Errorf("Expected merged text 'Hello world!', got: %s", string(merged))
	}
}

func TestConsumeAndMergeSSE_WithFunctionCall(t *testing.T) {
	client := NewClient()

	chunks := []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"location":"Tokyo"}}}]}}]}}`,
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockSSEBody(chunks),
	}

	merged, err := client.consumeAndMergeSSE(resp)
	if err != nil {
		t.Fatalf("consumeAndMergeSSE failed: %v", err)
	}

	// Should preserve functionCall
	if !bytes.Contains(merged, []byte("functionCall")) {
		t.Errorf("Expected functionCall to be preserved, got: %s", string(merged))
	}
	if !bytes.Contains(merged, []byte("get_weather")) {
		t.Errorf("Expected function name 'get_weather', got: %s", string(merged))
	}
}

func TestConsumeAndMergeSSE_ParseError(t *testing.T) {
	client := NewClient()

	// Mix of valid and invalid chunks
	chunks := []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Valid"}]}}]}}`,
		`{invalid json`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":" text"}]}}]}}`,
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockSSEBody(chunks),
	}

	merged, err := client.consumeAndMergeSSE(resp)
	if err != nil {
		t.Fatalf("consumeAndMergeSSE failed: %v", err)
	}

	// Should still merge valid chunks
	if !bytes.Contains(merged, []byte("Valid text")) {
		t.Errorf("Expected merged valid text, got: %s", string(merged))
	}
}

func TestConsumeAndMergeSSE_EmptyStream(t *testing.T) {
	client := NewClient()

	// Empty stream with only [DONE]
	chunks := []string{}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockSSEBody(chunks),
	}

	merged, err := client.consumeAndMergeSSE(resp)
	if err != nil {
		t.Fatalf("consumeAndMergeSSE failed: %v", err)
	}

	// Should return default empty response
	if len(merged) == 0 {
		t.Error("Expected non-empty default response for empty stream")
	}
	if !bytes.Contains(merged, []byte("response")) {
		t.Errorf("Expected valid JSON with 'response' field, got: %s", string(merged))
	}
}

func TestConsumeAndMergeSSE_WithUsageMetadata(t *testing.T) {
	client := NewClient()

	chunks := []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Test"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}}`,
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockSSEBody(chunks),
	}

	merged, err := client.consumeAndMergeSSE(resp)
	if err != nil {
		t.Fatalf("consumeAndMergeSSE failed: %v", err)
	}

	// Should preserve usageMetadata
	if !bytes.Contains(merged, []byte("usageMetadata")) {
		t.Errorf("Expected usageMetadata to be preserved, got: %s", string(merged))
	}
	if !bytes.Contains(merged, []byte("finishReason")) {
		t.Errorf("Expected finishReason to be preserved, got: %s", string(merged))
	}
}
