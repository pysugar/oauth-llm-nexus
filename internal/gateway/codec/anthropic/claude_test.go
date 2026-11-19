package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

const longSig = "EqQBCkYIBxgCKkBq3J9sRealisticLongClaudeSignatureBase64PayloadForTests=="

func build(t *testing.T, req *ir.Request, model string) map[string]any {
	t.Helper()
	raw, ge := New().BuildRequest(req, model, true, codec.Quirks{})
	if ge != nil {
		t.Fatalf("build: %v", ge)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func toolLoop(thought ir.Part) *ir.Request {
	asst := []ir.Part{{Kind: ir.KindToolCall, CallID: "call_abc", Name: "ls", Args: `{}`}}
	if thought.Kind != "" {
		asst = append([]ir.Part{thought}, asst...)
	}
	return &ir.Request{
		Model:     "x",
		MaxTokens: 4096,
		Reasoning: ir.Reasoning{Effort: "high"},
		Messages: []ir.Message{
			{Role: ir.RoleUser, Parts: []ir.Part{{Kind: ir.KindText, Text: "list"}}},
			{Role: ir.RoleAssistant, Parts: asst},
			{Role: ir.RoleTool, Parts: []ir.Part{{Kind: ir.KindToolResult, CallID: "call_abc", Result: "a.txt"}}},
		},
	}
}

func TestAdaptiveThinkingForNewClaude(t *testing.T) {
	out := build(t, &ir.Request{Model: "x", Reasoning: ir.Reasoning{Effort: "high"},
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{{Kind: ir.KindText, Text: "hi"}}}}}, "claude-opus-5-5")
	th, _ := json.Marshal(out["thinking"])
	oc, _ := json.Marshal(out["output_config"])
	if string(th) != `{"display":"summarized","type":"adaptive"}` || string(oc) != `{"effort":"high"}` {
		t.Fatalf("unexpected adaptive thinking %s %s", th, oc)
	}
	out = build(t, &ir.Request{Model: "x", Reasoning: ir.Reasoning{Effort: "high"},
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{{Kind: ir.KindText, Text: "hi"}}}}}, "claude-sonnet-4-5")
	if m, _ := out["thinking"].(map[string]any); m["type"] != "enabled" || out["output_config"] != nil {
		t.Fatalf("older Claude must keep budget thinking, got %v", out["thinking"])
	}
	if AdaptiveOnly("claude-3-5-sonnet-20241022") || !AdaptiveOnly("claude-sonnet-4-6") || !AdaptiveOnly("anthropic.claude-opus-5-5-20260901-v1:0") {
		t.Fatal("adaptive model detection is wrong")
	}
}

func TestThinkingRestoredByCallIDAndForeignSignaturesDropped(t *testing.T) {
	// No thinking in the client history and nothing cached: thinking is turned off, not a 400.
	out := build(t, toolLoop(ir.Part{}), "claude-sonnet-4-5")
	if out["thinking"] != nil {
		t.Fatalf("expected thinking omitted when tool turn lost it, got %v", out["thinking"])
	}
	// A foreign (UUID) signature is dropped for Claude.
	out = build(t, toolLoop(ir.Part{Kind: ir.KindThought, Text: "hm", Signature: "0b3c9a4e-1f2d-4c5b-8a6e-7d8f9a0b1c2d"}), "claude-sonnet-4-5")
	if strings.Contains(mustJSON(out["messages"]), "0b3c9a4e") {
		t.Fatal("foreign signature must not reach Claude")
	}
	// Saved by call id from an earlier Claude reply: put back in front of the tool turn.
	ir.DefaultSigStore.SaveCallThinking([]string{"call_abc"}, []ir.ThinkingBlock{{Text: "plan", Signature: longSig}})
	out = build(t, toolLoop(ir.Part{}), "claude-sonnet-4-5")
	asst := out["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if first := asst[0].(map[string]any); first["type"] != "thinking" || first["signature"] != longSig || out["thinking"] == nil {
		t.Fatalf("expected restored thinking and thinking enabled, got %v / %v", asst, out["thinking"])
	}
}

func TestMissingToolResultIsAnError(t *testing.T) {
	req := toolLoop(ir.Part{})
	req.Messages = req.Messages[:2] // the result never came back
	req.Messages = append(req.Messages, ir.Message{Role: ir.RoleUser, Parts: []ir.Part{{Kind: ir.KindText, Text: "go on"}}})
	got := mustJSON(build(t, req, "claude-sonnet-4-5")["messages"])
	if !strings.Contains(got, ir.MissingToolResult) || !strings.Contains(got, `"is_error":true`) || strings.Contains(got, `status":"completed`) {
		t.Fatalf("expected an is_error placeholder, got %s", got)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
