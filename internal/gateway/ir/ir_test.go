package ir

import "testing"

func TestCollectorAggregatesEvents(t *testing.T) {
	c := NewCollector()
	c.Add(Event{Kind: EventStart, ResponseID: "resp_1", Model: "gpt-4o", Usage: Usage{InputTokens: 100}})
	c.Add(Event{Kind: EventThought, Text: "step 1 "})
	c.Add(Event{Kind: EventThought, Text: "step 2"})
	c.Add(Event{Kind: EventThoughtSignature, Signature: "sig_abc"})
	c.Add(Event{Kind: EventText, Text: "Hello "})
	c.Add(Event{Kind: EventText, Text: "world"})
	c.Add(Event{Kind: EventToolCallStart, CallID: "call_1", Name: "get_weather"})
	c.Add(Event{Kind: EventToolCallDelta, CallID: "call_1", Args: `{"city":`})
	c.Add(Event{Kind: EventToolCallDelta, CallID: "call_1", Args: `"NYC"}`})
	c.Add(Event{Kind: EventToolCallDone, CallID: "call_1"})
	c.Add(Event{Kind: EventFinish, FinishReason: FinishToolCalls, Usage: Usage{OutputTokens: 25, ReasoningTokens: 10}})

	res := c.Build()
	if res.ID != "resp_1" || res.Model != "gpt-4o" {
		t.Fatalf("unexpected header: %+v", res)
	}
	if res.FinishReason != FinishToolCalls {
		t.Fatalf("expected finish %q, got %q", FinishToolCalls, res.FinishReason)
	}
	if res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 25 || res.Usage.ReasoningTokens != 10 {
		t.Fatalf("unexpected usage: %+v", res.Usage)
	}
	if len(res.Content) != 3 {
		t.Fatalf("expected 3 content parts, got %d: %+v", len(res.Content), res.Content)
	}
	if res.Content[0].Kind != KindThought || res.Content[0].Text != "step 1 step 2" || res.Content[0].Signature != "sig_abc" {
		t.Fatalf("unexpected thought part: %+v", res.Content[0])
	}
	if res.Content[1].Kind != KindText || res.Content[1].Text != "Hello world" {
		t.Fatalf("unexpected text part: %+v", res.Content[1])
	}
	if res.Content[2].Kind != KindToolCall || res.Content[2].CallID != "call_1" || res.Content[2].Args != `{"city":"NYC"}` {
		t.Fatalf("unexpected tool part: %+v", res.Content[2])
	}
}

func TestSigStoreAndToolIDHelpers(t *testing.T) {
	store := NewSigStore(2)
	store.SaveCall("call:one", "sig_1")
	store.SaveCall("call_2", "sig_2")

	if got := store.LookupCall("call:one"); got != "sig_1" {
		t.Fatalf("expected sig_1, got %q", got)
	}
	// Sanitized form must also resolve.
	san := SanitizeToolID("call:one")
	if san == "call:one" {
		t.Fatalf("expected sanitized ID to differ")
	}
	if got := store.LookupCall(san); got != "sig_1" {
		t.Fatalf("expected sig_1 via sanitized ID %q, got %q", san, got)
	}
	if UnsanitizeToolID(san) != "call:one" {
		t.Fatalf("expected round-trip unsanitize, got %q", UnsanitizeToolID(san))
	}

	// Evicts oldest when exceeding maxItems=2.
	store.SaveCall("call_3", "sig_3")
	if got := store.LookupCall("call:one"); got != "" {
		t.Fatalf("expected oldest entry to be evicted, got %q", got)
	}
}

func TestEffortConversion(t *testing.T) {
	if got := NormalizeEffort("disabled"); got != "none" {
		t.Fatalf("expected none, got %q", got)
	}
	if got := EffortToBudget("high"); got != 16384 {
		t.Fatalf("expected 16384, got %d", got)
	}
	if got := BudgetToEffort(2048); got != "low" {
		t.Fatalf("expected low, got %q", got)
	}
}
