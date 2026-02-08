package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/upstream/codex"
)

type fakeCodexProvider struct {
	lastPayload map[string]interface{}
	resp        *http.Response
	err         error
}

func (f *fakeCodexProvider) StreamResponses(payload map[string]interface{}) (*http.Response, error) {
	raw, _ := json.Marshal(payload)
	var copied map[string]interface{}
	_ = json.Unmarshal(raw, &copied)
	f.lastPayload = copied
	return f.resp, f.err
}

func (f *fakeCodexProvider) GetQuota() *codex.QuotaInfo {
	return &codex.QuotaInfo{Email: "test@example.com", PlanType: "plus", HasAccess: true}
}

func TestFilterUnsupportedCodexParams(t *testing.T) {
	payload := map[string]interface{}{
		"model":             "gpt-5.2-codex",
		"temperature":       0.5,
		"top_p":             0.9,
		"max_output_tokens": 1024,
	}

	filtered := filterUnsupportedCodexParams(payload)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 filtered params, got %d (%v)", len(filtered), filtered)
	}
	if _, ok := payload["temperature"]; ok {
		t.Fatal("temperature should be removed")
	}
	if _, ok := payload["top_p"]; ok {
		t.Fatal("top_p should be removed")
	}
	if _, ok := payload["max_output_tokens"]; ok {
		t.Fatal("max_output_tokens should be removed")
	}
}

func TestHandleCodexResponsesPassthrough_ProxyMode(t *testing.T) {
	oldProvider := CodexProvider
	defer func() { CodexProvider = oldProvider }()

	fake := &fakeCodexProvider{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\"}\n\n" +
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
					"data: {\"type\":\"response.completed\"}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	CodexProvider = fake

	reqBody := `{
		"model":"gpt-5.2-codex",
		"input":"hello codex",
		"temperature":0.7,
		"top_p":0.9,
		"max_output_tokens":1024
	}`
	w := httptest.NewRecorder()
	handleCodexResponsesPassthrough(w, []byte(reqBody), "gpt-5.2-codex", "req-test")

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-Nexus-Codex-Filtered-Params"); got == "" {
		t.Fatal("expected X-Nexus-Codex-Filtered-Params header to be set")
	}

	if fake.lastPayload["stream"] != true {
		t.Fatalf("expected stream=true, got %v", fake.lastPayload["stream"])
	}
	if fake.lastPayload["store"] != false {
		t.Fatalf("expected store=false, got %v", fake.lastPayload["store"])
	}
	if fake.lastPayload["parallel_tool_calls"] != true {
		t.Fatalf("expected parallel_tool_calls=true, got %v", fake.lastPayload["parallel_tool_calls"])
	}
	if _, ok := fake.lastPayload["temperature"]; ok {
		t.Fatal("temperature should be filtered before upstream")
	}
	if _, ok := fake.lastPayload["top_p"]; ok {
		t.Fatal("top_p should be filtered before upstream")
	}
	if _, ok := fake.lastPayload["max_output_tokens"]; ok {
		t.Fatal("max_output_tokens should be filtered before upstream")
	}

	input, ok := fake.lastPayload["input"].([]interface{})
	if !ok || len(input) != 1 {
		t.Fatalf("expected normalized input array, got %#v", fake.lastPayload["input"])
	}
	msg, ok := input[0].(map[string]interface{})
	if !ok || msg["type"] != "message" || msg["role"] != "user" {
		t.Fatalf("expected normalized message object, got %#v", input[0])
	}

	body := w.Body.String()
	if !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("expected SSE passthrough body, got %s", body)
	}
}

func TestHandleCodexChatRequest_NonStreaming(t *testing.T) {
	oldProvider := CodexProvider
	defer func() { CodexProvider = oldProvider }()

	fake := &fakeCodexProvider{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n" +
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}}\n\n",
			)),
		},
	}
	CodexProvider = fake

	req := map[string]interface{}{
		"model": "gpt-5.2-codex",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "Say hello",
			},
		},
		"stream": false,
	}
	w := httptest.NewRecorder()
	handleCodexChatRequest(w, req, "req-chat")

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("expected JSON response, got err: %v body=%s", err, w.Body.String())
	}
	choices, ok := got["choices"].([]interface{})
	if !ok || len(choices) != 1 {
		t.Fatalf("expected one choice, got %#v", got["choices"])
	}
	choice, _ := choices[0].(map[string]interface{})
	message, _ := choice["message"].(map[string]interface{})
	if message["content"] != "Hello world" {
		t.Fatalf("expected aggregated content, got %v", message["content"])
	}
}
