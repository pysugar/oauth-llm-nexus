package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// MissingToolResult is the placeholder for a tool call whose result never came back. It is sent
// with IsError so the model does not take the call as having succeeded.
const MissingToolResult = "[tool result missing: the call was interrupted before it returned]"

// NormalizeToolTurns makes every assistant tool call answered exactly once, right after it, as
// all upstream APIs require:
//   - results anywhere before the next assistant turn are pulled up behind their calls (a result
//     without a call id answers the next unanswered call);
//   - system/user messages that sit between a call and its results move after the results;
//   - a call with no result gets an IsError MissingToolResult placeholder;
//   - results whose call is unknown become user text instead of being silently dropped.
func NormalizeToolTurns(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs)+2)
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role == RoleTool {
			// A result with no preceding assistant call in this window.
			out = appendOrphans(out, toolResultParts(m))
			continue
		}
		calls := toolCallParts(m)
		if m.Role != RoleAssistant || len(calls) == 0 {
			out = append(out, m)
			continue
		}
		out = append(out, m)

		pending := make(map[string]bool, len(calls))
		for _, c := range calls {
			pending[c.CallID] = true
		}
		results := make(map[string]Part, len(calls))
		var unnamed, orphans []Part
		var deferred []Message
		j := i + 1
		for ; j < len(msgs) && msgs[j].Role != RoleAssistant; j++ {
			if msgs[j].Role != RoleTool {
				deferred = append(deferred, msgs[j])
				continue
			}
			for _, p := range toolResultParts(msgs[j]) {
				switch {
				case p.CallID == "":
					unnamed = append(unnamed, p)
				case pending[p.CallID]:
					if _, dup := results[p.CallID]; !dup {
						results[p.CallID] = p
					}
				default:
					orphans = append(orphans, p)
				}
			}
		}

		answers := make([]Part, 0, len(calls))
		for _, c := range calls {
			if r, ok := results[c.CallID]; ok {
				answers = append(answers, r)
				continue
			}
			if len(unnamed) > 0 {
				r := unnamed[0]
				unnamed = unnamed[1:]
				r.CallID = c.CallID
				if r.Name == "" {
					r.Name = c.Name
				}
				answers = append(answers, r)
				continue
			}
			answers = append(answers, Part{Kind: KindToolResult, CallID: c.CallID, Name: c.Name, Result: MissingToolResult, IsError: true})
		}
		out = append(out, Message{Role: RoleTool, Parts: answers})
		out = appendOrphans(out, append(orphans, unnamed...))
		out = append(out, deferred...)
		i = j - 1
	}
	return out
}

func toolCallParts(m Message) []Part {
	var calls []Part
	for _, p := range m.Parts {
		if p.Kind == KindToolCall && p.CallID != "" {
			calls = append(calls, p)
		}
	}
	return calls
}

func toolResultParts(m Message) []Part {
	var rs []Part
	for _, p := range m.Parts {
		if p.Kind == KindToolResult {
			rs = append(rs, p)
		}
	}
	return rs
}

func appendOrphans(out []Message, orphans []Part) []Message {
	if len(orphans) == 0 {
		return out
	}
	parts := make([]Part, 0, len(orphans))
	for _, p := range orphans {
		label := "[tool result"
		if p.Name != "" {
			label += " from " + p.Name
		}
		label += "]: "
		parts = append(parts, Part{Kind: KindText, Text: label + p.Result})
		for _, rp := range p.ResultParts {
			parts = append(parts, rp)
		}
	}
	return append(out, Message{Role: RoleUser, Parts: parts})
}

// StableCallID derives a deterministic tool-call id for histories that carry none (Gemini, legacy
// function_call), so a resent conversation yields identical bytes and the prompt cache holds.
func StableCallID(msgIdx, partIdx int, name, args string) string {
	h := sha256.New()
	h.Write([]byte(strconv.Itoa(msgIdx) + "\x00" + strconv.Itoa(partIdx) + "\x00" + name + "\x00" + args))
	return "call_" + hex.EncodeToString(h.Sum(nil))[:24]
}
