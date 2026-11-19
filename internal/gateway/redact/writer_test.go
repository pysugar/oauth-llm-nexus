package redact

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// streamThrough writes events (each a complete SSE event, in pieces of size chunk) through a
// restoring Writer and returns what the client got.
func streamThrough(t *testing.T, m *Masker, events []string, chunk int) string {
	t.Helper()
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	rw := m.NewWriter(rec)
	rw.WriteHeader(200)
	all := strings.Join(events, "")
	for len(all) > 0 {
		n := min(chunk, len(all))
		if _, err := rw.Write([]byte(all[:n])); err != nil {
			t.Fatal(err)
		}
		all = all[n:]
	}
	rw.Finish()
	return rec.Body.String()
}

func dataEvent(name string, v any) string {
	b, _ := json.Marshal(v)
	if name != "" {
		return "event: " + name + "\ndata: " + string(b) + "\n\n"
	}
	return "data: " + string(b) + "\n\n"
}

func chatDelta(content string) string {
	return dataEvent("", map[string]any{"id": "c1", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}}}})
}

// collect concatenates the strings at path pick of every data event.
func collect(t *testing.T, out string, pick func(map[string]any) (string, bool)) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range strings.Split(out, "\n\n") {
		for _, line := range strings.Split(ev, "\n") {
			d, ok := strings.CutPrefix(line, "data: ")
			if !ok || !strings.HasPrefix(d, "{") {
				continue
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(d), &v); err != nil {
				t.Fatalf("bad event %q: %v", d, err)
			}
			if s, ok := pick(v); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

func chatText(v map[string]any) (string, bool) {
	ch, _ := v["choices"].([]any)
	if len(ch) == 0 {
		return "", false
	}
	d, _ := ch[0].(map[string]any)["delta"].(map[string]any)
	s, ok := d["content"].(string)
	return s, ok
}

func maskedEmail(t *testing.T, m *Masker, v string) string {
	t.Helper()
	if _, _, err := m.MaskText(v, Policy{}); err != nil {
		t.Fatal(err)
	}
	return m.Tag(KindEmail, v)
}

func TestStreamRestoresSplitPlaceholder(t *testing.T) {
	m := testMasker(t)
	p := maskedEmail(t, m, "jo@quill.dev")
	for cut := 1; cut < len(p); cut++ {
		events := []string{chatDelta("Write to "), chatDelta(p[:cut]), chatDelta(p[cut:] + " today"), "data: [DONE]\n\n"}
		for _, chunk := range []int{7, 4096} {
			out := streamThrough(t, m, events, chunk)
			if got := collect(t, out, chatText); got != "Write to jo@quill.dev today" {
				t.Fatalf("cut %d chunk %d: %q\n%s", cut, chunk, got, out)
			}
			if !strings.HasSuffix(out, "data: [DONE]\n\n") {
				t.Fatalf("DONE lost: %q", out)
			}
		}
	}
}

func TestStreamRestoresAcrossThreeEventsAndHeldTailAtEnd(t *testing.T) {
	m := testMasker(t)
	p := maskedEmail(t, m, "jo@quill.dev")
	out := streamThrough(t, m, []string{chatDelta("a " + p[:3]), chatDelta(p[3:9]), chatDelta(p[9:])}, 4096)
	if got := collect(t, out, chatText); got != "a jo@quill.dev" {
		t.Fatalf("got %q", got)
	}
	// a stream that ends on a held partial placeholder still delivers it, unchanged
	out = streamThrough(t, m, []string{chatDelta("tail {{EMA")}, 4096)
	if got := collect(t, out, chatText); got != "tail {{EMA" {
		t.Fatalf("got %q", got)
	}
}

func TestStreamAnthropicTextAndPing(t *testing.T) {
	m := testMasker(t)
	p := maskedEmail(t, m, "jo@quill.dev")
	td := func(s string) string {
		return dataEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": s}})
	}
	ping := "event: ping\ndata: {\"type\": \"ping\"}\n\n"
	out := streamThrough(t, m, []string{td("hi " + p[:6]), ": keepalive\n\n", ping, td(p[6:] + "!")}, 4096)
	text := collect(t, out, func(v map[string]any) (string, bool) {
		d, _ := v["delta"].(map[string]any)
		s, ok := d["text"].(string)
		return s, ok
	})
	if text != "hi jo@quill.dev!" {
		t.Fatalf("text %q\n%s", text, out)
	}
	// the keepalive and the ping went out before the held delta
	if strings.Index(out, ": keepalive") > strings.Index(out, "hi ") || strings.Index(out, "event: ping") > strings.Index(out, "hi ") {
		t.Fatalf("keepalives were held behind the partial event:\n%s", out)
	}
}

func TestStreamResponsesTextDelta(t *testing.T) {
	m := testMasker(t)
	p := maskedEmail(t, m, "jo@quill.dev")
	ev := func(s string) string {
		return dataEvent("response.output_text.delta", map[string]any{"type": "response.output_text.delta",
			"item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": s})
	}
	out := streamThrough(t, m, []string{ev("to " + p[:10]), ev(p[10:])}, 4096)
	got := collect(t, out, func(v map[string]any) (string, bool) { s, ok := v["delta"].(string); return s, ok })
	if got != "to jo@quill.dev" {
		t.Fatalf("got %q", got)
	}
}

func TestStreamToolArgumentsRestoredEscaped(t *testing.T) {
	m := testMasker(t)
	secret := `pa"ss\w0rdX9`
	maskToolSecret(t, m, secret)
	p := m.Tag(KindSecret, secret)
	if _, ok := m.Value(p); !ok {
		t.Fatal("secret not masked")
	}
	args := `{"user":"root","pw":"` + p + `"}`

	// OpenAI Chat: tool_calls[].function.arguments fragments
	chat := func(frag string) string {
		return dataEvent("", map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": frag}}}}}}})
	}
	// Anthropic: input_json_delta partial_json fragments
	anth := func(frag string) string {
		return dataEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": 1,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": frag}})
	}
	// Responses: function_call_arguments.delta fragments
	resp := func(frag string) string {
		return dataEvent("response.function_call_arguments.delta", map[string]any{
			"type": "response.function_call_arguments.delta", "item_id": "fc_1", "output_index": 0, "delta": frag})
	}
	picks := map[string]func(map[string]any) (string, bool){
		"chat": func(v map[string]any) (string, bool) {
			ch, _ := v["choices"].([]any)
			d, _ := ch[0].(map[string]any)["delta"].(map[string]any)
			tc, _ := d["tool_calls"].([]any)
			f, _ := tc[0].(map[string]any)["function"].(map[string]any)
			s, ok := f["arguments"].(string)
			return s, ok
		},
		"anthropic": func(v map[string]any) (string, bool) {
			d, _ := v["delta"].(map[string]any)
			s, ok := d["partial_json"].(string)
			return s, ok
		},
		"responses": func(v map[string]any) (string, bool) { s, ok := v["delta"].(string); return s, ok },
	}
	makers := map[string]func(string) string{"chat": chat, "anthropic": anth, "responses": resp}
	cut := strings.Index(args, p) + 5
	for name, mk := range makers {
		out := streamThrough(t, m, []string{mk(args[:8]), mk(args[8:cut]), mk(args[cut:])}, 13)
		joined := collect(t, out, picks[name])
		var got map[string]string
		if err := json.Unmarshal([]byte(joined), &got); err != nil {
			t.Fatalf("%s: arguments %q not JSON: %v", name, joined, err)
		}
		if got["pw"] != secret || got["user"] != "root" {
			t.Fatalf("%s: arguments %v", name, got)
		}
	}
}

func TestWholeBodyRestoredAtFinish(t *testing.T) {
	m := testMasker(t)
	p := maskedEmail(t, m, "jo@quill.dev")
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.Header().Set("Content-Length", "99")
	rw := m.NewWriter(rec)
	rw.WriteHeader(200)
	_, _ = rw.Write([]byte(`{"content":[{"type":"text","text":"hi `))
	_, _ = rw.Write([]byte(p + `"}],"signature":"` + p + `"}`))
	if rec.Body.Len() != 0 {
		t.Fatal("whole body written before Finish")
	}
	rw.Finish()
	rw.Finish()
	want := `{"content":[{"type":"text","text":"hi jo@quill.dev"}],"signature":"` + p + `"}`
	if rec.Body.String() != want || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestStreamWithNothingMaskedPassesThrough(t *testing.T) {
	m := testMasker(t)
	events := []string{chatDelta("Hello {{EMA"), chatDelta("IL}} world"), "data: [DONE]\n\n"}
	if out := streamThrough(t, m, events, 5); out != strings.Join(events, "") {
		t.Fatalf("changed: %q", out)
	}
}
