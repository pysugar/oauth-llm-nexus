package openaichat

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

func TestParseAndBuildOpenAIChatRoundTrip(t *testing.T) {
	c := New()
	rawReq := `{
		"model": "gpt-4o",
		"stream": true,
		"max_completion_tokens": 1024,
		"reasoning_effort": "high",
		"messages": [
			{"role": "system", "content": "Be brief."},
			{"role": "user", "content": [
				{"type": "text", "text": "Inspect this"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}
			]},
			{"role": "assistant", "reasoning_content": "thinking", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "lookup", "arguments": "{\"q\":\"a\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "result_a"}
		],
		"tools": [{"type": "function", "function": {"name": "lookup", "parameters": {"type": "object"}}}]
	}`

	req, ge := c.ParseRequest("", []byte(rawReq))
	if ge != nil {
		t.Fatalf("ParseRequest error: %v", ge)
	}
	if req.Model != "gpt-4o" || req.MaxTokens != 1024 || req.Reasoning.Effort != "high" {
		t.Fatalf("unexpected parsed request: %+v", req)
	}
	if req.SystemText() != "Be brief." {
		t.Fatalf("unexpected system text: %q", req.SystemText())
	}

	built, ge := c.BuildRequest(req, "deepseek-reasoner", true, codec.Quirks{
		MaxCompletionTokens:  false,
		EchoReasoningContent: true,
		IncludeUsageInStream: true,
	})
	if ge != nil {
		t.Fatalf("BuildRequest error: %v", ge)
	}

	var check map[string]any
	if err := json.Unmarshal(built, &check); err != nil {
		t.Fatalf("unmarshal built request: %v", err)
	}
	if check["model"] != "deepseek-reasoner" {
		t.Fatalf("expected rewritten model, got %v", check["model"])
	}
	if _, ok := check["max_tokens"]; !ok {
		t.Fatalf("expected max_tokens when MaxCompletionTokens quirk is false")
	}
	msgs := check["messages"].([]any)
	assistant := msgs[2].(map[string]any)
	if assistant["reasoning_content"] != "thinking" {
		t.Fatalf("expected reasoning_content echoed for DeepSeek, got %v", assistant["reasoning_content"])
	}
}

func TestDecodeThinkTagsAndParallelTools(t *testing.T) {
	c := New()
	dec := c.NewDecoder(codec.Quirks{ThoughtTagParsing: true})

	streamData := strings.Join([]string{
		`data: {"id":"cmpl_1","model":"r1","choices":[{"index":0,"delta":{"role":"assistant","content":"<thi"}}]}`,
		`data: {"id":"cmpl_1","model":"r1","choices":[{"index":0,"delta":{"content":"nk>secret</think>Hello!"}}]}`,
		`data: {"id":"cmpl_1","model":"r1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"f0","arguments":"{\"a\":"}}]}}]}`,
		`data: {"id":"cmpl_1","model":"r1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c1","type":"function","function":{"name":"f1","arguments":"{\"b\":2}"}}]}}]}`,
		`data: {"id":"cmpl_1","model":"r1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`data: {"id":"cmpl_1","model":"r1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"cmpl_1","model":"r1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	r := sse.NewReader(strings.NewReader(streamData))
	col := ir.NewCollector()
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
		}
		if done {
			break
		}
	}

	res := col.Build()
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 {
		t.Fatalf("expected usage (10,5), got %+v", res.Usage)
	}
	if len(res.Content) != 4 {
		t.Fatalf("expected 4 parts (thought, text, tool0, tool1), got %d: %+v", len(res.Content), res.Content)
	}
	if res.Content[0].Kind != ir.KindThought || res.Content[0].Text != "secret" {
		t.Fatalf("unexpected thought part: %+v", res.Content[0])
	}
	if res.Content[1].Kind != ir.KindText || res.Content[1].Text != "Hello!" {
		t.Fatalf("unexpected text part: %+v", res.Content[1])
	}
	if res.Content[2].CallID != "c0" || res.Content[2].Args != `{"a":1}` {
		t.Fatalf("unexpected tool0: %+v", res.Content[2])
	}
	if res.Content[3].CallID != "c1" || res.Content[3].Args != `{"b":2}` {
		t.Fatalf("unexpected tool1: %+v", res.Content[3])
	}

	// Verify encoder round-trip outputs SSE cleanly.
	rec := httptest.NewRecorder()
	w := sse.NewWriter(rec)
	enc := c.NewEncoder("r1")
	_ = enc.Encode(ir.Event{Kind: ir.EventStart, ResponseID: res.ID, Model: "r1"}, w)
	_ = enc.Encode(ir.Event{Kind: ir.EventText, Text: "Hello!"}, w)
	_ = enc.Encode(ir.Event{Kind: ir.EventFinish, FinishReason: ir.FinishStop, Usage: res.Usage}, w)
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("expected [DONE] sentinel in encoded SSE, got %q", rec.Body.String())
	}
}

func TestIndexlessParallelToolCallsDoNotCollide(t *testing.T) {
	c := New()
	dec := c.NewDecoder(codec.Quirks{})
	col := ir.NewCollector()

	ssePayload := strings.Join([]string{
		`data: {"id":"cmpl_noidx","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"x\":"}}]}}]}`,
		`data: {"id":"cmpl_noidx","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"1}"}}]}}]}`,
		`data: {"id":"cmpl_noidx","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"y\":2}"}}]}}]}`,
		`data: {"id":"cmpl_noidx","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	r := sse.NewReader(strings.NewReader(ssePayload))
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
		}
		if done {
			break
		}
	}
	res := col.Build()
	if len(res.Content) != 2 {
		t.Fatalf("expected 2 distinct tool calls, got %d: %+v", len(res.Content), res.Content)
	}
	if res.Content[0].CallID != "call_a" || res.Content[0].Name != "first" || res.Content[0].Args != `{"x":1}` {
		t.Fatalf("unexpected first tool call: %+v", res.Content[0])
	}
	if res.Content[1].CallID != "call_b" || res.Content[1].Name != "second" || res.Content[1].Args != `{"y":2}` {
		t.Fatalf("unexpected second tool call: %+v", res.Content[1])
	}
}
