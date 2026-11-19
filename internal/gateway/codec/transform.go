package codec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

// ExtraBodyReserved are the top-level fields a provider's extra_body can't set: the model and
// stream flag, which the gateway decides for routing and reading the reply, and the
// conversation itself (messages, input, contents, system prompt, instructions, tools).
var ExtraBodyReserved = []string{"model", "stream", "messages", "input", "contents", "system",
	"systemInstruction", "system_instruction", "instructions", "tools"}

// ExtraBodyName is a field's name without its "!" and whether it is forced.
func ExtraBodyName(k string) (string, bool) {
	n, forced := strings.CutPrefix(strings.TrimSpace(k), "!")
	return strings.TrimSpace(n), forced
}

// ExtraBodyProblem says what is wrong with an extra_body ("" when nothing is).
func ExtraBodyProblem(e map[string]json.RawMessage) string {
	seen := map[string]bool{}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n, _ := ExtraBodyName(k)
		switch {
		case n == "":
			return "an extra_body field needs a name"
		case isReserved(n):
			return fmt.Sprintf("extra_body can't set %q: the gateway sets the model, stream and the conversation itself", n)
		case seen[n]:
			return fmt.Sprintf("extra_body names %q twice", n)
		case !json.Valid(e[k]):
			return fmt.Sprintf("extra_body's %q is not JSON", n)
		}
		seen[n] = true
	}
	return ""
}

// CleanExtraBody drops what can't be sent (ExtraBodyProblem's faults, for a file written by
// hand) and returns the names it dropped; nil when nothing is left.
func CleanExtraBody(e map[string]json.RawMessage) (map[string]json.RawMessage, []string) {
	out := map[string]json.RawMessage{}
	seen := map[string]bool{}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var dropped []string
	for _, k := range keys {
		n, forced := ExtraBodyName(k)
		if n == "" || isReserved(n) || seen[n] || !json.Valid(e[k]) {
			dropped = append(dropped, k)
			continue
		}
		seen[n] = true
		var buf bytes.Buffer
		_ = json.Compact(&buf, e[k])
		if forced {
			n = "!" + n
		}
		out[n] = json.RawMessage(buf.Bytes())
	}
	if len(out) == 0 {
		return nil, dropped
	}
	return out, dropped
}

func isReserved(n string) bool {
	for _, r := range ExtraBodyReserved {
		if r == n {
			return true
		}
	}
	return false
}

// MergeExtraBodyJSON merges a provider's extra fields into the top level of an outgoing body: a
// field the body has is kept unless its name is written "!name" (forced). Reserved names are
// never set. Everything the merge does not touch stays byte for byte, so a cached prefix keeps
// matching. A body that isn't a JSON object comes back as it is.
func MergeExtraBodyJSON(body []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return body, nil
	}
	obj, err := scanTopLevel(body)
	if err != nil || obj == nil {
		return body, err
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { // by name, "!" aside
		a, _ := ExtraBodyName(keys[i])
		b, _ := ExtraBodyName(keys[j])
		return a < b || a == b && keys[i] < keys[j]
	})
	for _, k := range keys {
		n, forced := ExtraBodyName(k)
		if n == "" || isReserved(n) || !json.Valid(extra[k]) {
			continue
		}
		if _, has := obj.ranges[n]; has && !forced {
			continue
		}
		var v bytes.Buffer
		if err := json.Compact(&v, extra[k]); err != nil {
			continue
		}
		body = obj.set(body, n, v.Bytes())
		if obj, err = scanTopLevel(body); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// AppendSystemPromptJSON appends text to the system prompt of a request body in proto: after the
// leading system/developer messages (OpenAI Chat), as a last system block (Anthropic), after the
// instructions (Responses), or as a last systemInstruction part (Gemini).
func AppendSystemPromptJSON(proto ir.Protocol, body []byte, text string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return body, nil
	}
	obj, err := scanTopLevel(body)
	if err != nil || obj == nil {
		return body, err
	}
	quoted, _ := json.Marshal(text)
	switch proto {
	case ir.ProtoOpenAIChat:
		r, ok := obj.ranges["messages"]
		if !ok {
			return body, nil
		}
		var msgs []json.RawMessage
		if err := json.Unmarshal(body[r[0]:r[1]], &msgs); err != nil {
			return nil, err
		}
		at := 0
		for at < len(msgs) {
			var probe struct{ Role string }
			if json.Unmarshal(msgs[at], &probe) != nil || probe.Role != "system" && probe.Role != "developer" {
				break
			}
			at++
		}
		msg := json.RawMessage(`{"role":"system","content":` + string(quoted) + `}`)
		msgs = append(msgs[:at], append([]json.RawMessage{msg}, msgs[at:]...)...)
		return obj.set(body, "messages", joinRaw(msgs)), nil
	case ir.ProtoAnthropic:
		block := `{"type":"text","text":` + string(quoted) + `}`
		r, ok := obj.ranges["system"]
		if !ok || string(body[r[0]:r[1]]) == "null" {
			return obj.set(body, "system", quoted), nil
		}
		cur := body[r[0]:r[1]]
		if cur[0] == '"' {
			var s string
			if err := json.Unmarshal(cur, &s); err != nil {
				return nil, err
			}
			joined, _ := json.Marshal(s + "\n\n" + text)
			return obj.set(body, "system", joined), nil
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal(cur, &blocks); err != nil {
			return nil, err
		}
		return obj.set(body, "system", joinRaw(append(blocks, json.RawMessage(block)))), nil
	case ir.ProtoOpenAIResp:
		r, ok := obj.ranges["instructions"]
		if !ok || string(body[r[0]:r[1]]) == "null" {
			return obj.set(body, "instructions", quoted), nil
		}
		var s string
		if err := json.Unmarshal(body[r[0]:r[1]], &s); err != nil {
			return nil, err
		}
		if strings.TrimSpace(s) == "" {
			return obj.set(body, "instructions", quoted), nil
		}
		joined, _ := json.Marshal(s + "\n\n" + text)
		return obj.set(body, "instructions", joined), nil
	case ir.ProtoGemini:
		key := "systemInstruction"
		r, ok := obj.ranges[key]
		if !ok {
			if r2, ok2 := obj.ranges["system_instruction"]; ok2 {
				key, r, ok = "system_instruction", r2, true
			}
		}
		part := json.RawMessage(`{"text":` + string(quoted) + `}`)
		if !ok || string(body[r[0]:r[1]]) == "null" {
			return obj.set(body, key, []byte(`{"parts":[`+string(part)+`]}`)), nil
		}
		var si map[string]json.RawMessage
		if err := json.Unmarshal(body[r[0]:r[1]], &si); err != nil {
			return nil, err
		}
		var parts []json.RawMessage
		if raw, ok := si["parts"]; ok {
			if err := json.Unmarshal(raw, &parts); err != nil {
				return nil, err
			}
		}
		si["parts"] = joinRaw(append(parts, part))
		out, err := json.Marshal(si)
		if err != nil {
			return nil, err
		}
		return obj.set(body, key, out), nil
	}
	return body, nil
}

func joinRaw(items []json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// topLevel is where each top-level field's value of an object sits, and its closing brace.
type topLevel struct {
	ranges map[string][2]int
	close  int
	empty  bool
}

// scanTopLevel reads the top level of a JSON object; nil (and no error) for another JSON value.
func scanTopLevel(body []byte) (*topLevel, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil
	}
	obj := &topLevel{ranges: map[string][2]int{}, empty: true}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		end := int(dec.InputOffset())
		obj.ranges[k] = [2]int{end - len(raw), end}
		obj.empty = false
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	obj.close = int(dec.InputOffset()) - 1
	return obj, nil
}

// set replaces the value of key, or adds key before the closing brace.
func (o *topLevel) set(body []byte, key string, value []byte) []byte {
	if r, ok := o.ranges[key]; ok {
		out := make([]byte, 0, len(body)-(r[1]-r[0])+len(value))
		out = append(out, body[:r[0]]...)
		out = append(out, value...)
		return append(out, body[r[1]:]...)
	}
	name, _ := json.Marshal(key)
	field := string(name) + ":" + string(value)
	if !o.empty {
		field = "," + field
	}
	// insert right after the last value (before any whitespace that precedes the brace)
	at := o.close
	for at > 0 && (body[at-1] == ' ' || body[at-1] == '\n' || body[at-1] == '\r' || body[at-1] == '\t') {
		at--
	}
	out := make([]byte, 0, len(body)+len(field))
	out = append(out, body[:at]...)
	out = append(out, field...)
	return append(out, body[at:]...)
}
