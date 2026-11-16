package gemini

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

func TestParseSnakeCaseAndCleanSchemaBuild(t *testing.T) {
	c := New()
	raw := `{
		"system_instruction": {"parts": [{"text": "You are a coder."}]},
		"generation_config": {
			"max_output_tokens": 2048,
			"thinking_config": {"thinking_budget": 4096}
		},
		"contents": [
			{"role": "user", "parts": [{"text": "Check file"}]},
			{"role": "model", "parts": [
				{"function_call": {"name": "read", "args": {"path": "go.mod"}}, "thought_signature": "tsig_xyz"}
			]},
			{"role": "user", "parts": [
				{"function_response": {"name": "read", "response": {"result": "module ok"}}}
			]}
		],
		"tools": [{"function_declarations": [{
			"name": "read",
			"parameters": {
				"$schema": "http://json-schema.org/draft-07/schema#",
				"type": "object",
				"additionalProperties": false,
				"properties": {"path": {"type": ["string", "null"]}}
			}
		}]}]
	}`

	req, ge := c.ParseRequest("gemini-3-flash-preview", []byte(raw))
	if ge != nil {
		t.Fatalf("ParseRequest error: %v", ge)
	}
	if req.SystemText() != "You are a coder." || req.MaxTokens != 2048 {
		t.Fatalf("unexpected header: %+v", req)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 IR messages, got %d", len(req.Messages))
	}
	if req.Messages[2].Parts[0].CallID != req.Messages[1].Parts[0].CallID {
		t.Fatalf("expected name-matched functionResponse call ID")
	}

	built, ge := c.BuildRequest(req, "gemini-3-flash-preview", true, codec.Quirks{})
	if ge != nil {
		t.Fatalf("BuildRequest error: %v", ge)
	}
	builtStr := string(built)
	if strings.Contains(builtStr, "$schema") || strings.Contains(builtStr, "additionalProperties") {
		t.Fatalf("expected unsupported JSON Schema fields stripped, got %s", builtStr)
	}
	if !strings.Contains(builtStr, "thinkingLevel") {
		t.Fatalf("expected thinkingLevel for gemini-3 model, got %s", builtStr)
	}

	for _, gem4Model := range []string{"gemini-4-hydrogen", "gemini-4-argon", "gemini-4-carbon"} {
		b4, ge4 := c.BuildRequest(req, gem4Model, true, codec.Quirks{})
		if ge4 != nil {
			t.Fatalf("BuildRequest(%s) error: %v", gem4Model, ge4)
		}
		if !strings.Contains(string(b4), "thinkingLevel") {
			t.Fatalf("expected thinkingLevel for %s model, got %s", gem4Model, string(b4))
		}
	}
}

func TestDecodeAndEncodeGeminiSSE(t *testing.T) {
	c := New()
	dec := c.NewDecoder(codec.Quirks{})

	frames := []string{
		`data: {"responseId":"r_1","modelVersion":"gemini-2.5-pro","candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"text":"plan","thoughtSignature":"sig_p"}]}}]}`,
		`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"calc","args":{"x":2}},"thoughtSignature":"sig_fc"}]}}]}`,
		`data: {"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[]}}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"thoughtsTokenCount":15}}`,
	}

	r := sse.NewReader(strings.NewReader(strings.Join(frames, "\n\n") + "\n\n"))
	col := ir.NewCollector()
	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	enc := c.NewEncoder("gemini-2.5-pro")

	for {
		ev, err := r.Next()
		if err != nil {
			break
		}
		events, _, ge := dec.DecodeFrame(ev)
		if ge != nil {
			t.Fatalf("decode error: %v", ge)
		}
		for _, item := range events {
			col.Add(item)
			if err := enc.Encode(item, sw); err != nil {
				t.Fatalf("encode error: %v", err)
			}
		}
	}
	for _, item := range dec.Flush() {
		col.Add(item)
		_ = enc.Encode(item, sw)
	}

	res := col.Build()
	if res.FinishReason != ir.FinishToolCalls {
		t.Fatalf("expected FinishToolCalls when tool emitted, got %q", res.FinishReason)
	}
	if res.Usage.InputTokens != 20 || res.Usage.OutputTokens != 20 || res.Usage.ReasoningTokens != 15 {
		t.Fatalf("unexpected usage: %+v", res.Usage)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.Split(rec.Body.String(), "\n\n")[0], "data: ")), &payload); err != nil {
		t.Fatalf("invalid encoded SSE JSON: %v", err)
	}
}
