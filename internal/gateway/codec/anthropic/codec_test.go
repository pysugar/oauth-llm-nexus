package anthropic

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

func TestParseAndBuildAnthropicWithToolPairing(t *testing.T) {
	c := New()
	raw := `{
		"model": "claude-sonnet-4-5",
		"system": [{"type": "text", "text": "System prompt"}],
		"max_tokens": 4096,
		"thinking": {"type": "enabled", "budget_tokens": 2048},
		"messages": [
			{"role": "user", "content": "Run ping"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "hm", "signature": "sig123"},
				{"type": "tool_use", "id": "call:weird/id", "name": "ping", "input": {"host": "127.0.0.1"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call:weird/id", "content": "ok"},
				{"type": "text", "text": "Thanks"}
			]}
		],
		"tools": [{"name": "ping", "input_schema": {"type": "object"}}]
	}`

	req, ge := c.ParseRequest("", []byte(raw))
	if ge != nil {
		t.Fatalf("ParseRequest error: %v", ge)
	}
	if req.SystemText() != "System prompt" || req.Reasoning.Budget != 2048 {
		t.Fatalf("unexpected request metadata: %+v", req)
	}

	built, ge := c.BuildRequest(req, "claude-sonnet-4-5", true, codec.Quirks{})
	if ge != nil {
		t.Fatalf("BuildRequest error: %v", ge)
	}
	var out map[string]any
	if err := json.Unmarshal(built, &out); err != nil {
		t.Fatalf("unmarshal built: %v", err)
	}
	if _, hasTemp := out["temperature"]; hasTemp {
		t.Fatalf("expected temperature omitted when thinking enabled")
	}
	msgs := out["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 alternating messages, got %d", len(msgs))
	}
	assistant := msgs[1].(map[string]any)
	toolUse := assistant["content"].([]any)[1].(map[string]any)
	if strings.Contains(toolUse["id"].(string), ":") {
		t.Fatalf("expected sanitized tool_use id, got %v", toolUse["id"])
	}
}

func TestDecodeFullInputBlockStartAndEncodeStream(t *testing.T) {
	c := New()
	dec := c.NewDecoder(codec.Quirks{})

	frames := []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":10,"cache_read_input_tokens":5}}}`,
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"ponder"}}`,
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_end"}}`,
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"go"}}}`,
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}`,
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}

	r := sse.NewReader(strings.NewReader(strings.Join(frames, "\n\n") + "\n\n"))
	col := ir.NewCollector()
	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	enc := c.NewEncoder("claude")

	for {
		ev, err := r.Next()
		if err != nil {
			break
		}
		events, done, ge := dec.DecodeFrame(ev)
		if ge != nil {
			t.Fatalf("decode error: %v", ge)
		}
		for _, item := range events {
			col.Add(item)
			if err := enc.Encode(item, sw); err != nil {
				t.Fatalf("encode error: %v", err)
			}
		}
		if done {
			break
		}
	}

	res := col.Build()
	if res.Usage.InputTokens != 15 || res.Usage.CacheReadTokens != 5 || res.Usage.OutputTokens != 20 {
		t.Fatalf("unexpected usage: %+v", res.Usage)
	}
	if len(res.Content) != 2 || res.Content[1].Args != `{"q":"go"}` {
		t.Fatalf("unexpected collected content: %+v", res.Content)
	}
	if !strings.Contains(rec.Body.String(), `"message_stop"`) {
		t.Fatalf("expected message_stop in encoded SSE, got %s", rec.Body.String())
	}
}
