package sse

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReaderLargeAndMultilineFrames(t *testing.T) {
	large := strings.Repeat("x", 128*1024)
	raw := ": ping\n\n" +
		"event: content_block_delta\n" +
		"data: {\"a\":1}\n" +
		"data: {\"b\":2}\n\n" +
		"data: " + large + "\n\n" +
		"data: [DONE]\n\n"

	r := NewReader(strings.NewReader(raw))

	ev1, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected err reading ev1: %v", err)
	}
	if ev1.Type != "content_block_delta" || string(ev1.Data) != "{\"a\":1}\n{\"b\":2}" {
		t.Fatalf("unexpected ev1: %+v", ev1)
	}

	ev2, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected err reading ev2: %v", err)
	}
	if len(ev2.Data) != len(large) {
		t.Fatalf("expected payload len %d, got %d", len(large), len(ev2.Data))
	}

	ev3, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected err reading ev3: %v", err)
	}
	if !IsDone(ev3.Data) {
		t.Fatalf("expected [DONE], got %q", string(ev3.Data))
	}

	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestWriterFormatsFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	SetHeaders(rec)
	w := NewWriter(rec)

	if err := w.WriteJSON("message_start", map[string]any{"type": "message_start"}); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}
	if err := w.WriteDone(); err != nil {
		t.Fatalf("WriteDone failed: %v", err)
	}

	got := rec.Body.String()
	want := "event: message_start\ndata: {\"type\":\"message_start\"}\n\ndata: [DONE]\n\n"
	if got != want {
		t.Fatalf("unexpected SSE output:\ngot:  %q\nwant: %q", got, want)
	}
}
