package openaichat

import (
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

func decodeAll(t *testing.T, q codec.Quirks, frames ...string) []ir.Event {
	t.Helper()
	dec := New().NewDecoder(q)
	var evs []ir.Event
	for _, f := range frames {
		out, _, ge := dec.DecodeFrame(sse.Event{Data: []byte(f)})
		if ge != nil {
			t.Fatal(ge)
		}
		evs = append(evs, out...)
	}
	return append(evs, dec.Flush()...)
}

func TestReusedToolIndexStartsANewCall(t *testing.T) {
	evs := decodeAll(t, codec.Quirks{},
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"ls","arguments":"{\"p\":1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"b","function":{"name":"cat","arguments":"{\"p\":2}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	col := ir.NewCollector()
	for _, ev := range evs {
		col.Add(ev)
	}
	var calls []ir.Part
	for _, p := range col.Build().Content {
		if p.Kind == ir.KindToolCall {
			calls = append(calls, p)
		}
	}
	if len(calls) != 2 || calls[0].Args != `{"p":1}` || calls[1].Name != "cat" || calls[1].Args != `{"p":2}` {
		t.Fatalf("expected two separate calls, got %+v", calls)
	}
}

func TestThinkTagOnlyAtReplyStart(t *testing.T) {
	evs := decodeAll(t, codec.Quirks{ThoughtTagParsing: true},
		`{"choices":[{"index":0,"delta":{"content":"Use the "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"<think> tag like this"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	var text strings.Builder
	for _, ev := range evs {
		if ev.Kind == ir.EventThought {
			t.Fatalf("a tag in prose must not become reasoning: %+v", ev)
		}
		if ev.Kind == ir.EventText {
			text.WriteString(ev.Text)
		}
	}
	if text.String() != "Use the <think> tag like this" {
		t.Fatalf("unexpected text %q", text.String())
	}
	evs = decodeAll(t, codec.Quirks{},
		`{"choices":[{"index":0,"delta":{"content":"<think>plan</think>answer"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	var first ir.Event
	for _, ev := range evs {
		if ev.Kind == ir.EventThought || ev.Kind == ir.EventText {
			first = ev
			break
		}
	}
	if first.Kind != ir.EventThought || first.Text != "plan" {
		t.Fatalf("leading think tag must still be reasoning, got %+v", evs)
	}
}
