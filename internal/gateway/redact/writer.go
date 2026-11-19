package redact

import (
	"bytes"
	"net/http"
	"strings"
)

// Writer puts the values back in what a vendor answers, on its way to the client. A stream is
// put back event by event, as it comes; anything else when it is done, at Finish.
//
// A placeholder a stream splits between two events — {{API_ in one text delta, KEY_k3v9x2mq}} in
// the next — is put back whole: an event whose text ends in what may be the start of one is held
// until the next, and if that one goes on with the same text (same event context and JSON path)
// the start moves over to it. Nothing is added or dropped, so the text the client puts together
// is the same. Comments (": keepalive") and wait events (ping) pass a held event.
type Writer struct {
	m       *Masker
	w       http.ResponseWriter
	mode    int
	buf     bytes.Buffer
	pending *held
	done    bool
}

const (
	modeUnknown = iota
	modeStream
	modeWhole
	modeAsIs
)

// held is an event one or more of whose texts ended in what may be the start of a placeholder:
// each one's tail by its path.
type held struct {
	ctx       string
	tails     map[string]string
	full, cut []byte // the event as it is, and without the tails
}

// NewWriter wraps w; Finish must be called when the handler is done.
func (m *Masker) NewWriter(w http.ResponseWriter) *Writer { return &Writer{m: m, w: w} }

func (rw *Writer) Header() http.Header { return rw.w.Header() }

// Unwrap is for http.ResponseController.
func (rw *Writer) Unwrap() http.ResponseWriter { return rw.w }

func (rw *Writer) decide(first []byte) {
	h := rw.w.Header()
	ct := strings.ToLower(h.Get("Content-Type"))
	switch {
	case h.Get("Content-Encoding") != "" && !strings.EqualFold(h.Get("Content-Encoding"), "identity"):
		rw.mode = modeAsIs
	case strings.Contains(ct, "event-stream"):
		rw.mode = modeStream
	case ct == "" && (bytes.HasPrefix(first, []byte("event:")) || bytes.HasPrefix(first, []byte("data:")) || bytes.HasPrefix(first, []byte(":"))):
		rw.mode = modeStream
	case binaryType(ct):
		rw.mode = modeAsIs
	default:
		rw.mode = modeWhole
	}
}

func binaryType(ct string) bool {
	for _, t := range []string{"audio/", "image/", "video/", "application/octet-stream", "application/pdf", "application/zip"} {
		if strings.HasPrefix(ct, t) {
			return true
		}
	}
	return false
}

func (rw *Writer) WriteHeader(code int) {
	if rw.mode == modeUnknown && rw.w.Header().Get("Content-Type") != "" {
		rw.decide(nil)
	}
	if rw.mode != modeAsIs {
		rw.w.Header().Del("Content-Length") // what is put back is not as long as what came
	}
	rw.w.WriteHeader(code)
}

func (rw *Writer) Write(p []byte) (int, error) {
	if rw.mode == modeUnknown {
		rw.decide(p)
		if rw.mode != modeAsIs {
			rw.w.Header().Del("Content-Length")
		}
	}
	switch rw.mode {
	case modeAsIs:
		return rw.w.Write(p)
	case modeWhole:
		return rw.buf.Write(p)
	}
	rw.buf.Write(p)
	for {
		b := rw.buf.Bytes()
		if c := commentLines(b); c > 0 {
			if _, err := rw.w.Write(b[:c]); err != nil {
				return len(p), err
			}
			rw.buf.Next(c)
			continue
		}
		i, n := eventEnd(b)
		if i < 0 {
			break
		}
		ev := append([]byte(nil), b[:i+n]...)
		rw.buf.Next(i + n)
		if err := rw.event(ev); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// commentLines is how long the whole comment lines and blank lines at the start of b are.
func commentLines(b []byte) int {
	n := 0
	for n < len(b) && (b[n] == ':' || b[n] == '\n' || b[n] == '\r') {
		i := bytes.IndexByte(b[n:], '\n')
		if i < 0 || b[n] == '\r' && i != 1 {
			break
		}
		n += i + 1
	}
	return n
}

// eventEnd is where the first complete event in b ends, and the length of the blank line.
func eventEnd(b []byte) (int, int) {
	i := bytes.Index(b, []byte("\n\n"))
	j := bytes.Index(b, []byte("\r\n\r\n"))
	switch {
	case i < 0 && j < 0:
		return -1, 0
	case j >= 0 && (i < 0 || j < i):
		return j, 4
	default:
		return i, 2
	}
}

func (rw *Writer) Flush() {
	if f, ok := rw.w.(http.Flusher); ok && rw.mode != modeWhole {
		f.Flush()
	}
}

// Finish writes what is still held. It is safe to call more than once.
func (rw *Writer) Finish() {
	if rw.done {
		return
	}
	rw.done = true
	switch rw.mode {
	case modeWhole:
		if rw.buf.Len() > 0 {
			_, _ = rw.w.Write(rw.m.RestoreJSON(rw.buf.Bytes()))
		}
	case modeStream:
		if rw.pending != nil {
			_, _ = rw.w.Write(rw.pending.full)
			rw.pending = nil
		}
		if rw.buf.Len() > 0 {
			_, _ = rw.w.Write([]byte(rw.m.Restore(rw.buf.String(), false)))
		}
	}
	if f, ok := rw.w.(http.Flusher); ok {
		f.Flush()
	}
}

// event puts the values back in one event and writes it, or holds it.
func (rw *Writer) event(ev []byte) error {
	if rw.pending == nil && !rw.m.holding() {
		_, err := rw.w.Write(ev) // nothing masked to put back
		return err
	}
	name, data, pre, post, ok := splitEvent(ev)
	if !ok {
		if err := rw.release(); err != nil {
			return err
		}
		_, err := rw.w.Write([]byte(rw.m.Restore(string(ev), false)))
		return err
	}
	if rw.pending != nil && waiting(name, data) {
		// a keepalive between two pieces of a text goes on ahead of the piece held
		_, err := rw.w.Write(rebuild(pre, rw.m.RestoreJSON(data), post))
		return err
	}
	ctx := name + "|" + eventCtx(data)
	var carry map[string]string
	if p := rw.pending; p != nil {
		if p.ctx == ctx && hasPaths(data, p.tails) {
			if _, err := rw.w.Write(p.cut); err != nil {
				return err
			}
			carry = p.tails
		} else if _, err := rw.w.Write(p.full); err != nil {
			return err
		}
		rw.pending = nil
	}
	typ := typeOf(data)
	if typ == "" {
		typ = name
	}
	var tails, cuts map[string]string
	restored, _ := walk(data, func(path, key, s string) string {
		if c, ok := carry[path]; ok {
			s = c + s
		}
		if sealedText(key, s) {
			return s
		}
		// the tail is looked for before anything is put back, so a placeholder whose last
		// brace is still to come isn't put back without it
		if !kept[key] && key != "obfuscation" {
			if n := partialTail(s); n > 0 {
				if tails == nil {
					tails, cuts = map[string]string{}, map[string]string{}
				}
				tails[path] = s[len(s)-n:]
				cuts[path] = rw.m.restore(s[:len(s)-n], asJSON(key, typ), false)
			}
		}
		return rw.m.Restore(s, asJSON(key, typ))
	})
	full := rebuild(pre, restored, post)
	if tails == nil {
		_, err := rw.w.Write(full)
		return err
	}
	cutData, _ := walk(restored, func(path, _, s string) string {
		if c, ok := cuts[path]; ok {
			return c
		}
		return s
	})
	rw.pending = &held{ctx: ctx, tails: tails, full: full, cut: rebuild(pre, cutData, post)}
	return nil
}

// waitEvents are the events a stream sends while the model is slow, which say nothing of the
// answer: Anthropic's ping, Responses' keepalive and in-progress events.
var waitEvents = []string{"ping", "keepalive", "response.in_progress", "response.queued"}

func waiting(name string, data []byte) bool {
	typ := typeOf(data)
	if typ == "" {
		typ = name
	}
	for _, w := range waitEvents {
		if typ == w {
			return true
		}
	}
	return false
}

func (rw *Writer) release() error {
	if p := rw.pending; p != nil {
		rw.pending = nil
		_, err := rw.w.Write(p.full)
		return err
	}
	return nil
}

func rebuild(pre, data, post []byte) []byte {
	out := make([]byte, 0, len(pre)+len(data)+len(post))
	return append(append(append(out, pre...), data...), post...)
}

// splitEvent finds the one data line of an event that has JSON in it: what comes before the
// JSON, the JSON, and what comes after it.
func splitEvent(ev []byte) (name string, data, pre, post []byte, ok bool) {
	lines := bytes.SplitAfter(ev, []byte("\n"))
	at := -1
	off := 0
	for i, l := range lines {
		t := bytes.TrimRight(l, "\r\n")
		switch {
		case bytes.HasPrefix(t, []byte("event:")):
			name = strings.TrimSpace(string(t[6:]))
		case bytes.HasPrefix(t, []byte("data:")):
			if at >= 0 {
				return "", nil, nil, nil, false // data over more than one line
			}
			at = i
		}
		if at < 0 {
			off += len(l)
		}
	}
	if at < 0 {
		return "", nil, nil, nil, false
	}
	t := bytes.TrimRight(lines[at], "\r\n")
	start := 5
	if len(t) > 5 && t[5] == ' ' {
		start = 6
	}
	d := t[start:]
	if len(d) == 0 || d[0] != '{' {
		return "", nil, nil, nil, false
	}
	return name, d, ev[:off+start], ev[off+len(t):], true
}

// eventCtx is what says which text an event goes on with.
func eventCtx(data []byte) string {
	var b strings.Builder
	walk(data, func(path, _, s string) string {
		switch path {
		case "type", "item_id":
			b.WriteString(path + "=" + s + ";")
		}
		return s
	})
	for _, k := range []string{`"index":`, `"output_index":`, `"content_index":`} {
		if i := bytes.Index(data, []byte(k)); i >= 0 {
			j := i + len(k)
			e := j
			for e < len(data) && (data[e] >= '0' && data[e] <= '9' || data[e] == ' ') {
				e++
			}
			b.WriteString(k + string(data[j:e]) + ";")
		}
	}
	return b.String()
}

// hasPaths says data has a string at each of the paths.
func hasPaths(data []byte, paths map[string]string) bool {
	found := 0
	walk(data, func(p, _, s string) string {
		if _, ok := paths[p]; ok {
			found++
		}
		return s
	})
	return found == len(paths)
}
