package openairesp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

func TestParseAndRejectPreviousResponseIDOnBuild(t *testing.T) {
	c := New()
	raw := `{
		"model": "gpt-4o",
		"instructions": "Be helpful.",
		"previous_response_id": "resp_prev",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hi"}]},
			{"type": "function_call", "call_id": "c1", "name": "ping", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "c1", "output": "pong"}
		]
	}`

	req, ge := c.ParseRequest("", []byte(raw))
	if ge != nil {
		t.Fatalf("ParseRequest error: %v", ge)
	}
	if req.PreviousResponseID != "resp_prev" {
		t.Fatalf("expected previous_response_id, got %q", req.PreviousResponseID)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}

	if _, ge := c.BuildRequest(req, "gpt-4o", true, codec.Quirks{}); ge == nil || ge.HTTPStatus != 400 {
		t.Fatalf("expected 400 rejecting previous_response_id during translation, got %v", ge)
	}

	req.PreviousResponseID = ""
	built, ge := c.BuildRequest(req, "o3-mini", true, codec.Quirks{})
	if ge != nil {
		t.Fatalf("BuildRequest error: %v", ge)
	}
	var out map[string]any
	if err := json.Unmarshal(built, &out); err != nil {
		t.Fatalf("unmarshal built: %v", err)
	}
	if out["model"] != "o3-mini" || out["instructions"] != "Be helpful." {
		t.Fatalf("unexpected built output: %+v", out)
	}
}

func TestDecodeParallelToolsAndEncodeResponsesStream(t *testing.T) {
	c := New()
	dec := c.NewDecoder(codec.Quirks{})

	frames := []string{
		`event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"r1","model":"gpt-4o"}}`,
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","item":{"id":"fc1","type":"function_call","call_id":"call_1","name":"toolA"}}`,
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","item":{"id":"fc2","type":"function_call","call_id":"call_2","name":"toolB"}}`,
		`event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","item_id":"fc2","delta":"{\"y\":2}"}`,
		`event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","item_id":"fc1","delta":"{\"x\":1}"}`,
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","item":{"id":"fc2","type":"function_call"}}`,
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","item":{"id":"fc1","type":"function_call"}}`,
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":8}}}`,
	}

	r := sse.NewReader(strings.NewReader(strings.Join(frames, "\n\n") + "\n\n"))
	col := ir.NewCollector()
	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	enc := c.NewEncoder("gpt-4o")

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
	if len(res.Content) != 2 {
		t.Fatalf("expected 2 sequential tool calls, got %+v", res.Content)
	}
	if res.Content[0].CallID != "call_1" || res.Content[0].Args != `{"x":1}` {
		t.Fatalf("unexpected call_1: %+v", res.Content[0])
	}
	if res.Content[1].CallID != "call_2" || res.Content[1].Args != `{"y":2}` {
		t.Fatalf("unexpected call_2: %+v", res.Content[1])
	}
	if !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("expected response.completed event in SSE output, got %s", rec.Body.String())
	}
}
