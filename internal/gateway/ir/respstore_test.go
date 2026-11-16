package ir

import (
	"testing"
	"time"
)

func TestResponseStoreSaveLookupTTLAndEviction(t *testing.T) {
	now := time.Unix(1000, 0)
	store := NewResponseStore(2, 10*time.Minute)
	store.nowFunc = func() time.Time { return now }

	store.Save("resp_1", []Message{{Role: RoleUser, Parts: []Part{{Kind: KindText, Text: "hi"}}}}, []Part{{Kind: KindText, Text: "hello"}})
	msgs, ok := store.Lookup("resp_1")
	if !ok || len(msgs) != 2 || msgs[1].Role != RoleAssistant || msgs[1].Parts[0].Text != "hello" {
		t.Fatalf("unexpected lookup for resp_1: ok=%v msgs=%+v", ok, msgs)
	}

	// Mutating returned slice must not mutate cached copy.
	msgs[0].Parts[0].Text = "mutated"
	msgs2, _ := store.Lookup("resp_1")
	if msgs2[0].Parts[0].Text != "hi" {
		t.Fatalf("expected deep-copied messages, got %q", msgs2[0].Parts[0].Text)
	}

	// Capacity eviction keeps most recent 2 keys.
	store.Save("resp_2", msgs2, []Part{{Kind: KindText, Text: "second"}})
	store.Save("resp_3", msgs2, []Part{{Kind: KindText, Text: "third"}})
	if _, ok := store.Lookup("resp_1"); ok {
		t.Fatalf("expected resp_1 evicted after cap=2")
	}
	if _, ok := store.Lookup("resp_2"); !ok {
		t.Fatalf("expected resp_2 retained")
	}

	// TTL expiry removes expired entry.
	now = now.Add(11 * time.Minute)
	if _, ok := store.Lookup("resp_2"); ok {
		t.Fatalf("expected resp_2 expired after TTL")
	}
}
