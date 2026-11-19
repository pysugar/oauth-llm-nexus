package codec

import (
	"bytes"
	"encoding/json"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"regexp"
	"strings"
)

// IsDeepSeekTarget scopes DeepSeek rules to DeepSeek: its own API, or a DeepSeek model served by
// a relay (OpenRouter, NVIDIA, ...).
func IsDeepSeekTarget(preset, providerID, model string) bool {
	return preset == "deepseek" || providerID == "deepseek" || strings.Contains(strings.ToLower(model), "deepseek")
}

// ApplyDeepSeekChatJSON patches an OpenAI Chat body passed through to a DeepSeek model:
//   - max_completion_tokens is ignored by DeepSeek (replies stop at its 8K default): use max_tokens;
//   - the "developer" role is a 422: send it as "system";
//   - with echoReasoning (DeepSeek's own API), an assistant tool-call turn must carry
//     reasoning_content (OpenAI SDKs drop it); never send it beside "reasoning";
//   - an assistant message with neither content nor tool calls is a 400: drop it.
func ApplyDeepSeekChatJSON(body []byte, echoReasoning bool) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	changed := false
	if mct, ok := obj["max_completion_tokens"]; ok {
		if _, has := obj["max_tokens"]; !has {
			obj["max_tokens"] = mct
		}
		delete(obj, "max_completion_tokens")
		changed = true
	}
	var msgs []map[string]json.RawMessage
	if raw, ok := obj["messages"]; ok && json.Unmarshal(raw, &msgs) == nil {
		kept := msgs[:0]
		for _, m := range msgs {
			role := jsonString(m["role"])
			if role == "developer" {
				m["role"] = json.RawMessage(`"system"`)
				changed = true
			}
			if role == "assistant" {
				hasCalls := nonEmptyJSON(m["tool_calls"])
				if !hasCalls && !nonEmptyContent(m["content"]) {
					changed = true
					continue
				}
				if echoReasoning && hasCalls {
					if _, ok := m["reasoning_content"]; !ok {
						rc := jsonString(m["reasoning"])
						if rc == "" {
							rc = " "
						}
						b, _ := json.Marshal(rc)
						m["reasoning_content"] = b
						changed = true
					}
				}
				if _, ok := m["reasoning_content"]; ok {
					if _, both := m["reasoning"]; both {
						delete(m, "reasoning")
						changed = true
					}
				}
			}
			kept = append(kept, m)
		}
		if changed {
			b, _ := json.Marshal(kept)
			obj["messages"] = b
		}
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(obj)
}

// ApplyDeepSeekAnthropicJSON patches an Anthropic Messages body for DeepSeek's Anthropic endpoint:
// text written after a tool_use goes before it (DeepSeek otherwise takes the call as answered),
// redacted_thinking (a 422 there) is dropped, and with thinking on, a tool turn without thinking
// gets an empty thinking block, which DeepSeek requires.
func ApplyDeepSeekAnthropicJSON(body []byte) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	thinkingOn := thinkingEnabledJSON(obj["thinking"])
	msgs, ok := decodeMessages(obj)
	if !ok {
		return body, nil
	}
	changed := false
	for _, m := range msgs {
		if jsonString(m["role"]) != "assistant" {
			continue
		}
		var blocks []map[string]any
		if json.Unmarshal(m["content"], &blocks) != nil || len(blocks) == 0 {
			continue
		}
		var thinking, text, calls, other []map[string]any
		seenCall := false
		moved := false
		for _, b := range blocks {
			switch b["type"] {
			case "redacted_thinking":
				moved = true
			case "thinking":
				thinking = append(thinking, b)
			case "tool_use":
				seenCall = true
				calls = append(calls, b)
			case "text":
				if seenCall {
					moved = true
				}
				text = append(text, b)
			default:
				other = append(other, b)
			}
		}
		if thinkingOn && len(calls) > 0 && len(thinking) == 0 {
			thinking = []map[string]any{{"type": "thinking", "thinking": "", "signature": ""}}
			moved = true
		}
		if !moved {
			continue
		}
		out := append(append(append(thinking, text...), other...), calls...)
		m["content"], _ = json.Marshal(out)
		changed = true
	}
	if !changed {
		return body, nil
	}
	obj["messages"], _ = json.Marshal(msgs)
	return json.Marshal(obj)
}

// SanitizeClaudeThinkingJSON drops thinking blocks Claude cannot have signed (after a client
// switched models mid-conversation), and turns thinking off when the ongoing tool turn is left
// without its thinking, which Claude would reject.
func SanitizeClaudeThinkingJSON(body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"thinking"`)) {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	msgs, ok := decodeMessages(obj)
	if !ok {
		return body, nil
	}
	changed := false
	lastToolTurnBare := false
	for _, m := range msgs {
		if jsonString(m["role"]) != "assistant" {
			continue
		}
		var blocks []map[string]any
		if json.Unmarshal(m["content"], &blocks) != nil {
			continue
		}
		kept := blocks[:0]
		hasTool := false
		for _, b := range blocks {
			if b["type"] == "thinking" {
				sig, _ := b["signature"].(string)
				if !ValidClaudeSignature(sig) {
					changed = true
					continue
				}
			}
			if b["type"] == "tool_use" {
				hasTool = true
			}
			kept = append(kept, b)
		}
		lastToolTurnBare = hasTool && (len(kept) == 0 || (kept[0]["type"] != "thinking" && kept[0]["type"] != "redacted_thinking"))
		if len(kept) != len(blocks) {
			m["content"], _ = json.Marshal(kept)
		}
	}
	if lastToolTurnBare && thinkingEnabledJSON(obj["thinking"]) {
		delete(obj, "thinking")
		changed = true
	}
	if !changed {
		return body, nil
	}
	obj["messages"], _ = json.Marshal(msgs)
	return json.Marshal(obj)
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidClaudeSignature rejects signatures Claude cannot have issued (empty, UUID-shaped DeepSeek
// placeholders, short tokens).
func ValidClaudeSignature(sig string) bool {
	return len(sig) >= 32 && !uuidRe.MatchString(sig)
}

func decodeMessages(obj map[string]json.RawMessage) ([]map[string]json.RawMessage, bool) {
	raw, ok := obj["messages"]
	if !ok {
		return nil, false
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return nil, false
	}
	return msgs, true
}

func thinkingEnabledJSON(raw json.RawMessage) bool {
	var th struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &th) != nil {
		return false
	}
	return th.Type == "enabled" || th.Type == "adaptive"
}

func jsonString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func nonEmptyJSON(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && !bytes.Equal(t, []byte("null")) && !bytes.Equal(t, []byte("[]"))
}

func nonEmptyContent(raw json.RawMessage) bool {
	if !nonEmptyJSON(raw) {
		return false
	}
	if s := jsonString(raw); strings.HasPrefix(strings.TrimSpace(string(raw)), `"`) {
		return strings.TrimSpace(s) != ""
	}
	return true
}

// DowngradeImageDetailJSON rewrites Responses input_image detail "original" to "high".
func DowngradeImageDetailJSON(body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"original"`)) {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(obj["input"], &items) != nil {
		return body, nil
	}
	changed := false
	for _, it := range items {
		var content []map[string]any
		if json.Unmarshal(it["content"], &content) != nil {
			continue
		}
		touched := false
		for _, c := range content {
			if c["type"] == "input_image" && c["detail"] == "original" {
				c["detail"] = "high"
				touched = true
			}
		}
		if touched {
			it["content"], _ = json.Marshal(content)
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	obj["input"], _ = json.Marshal(items)
	return json.Marshal(obj)
}

// SetReplyLimitJSON sets the reply limit of a passthrough body in proto's own field, replacing the
// field the client used (OpenAI Chat: max_completion_tokens or max_tokens; Responses:
// max_output_tokens; Gemini: generationConfig.maxOutputTokens; Anthropic: max_tokens).
func SetReplyLimitJSON(proto ir.Protocol, body []byte, limit int) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	n, _ := json.Marshal(limit)
	switch proto {
	case ir.ProtoOpenAIChat:
		field := "max_tokens"
		if _, ok := obj["max_completion_tokens"]; ok {
			field = "max_completion_tokens"
		}
		obj[field] = n
	case ir.ProtoOpenAIResp:
		obj["max_output_tokens"] = n
	case ir.ProtoAnthropic:
		obj["max_tokens"] = n
	case ir.ProtoGemini:
		var gc map[string]json.RawMessage
		if raw, ok := obj["generationConfig"]; !ok || json.Unmarshal(raw, &gc) != nil || gc == nil {
			gc = map[string]json.RawMessage{}
		}
		gc["maxOutputTokens"] = n
		obj["generationConfig"], _ = json.Marshal(gc)
	default:
		return body, nil
	}
	return json.Marshal(obj)
}
