package codec

import (
	"strings"
	"testing"
)

func TestApplyDeepSeekChatJSON(t *testing.T) {
	in := `{"model":"deepseek-reasoner","max_completion_tokens":32000,"messages":[
		{"role":"developer","content":"be brief"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":""},
		{"role":"assistant","content":null,"reasoning":"think","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"ok"}]}`
	out, err := ApplyDeepSeekChatJSON([]byte(in), true)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"max_tokens":32000`, `"role":"system"`, `"reasoning_content":"think"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	for _, bad := range []string{"max_completion_tokens", `"developer"`, `"reasoning":"think"`, `"content":""`} {
		if strings.Contains(s, bad) {
			t.Errorf("unexpected %s in %s", bad, s)
		}
	}
	if !IsDeepSeekTarget("openrouter", "openrouter", "deepseek/deepseek-r1") || IsDeepSeekTarget("openai", "openai", "gpt-5") {
		t.Fatal("deepseek scoping wrong")
	}
}

func TestApplyDeepSeekAnthropicJSON(t *testing.T) {
	in := `{"model":"deepseek-reasoner","thinking":{"type":"enabled","budget_tokens":2048},"messages":[
		{"role":"user","content":"go"},
		{"role":"assistant","content":[{"type":"redacted_thinking","data":"x"},{"type":"tool_use","id":"t1","name":"f","input":{}},{"type":"text","text":"after"}]}]}`
	out, err := ApplyDeepSeekAnthropicJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "redacted_thinking") || strings.Index(s, `"after"`) > strings.Index(s, `"tool_use"`) || !strings.Contains(s, `"type":"thinking"`) {
		t.Fatalf("unexpected patch result %s", s)
	}
}

func TestSanitizeClaudeThinkingJSON(t *testing.T) {
	in := `{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":2048},"messages":[
		{"role":"user","content":"go"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"0b3c9a4e-1f2d-4c5b-8a6e-7d8f9a0b1c2d"},{"type":"tool_use","id":"t1","name":"f","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`
	out, err := SanitizeClaudeThinkingJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); strings.Contains(s, "0b3c9a4e") || strings.Contains(s, `"budget_tokens"`) {
		t.Fatalf("expected foreign thinking and the thinking param removed: %s", s)
	}
}
