// Package sse provides Server-Sent Events streaming reader and writer utilities.
package sse

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// Event represents a single parsed Server-Sent Event frame.
type Event struct {
	Type string
	Data []byte
	ID   string
	// Raw holds the frame's original lines (comments included, blank terminator appended) when
	// the Reader was created with NewRawReader; passthrough streaming forwards it verbatim.
	Raw []byte
}

// Reader incrementally decodes SSE frames from an io.Reader without fixed small-line limits.
type Reader struct {
	br        *bufio.Reader
	eventType string
	eventID   string
	dataLines [][]byte
	eof       bool
	keepRaw   bool
	raw       []byte
}

// NewReader wraps r in a buffered SSE Reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{
		br: bufio.NewReaderSize(r, 64*1024),
	}
}

// NewRawReader is NewReader that also records each frame's raw bytes in Event.Raw.
func NewRawReader(r io.Reader) *Reader {
	rd := NewReader(r)
	rd.keepRaw = true
	return rd
}

// Next reads the next complete SSE frame. It returns io.EOF after all frames have been read.
func (r *Reader) Next() (Event, error) {
	if r.eof {
		return Event{}, io.EOF
	}
	for {
		line, err := r.readLine()
		if r.keepRaw && (err == nil || len(line) > 0) {
			r.raw = append(r.raw, line...)
			r.raw = append(r.raw, '\n')
		}
		if err != nil {
			if err == io.EOF {
				r.eof = true
				if r.hasPending() {
					return r.flush(), nil
				}
				return Event{}, io.EOF
			}
			return Event{}, err
		}

		// Empty line dispatches accumulated event.
		if len(line) == 0 {
			if r.hasPending() {
				return r.flush(), nil
			}
			continue
		}

		// Comment line.
		if line[0] == ':' {
			continue
		}

		field, value := splitField(line)
		switch string(field) {
		case "event":
			r.eventType = string(value)
		case "data":
			cp := make([]byte, len(value))
			copy(cp, value)
			r.dataLines = append(r.dataLines, cp)
		case "id":
			if !bytes.ContainsRune(value, 0) {
				r.eventID = string(value)
			}
		}
	}
}

func (r *Reader) readLine() ([]byte, error) {
	var buf []byte
	for {
		frag, isPrefix, err := r.br.ReadLine()
		if len(frag) > 0 {
			buf = append(buf, frag...)
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return buf, nil
			}
			return buf, err
		}
		if !isPrefix {
			return bytes.TrimSuffix(buf, []byte{'\r'}), nil
		}
	}
}

func (r *Reader) hasPending() bool {
	return len(r.dataLines) > 0 || r.eventType != ""
}

func (r *Reader) flush() Event {
	var data []byte
	if len(r.dataLines) == 1 {
		data = r.dataLines[0]
	} else if len(r.dataLines) > 1 {
		data = bytes.Join(r.dataLines, []byte{'\n'})
	}
	ev := Event{
		Type: r.eventType,
		Data: data,
		ID:   r.eventID,
	}
	if r.keepRaw {
		raw := r.raw
		if !bytes.HasSuffix(raw, []byte("\n\n")) {
			raw = append(raw, '\n')
		}
		ev.Raw = raw
		r.raw = nil
	}
	r.eventType = ""
	r.dataLines = r.dataLines[:0]
	return ev
}

func splitField(line []byte) (field, value []byte) {
	idx := bytes.IndexByte(line, ':')
	if idx < 0 {
		return line, nil
	}
	field = line[:idx]
	value = line[idx+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return field, value
}

// IsDone returns true when an SSE data payload is the OpenAI "[DONE]" sentinel.
func IsDone(data []byte) bool {
	return strings.TrimSpace(string(data)) == "[DONE]"
}
