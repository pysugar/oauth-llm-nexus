package handlers

import (
	"encoding/json"
	"errors"
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

type failingReaderCodex struct {
	payload []byte
	read    bool
	err     error
}

func (r *failingReaderCodex) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(p, r.payload), nil
	}
	return 0, r.err
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

func TestStreamCodexToOpenAI_ScannerErrorDoesNotEmitDone(t *testing.T) {
	reader := &failingReaderCodex{
		payload: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"item_id\":\"item_1\"}\n\n"),
		err:     errors.New("forced scanner failure"),
	}

	rec := httptest.NewRecorder()
	streamCodexToOpenAI(rec, reader, "gpt-5.2-codex", "req-stream-err")

	body := rec.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Fatalf("expected at least one streamed chat chunk, got %q", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Fatalf("did not expect done sentinel on scanner error, got %q", body)
	}
}

func TestHandleCodexChatRequest_NonStreamingScannerErrorReturnsBadGateway(t *testing.T) {
	oldProvider := CodexProvider
	defer func() { CodexProvider = oldProvider }()

	fake := &fakeCodexProvider{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(&failingReaderCodex{
				payload: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"),
				err:     errors.New("forced scanner failure"),
			}),
		},
	}
	CodexProvider = fake

	req := map[string]interface{}{
		"model": "gpt-5.2-codex",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "hello"},
		},
		"stream": false,
	}
	rec := httptest.NewRecorder()
	handleCodexChatRequest(rec, req, "req-collect-err")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on scanner error, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json error response, got %v (%s)", err, rec.Body.String())
	}
	errObj, _ := payload["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatalf("expected error object, got %#v", payload)
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "Codex stream read error") {
		t.Fatalf("expected scanner error message, got %#v", errObj["message"])
	}
}

func TestHandleCodexChatRequest_UpstreamErrorNormalizedEnvelope(t *testing.T) {
	oldProvider := CodexProvider
	defer func() { CodexProvider = oldProvider }()

	fake := &fakeCodexProvider{
		resp: &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`,
			)),
		},
	}
	CodexProvider = fake

	req := map[string]interface{}{
		"model": "gpt-5.2-codex",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "hello"},
		},
		"stream": false,
	}
	rec := httptest.NewRecorder()
	handleCodexChatRequest(rec, req, "req-upstream-404")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json error response, got %v (%s)", err, rec.Body.String())
	}
	errObj, _ := payload["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatalf("expected error object, got %#v", payload)
	}
	if typ, _ := errObj["type"].(string); typ != "invalid_request_error" {
		t.Fatalf("expected invalid_request_error, got %#v", errObj["type"])
	}
	if _, ok := errObj["code"]; !ok {
		t.Fatalf("expected error.code, got %#v", errObj)
	}
}

func TestHandleCodexResponsesPassthrough_UpstreamErrorNormalizedEnvelope(t *testing.T) {
	oldProvider := CodexProvider
	defer func() { CodexProvider = oldProvider }()

	fake := &fakeCodexProvider{
		resp: &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`,
			)),
		},
	}
	CodexProvider = fake

	rec := httptest.NewRecorder()
	reqBody := `{"model":"gpt-5.2-codex","input":"hello"}`
	handleCodexResponsesPassthrough(rec, []byte(reqBody), "gpt-5.2-codex", "req-responses-429")

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json error response, got %v (%s)", err, rec.Body.String())
	}
	errObj, _ := payload["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatalf("expected error object, got %#v", payload)
	}
	if typ, _ := errObj["type"].(string); typ != "rate_limit_error" {
		t.Fatalf("expected rate_limit_error, got %#v", errObj["type"])
	}
	if _, ok := errObj["code"]; !ok {
		t.Fatalf("expected error.code, got %#v", errObj)
	}
}
