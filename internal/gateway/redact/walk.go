package redact

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// walk calls fn with every string value in the JSON document b — its path (keys and indexes
// joined by dots) and the key it is under (an array's strings are under the array's key) — and
// writes what fn returns in its place. Everything else is kept byte for byte, and b comes back
// as it is when nothing changed; when b isn't JSON, ok is false.
func walk(b []byte, fn func(path, key, s string) string) (out []byte, ok bool) {
	w := walker{in: b, fn: fn}
	i, good := w.value(w.ws(0), "", "")
	if !good || w.ws(i) != len(b) {
		return b, false
	}
	if w.last == 0 {
		return b, true
	}
	w.out.Write(b[w.last:])
	return w.out.Bytes(), true
}

type walker struct {
	in   []byte
	out  bytes.Buffer
	last int
	fn   func(path, key, s string) string
}

func (w *walker) ws(i int) int {
	for i < len(w.in) && (w.in[i] == ' ' || w.in[i] == '\t' || w.in[i] == '\n' || w.in[i] == '\r') {
		i++
	}
	return i
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

// str is the end of the string literal that starts at i.
func (w *walker) str(i int) (end int, escaped, ok bool) {
	for j := i + 1; j < len(w.in); j++ {
		switch w.in[j] {
		case '\\':
			escaped = true
			j++
		case '"':
			return j + 1, escaped, true
		}
	}
	return 0, false, false
}

func (w *walker) value(i int, path, key string) (int, bool) {
	if i >= len(w.in) {
		return i, false
	}
	switch w.in[i] {
	case '{':
		i = w.ws(i + 1)
		if i < len(w.in) && w.in[i] == '}' {
			return i + 1, true
		}
		for {
			if i >= len(w.in) || w.in[i] != '"' {
				return i, false
			}
			end, esc, ok := w.str(i)
			if !ok {
				return i, false
			}
			k := string(w.in[i+1 : end-1])
			if esc && json.Unmarshal(w.in[i:end], &k) != nil {
				return i, false
			}
			i = w.ws(end)
			if i >= len(w.in) || w.in[i] != ':' {
				return i, false
			}
			if i, ok = w.value(w.ws(i+1), join(path, k), k); !ok {
				return i, false
			}
			i = w.ws(i)
			if i < len(w.in) && w.in[i] == ',' {
				i = w.ws(i + 1)
				continue
			}
			if i < len(w.in) && w.in[i] == '}' {
				return i + 1, true
			}
			return i, false
		}
	case '[':
		i = w.ws(i + 1)
		if i < len(w.in) && w.in[i] == ']' {
			return i + 1, true
		}
		for n := 0; ; n++ {
			var ok bool
			if i, ok = w.value(i, join(path, strconv.Itoa(n)), key); !ok {
				return i, false
			}
			i = w.ws(i)
			if i < len(w.in) && w.in[i] == ',' {
				i = w.ws(i + 1)
				continue
			}
			if i < len(w.in) && w.in[i] == ']' {
				return i + 1, true
			}
			return i, false
		}
	case '"':
		end, esc, ok := w.str(i)
		if !ok {
			return i, false
		}
		s := string(w.in[i+1 : end-1])
		if esc && json.Unmarshal(w.in[i:end], &s) != nil {
			return i, false
		}
		if t := w.fn(path, key, s); t != s {
			if w.out.Cap() == 0 {
				w.out.Grow(len(w.in) + len(w.in)/16 + 64)
			}
			w.out.Write(w.in[w.last:i])
			w.out.WriteByte('"')
			w.out.WriteString(jsonEscape(t))
			w.out.WriteByte('"')
			w.last = end
		}
		return end, true
	default:
		j := i
		for j < len(w.in) && !strings.ContainsRune(",]} \t\r\n", rune(w.in[j])) {
			j++
		}
		if j == i {
			return i, false
		}
		return j, true
	}
}

// jsonEscape is s as it goes between the quotes of a JSON string.
func jsonEscape(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	out := b.Bytes()
	return string(out[1 : len(out)-2]) // the quotes and the newline
}

// typeOf is the top-level "type" of a JSON object, wherever it is among its keys.
func typeOf(data []byte) string {
	typ := ""
	walk(data, func(path, _, s string) string {
		if path == "type" {
			typ = s
		}
		return s
	})
	return typ
}

// asJSON says a string under key holds JSON text of its own, so a value put back in it goes in
// escaped: a tool call's arguments, as a whole or streamed in pieces.
func asJSON(key, eventType string) bool {
	return key == "arguments" || key == "partial_json" || key == "delta" && strings.Contains(eventType, "arguments")
}

// kept are the keys whose strings are left alone: ids, names and kinds the vendor needs as they
// are, and what is sealed or encoded (a signature, encrypted reasoning, an image's data).
var kept = map[string]bool{
	"signature": true, "encrypted_content": true, "data": true, "thoughtSignature": true, "thought_signature": true,
	"model": true, "id": true, "type": true, "role": true, "name": true, "namespace": true,
	"server_label": true, "server_name": true, "prompt_cache_key": true, "media_type": true, "mime_type": true,
	"mimeType": true, "url": true, "image_url": true, "reasoning_effort": true, "effort": true,
	"stop_reason": true, "finish_reason": true, "status": true, "object": true, "event": true,
}

// isID says a key holds an identifier the vendor matches against another one.
func isID(key string) bool {
	return key == "id" || key == "ids" || strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_ids") ||
		strings.HasSuffix(key, "Id") || strings.HasSuffix(key, "Ids")
}

// signatureKeys are the keys of what a vendor seals the content beside it with.
var signatureKeys = []string{"signature", "thoughtSignature", "thought_signature", "encrypted_content"}

// defKeys are the request's own top-level keys whose values define structure for the vendor to
// read (tools, schemas, settings): only a "description" in them is masked.
var defKeys = []string{
	"tools", "tool_choice", "functions", "function_call", "toolConfig", "tool_config",
	"response_format", "text", "reasoning", "include", "output_format", "output_config",
	"generationConfig", "generation_config", "stream_options", "client_metadata", "thinking",
}

// scan finds three kinds of places in a request body: signed (blocks a vendor sealed with a
// signature), args (a tool call's arguments or a tool's structured result, whose keys are the
// tool's own) and defs (what the client defines for the vendor rather than says to the model).
func scan(body []byte) (signed, args, defs []string) {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil, nil, nil
	}
	defs = definitions(v)
	var visit func(path, key string, v any)
	visit = func(path, key string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if sealed(x) {
				signed = append(signed, path)
				return
			}
			t, _ := x["type"].(string)
			for k, c := range x {
				if toolArgs(t, key, k) || k == "response" && (key == "functionResponse" || key == "function_response") {
					args = append(args, join(path, k))
					continue
				}
				visit(join(path, k), k, c)
			}
		case []any:
			for i, c := range x {
				visit(join(path, strconv.Itoa(i)), key, c)
			}
		}
	}
	visit("", "", v)
	return signed, args, defs
}

// toolArgs says the value under k, in an object of type t under key, is what the model wrote for
// a tool to run: Anthropic's *_tool_use input, a Responses *_call's arguments/input/action,
// a Chat Completions tool call's function.arguments, Gemini's functionCall args.
func toolArgs(t, key, k string) bool {
	switch {
	case strings.HasSuffix(t, "tool_use"):
		return k == "input"
	case strings.HasSuffix(t, "_call"):
		return k == "arguments" || k == "input" || k == "action" || k == "operation"
	case key == "function" || key == "functionCall" || key == "function_call":
		return k == "arguments" || k == "args"
	}
	return false
}

// definitions are the paths of the top-level defKeys present in a request.
func definitions(v any) []string {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	var defs []string
	for _, k := range defKeys {
		val, ok := m[k]
		if !ok {
			continue
		}
		if k == "text" {
			// Responses' text is the output's settings; a prompt's text is a string
			if _, ok := val.(map[string]any); !ok {
				continue
			}
		}
		defs = append(defs, k)
	}
	return defs
}

// sealed says x is a block the vendor signed: a part with Gemini's thought signature, or a
// thinking or reasoning block with Anthropic's signature or Responses' encrypted content.
func sealed(x map[string]any) bool {
	t, _ := x["type"].(string)
	for _, k := range signatureKeys {
		if s, _ := x[k].(string); s == "" {
			continue
		}
		if k == "thoughtSignature" || k == "thought_signature" ||
			t == "thinking" || t == "redacted_thinking" || t == "reasoning" || strings.HasPrefix(t, "reasoning.") {
			return true
		}
	}
	return false
}

// under says path is in one of the objects at roots.
func under(path string, roots []string) bool {
	for _, r := range roots {
		if r == "" || path == r || strings.HasPrefix(path, r+".") {
			return true
		}
	}
	return false
}
