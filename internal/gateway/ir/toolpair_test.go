package ir

import "testing"

func TestNormalizeToolTurns(t *testing.T) {
	call := func(id, name string) Part { return Part{Kind: KindToolCall, CallID: id, Name: name} }
	res := func(id, r string) Part { return Part{Kind: KindToolResult, CallID: id, Result: r} }
	in := []Message{
		{Role: RoleUser, Parts: []Part{{Kind: KindText, Text: "go"}}},
		{Role: RoleAssistant, Parts: []Part{call("a", "ls"), call("b", "cat"), call("c", "rm")}},
		{Role: RoleSystem, Parts: []Part{{Kind: KindText, Text: "reminder"}}}, // between call and result
		{Role: RoleTool, Parts: []Part{res("b", "B")}},
		{Role: RoleTool, Parts: []Part{res("", "A")}},    // no id: answers the next unanswered call
		{Role: RoleTool, Parts: []Part{res("zzz", "Z")}}, // unknown call: becomes user text
		{Role: RoleAssistant, Parts: []Part{{Kind: KindText, Text: "done"}}},
	}
	out := NormalizeToolTurns(in)
	if len(out) != 6 || out[2].Role != RoleTool || out[3].Role != RoleUser || out[4].Role != RoleSystem {
		t.Fatalf("unexpected shape: %+v", out)
	}
	got := out[2].Parts
	if got[0].CallID != "a" || got[0].Result != "A" || got[1].Result != "B" || !got[2].IsError || got[2].Result != MissingToolResult {
		t.Fatalf("unexpected answers: %+v", got)
	}
	if out[3].Parts[0].Text != "[tool result]: Z" {
		t.Fatalf("unexpected orphan text: %+v", out[3])
	}
	if StableCallID(1, 0, "ls", "{}") != StableCallID(1, 0, "ls", "{}") || StableCallID(1, 0, "ls", "{}") == StableCallID(2, 0, "ls", "{}") {
		t.Fatal("StableCallID must be deterministic and position-dependent")
	}
}
