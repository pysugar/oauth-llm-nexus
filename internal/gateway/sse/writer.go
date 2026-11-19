package sse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// SetHeaders configures standard Server-Sent Events headers on w.
func SetHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

// Writer emits properly formatted SSE frames and flushes immediately.
type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewWriter creates an SSE Writer backed by w.
func NewWriter(w http.ResponseWriter) *Writer {
	flusher, _ := w.(http.Flusher)
	return &Writer{w: w, flusher: flusher}
}

// WriteEvent writes an SSE frame with an optional event type and raw payload, then flushes.
func (sw *Writer) WriteEvent(eventType string, data []byte) error {
	var buf bytes.Buffer
	if eventType != "" {
		buf.WriteString("event: ")
		buf.WriteString(eventType)
		buf.WriteByte('\n')
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		buf.WriteString("data: ")
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')

	if _, err := sw.w.Write(buf.Bytes()); err != nil {
		return err
	}
	sw.Flush()
	return nil
}

// WriteJSON marshals v to JSON and emits it as an SSE frame.
func (sw *Writer) WriteJSON(eventType string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return sw.WriteEvent(eventType, raw)
}

// WriteDone emits OpenAI's terminating `data: [DONE]` frame.
func (sw *Writer) WriteDone() error {
	if _, err := sw.w.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	sw.Flush()
	return nil
}

// WriteComment writes an SSE comment line (e.g., keepalive heartbeat).
func (sw *Writer) WriteComment(comment string) error {
	if _, err := sw.w.Write([]byte(": " + comment + "\n\n")); err != nil {
		return err
	}
	sw.Flush()
	return nil
}

// WriteCommentLine writes a single SSE comment line with no blank line after it. Unlike
// WriteComment it never dispatches an (empty) event, which some SDKs (openai-go) reject; the
// comment is simply absorbed by the next frame.
func (sw *Writer) WriteCommentLine(comment string) error {
	if _, err := sw.w.Write([]byte(": " + comment + "\n")); err != nil {
		return err
	}
	sw.Flush()
	return nil
}

// WritePing emits Anthropic's `event: ping` frame.
func (sw *Writer) WritePing() error {
	return sw.WriteEvent("ping", []byte(`{"type": "ping"}`))
}

// Flush flushes the underlying ResponseWriter if supported.
func (sw *Writer) Flush() {
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
}
