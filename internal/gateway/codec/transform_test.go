package codec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

func TestMergeExtraBodyJSON(t *testing.T) {
	body := []byte(`{"model":"m", "messages":[{"role":"user","content":"hi"}],"temperature":0.2 }`)
	extra := map[string]json.RawMessage{
		"temperature":      json.RawMessage(`0.9`),
		"!top_p":           json.RawMessage(`0.5`),
		"prompt_cache_key": json.RawMessage(`"pkm"`),
		"provider":         json.RawMessage(`{ "order": ["a", "b"] }`),
		"model":            json.RawMessage(`"other"`),
		"!stream":          json.RawMessage(`true`),
	}
	out, err := MergeExtraBodyJSON(body, extra)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m", "messages":[{"role":"user","content":"hi"}],"temperature":0.2,"prompt_cache_key":"pkm","provider":{"order":["a","b"]},"top_p":0.5 }`
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	forced, _ := MergeExtraBodyJSON(out, map[string]json.RawMessage{"!temperature": json.RawMessage(`1`)})
	if !strings.Contains(string(forced), `"temperature":1,`) || !strings.HasPrefix(string(forced), `{"model":"m", "messages":[`) {
		t.Fatalf("forced: %s", forced)
	}
	empty, _ := MergeExtraBodyJSON([]byte(`{}`), map[string]json.RawMessage{"a": json.RawMessage(`1`)})
	if string(empty) != `{"a":1}` {
		t.Fatalf("empty: %s", empty)
	}
	if arr, _ := MergeExtraBodyJSON([]byte(`[1]`), extra); string(arr) != `[1]` {
		t.Fatalf("non-object changed: %s", arr)
	}
}

func TestExtraBodyValidation(t *testing.T) {
	if msg := ExtraBodyProblem(map[string]json.RawMessage{"!messages": json.RawMessage(`[]`)}); !strings.Contains(msg, "messages") {
		t.Fatalf("reserved accepted: %q", msg)
	}
	if msg := ExtraBodyProblem(map[string]json.RawMessage{"a": json.RawMessage(`1`), "!a": json.RawMessage(`2`)}); !strings.Contains(msg, "twice") {
		t.Fatalf("duplicate accepted: %q", msg)
	}
	clean, dropped := CleanExtraBody(map[string]json.RawMessage{"tools": json.RawMessage(`[]`), "!x": json.RawMessage(`{ "y": 1 }`)})
	if len(dropped) != 1 || string(clean["!x"]) != `{"y":1}` {
		t.Fatalf("clean %v dropped %v", clean, dropped)
	}
}

func TestAppendSystemPromptJSON(t *testing.T) {
	const add = "Answer in English."
	cases := []struct {
		proto      ir.Protocol
		in, expect string
	}{
		{ir.ProtoOpenAIChat, `{"model":"m","messages":[{"role":"system","content":"S"},{"role":"user","content":"u"}]}`,
			`{"model":"m","messages":[{"role":"system","content":"S"},{"role":"system","content":"Answer in English."},{"role":"user","content":"u"}]}`},
		{ir.ProtoOpenAIChat, `{"messages":[{"role":"user","content":"u"}]}`,
			`{"messages":[{"role":"system","content":"Answer in English."},{"role":"user","content":"u"}]}`},
		{ir.ProtoAnthropic, `{"system":"S","messages":[]}`, `{"system":"S\n\nAnswer in English.","messages":[]}`},
		{ir.ProtoAnthropic, `{"system":[{"type":"text","text":"S","cache_control":{"type":"ephemeral"}}],"messages":[]}`,
			`{"system":[{"type":"text","text":"S","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Answer in English."}],"messages":[]}`},
		{ir.ProtoAnthropic, `{"messages":[]}`, `{"messages":[],"system":"Answer in English."}`},
		{ir.ProtoOpenAIResp, `{"instructions":"I","input":"x"}`, `{"instructions":"I\n\nAnswer in English.","input":"x"}`},
		{ir.ProtoOpenAIResp, `{"input":"x"}`, `{"input":"x","instructions":"Answer in English."}`},
		{ir.ProtoGemini, `{"contents":[]}`, `{"contents":[],"systemInstruction":{"parts":[{"text":"Answer in English."}]}}`},
		{ir.ProtoGemini, `{"systemInstruction":{"parts":[{"text":"S"}]},"contents":[]}`,
			`{"systemInstruction":{"parts":[{"text":"S"},{"text":"Answer in English."}]},"contents":[]}`},
	}
	for _, c := range cases {
		out, err := AppendSystemPromptJSON(c.proto, []byte(c.in), add)
		if err != nil {
			t.Fatalf("%s %s: %v", c.proto, c.in, err)
		}
		if string(out) != c.expect {
			t.Errorf("%s\n in  %s\n got %s\nwant %s", c.proto, c.in, out, c.expect)
		}
	}
}
