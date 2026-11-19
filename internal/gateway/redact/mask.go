package redact

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ErrNotJSON is returned by MaskJSON for a body that is not a JSON document.
var ErrNotJSON = errors.New("request body is not valid JSON")

// MaskJSON masks the strings of a client request body in any of the four client protocols and
// reports each value it found. Ids, names, kinds, signatures and data: URIs are left alone; in
// what the client defines for the vendor (tools, schemas, settings) only descriptions are
// masked; in a tool call's arguments every string is the tool's (a field named for a secret is
// masked whole); what a vendor sealed with a signature only gets values it was sent masked
// before put back to their placeholders, so the signed text matches again. A panic in a rule is
// an error, never an unmasked body.
func (m *Masker) MaskJSON(body []byte, pol Policy) (out []byte, hits []Hit, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, hits, err = nil, nil, fmt.Errorf("masking failed: %v", r)
		}
	}()
	signed, args, defs := scan(body)
	out, ok := walk(body, func(path, key, s string) string {
		if strings.HasPrefix(s, "data:") {
			return s
		}
		if signed != nil && under(path, signed) {
			if kept[key] || isID(key) {
				return s
			}
			return m.remaskKnown(s, pol, &hits)
		}
		if under(path, args) {
			return m.maskArgValue(key, s, pol, &hits)
		}
		if under(path, defs) {
			if key != "description" {
				return s
			}
			return m.maskText(s, pol, &hits, true)
		}
		if kept[key] || isID(key) {
			return s
		}
		if key == "arguments" {
			return m.maskArgsText(s, pol, &hits)
		}
		return m.maskText(s, pol, &hits, true)
	})
	if !ok {
		return nil, nil, ErrNotJSON
	}
	m.count(hits)
	return out, hits, nil
}

// MaskText masks a plain text and remembers its placeholders.
func (m *Masker) MaskText(s string, pol Policy) (out string, hits []Hit, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, hits, err = "", nil, fmt.Errorf("masking failed: %v", r)
		}
	}()
	out = m.maskText(s, pol, &hits, true)
	m.count(hits)
	return out, hits, nil
}

// Preview is MaskText without remembering anything or counting: what masking would do.
func (m *Masker) Preview(s string, pol Policy) (string, []Hit) {
	var hits []Hit
	return m.maskText(s, pol, &hits, false), hits
}

// maskArgValue masks a string inside a tool call's arguments: keys there are the tool's own.
func (m *Masker) maskArgValue(key, s string, pol Policy, hits *[]Hit) string {
	if key == "arguments" {
		return m.maskArgsText(s, pol, hits)
	}
	if namedSecret(key, s) && pol.mode(KindSecret) != ModeOff && !pol.allows(KindSecret, s) {
		return m.replaceWhole(KindSecret, s, pol, hits)
	}
	return m.maskText(s, pol, hits, true)
}

// maskArgsText masks arguments written as JSON text ({"path":"…"} inside a string): the inner
// document is walked with the tool's-keys rules, so what goes back in stays valid, correctly
// escaped JSON. Text that isn't JSON is masked as text.
func (m *Masker) maskArgsText(s string, pol Policy, hits *[]Hit) string {
	t := strings.TrimSpace(s)
	if t == "" || t[0] != '{' && t[0] != '[' {
		return m.maskText(s, pol, hits, true)
	}
	out, ok := walk([]byte(s), func(_, key, v string) string {
		if strings.HasPrefix(v, "data:") {
			return v
		}
		return m.maskArgValue(key, v, pol, hits)
	})
	if !ok {
		return m.maskText(s, pol, hits, true)
	}
	return string(out)
}

func (m *Masker) replaceWhole(kind, v string, pol Policy, hits *[]Hit) string {
	h := Hit{Kind: kind, Action: pol.mode(kind), Len: utf8.RuneCountInString(v)}
	switch h.Action {
	case ModeWarn, ModeBlock:
		h.Placeholder = m.Tag(kind, v)
		*hits = append(*hits, h)
		return v
	}
	h.Placeholder = m.put(kind, v)
	*hits = append(*hits, h)
	return h.Placeholder
}

// maskText replaces every value the rules find in s (remember: placeholders are kept for
// restoring; a preview only computes them).
func (m *Masker) maskText(s string, pol Policy, hits *[]Hit, remember bool) string {
	found := resolve(s, find(s, pol))
	if len(found) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, f := range found {
		v := s[f.start:f.end]
		if pol.allows(f.kind, v) {
			continue
		}
		h := Hit{Kind: f.kind, Action: pol.mode(f.kind), Len: utf8.RuneCountInString(v)}
		if h.Action != ModeMask || !remember {
			h.Placeholder = m.Tag(f.kind, v)
			*hits = append(*hits, h)
			if h.Action != ModeMask {
				continue
			}
		} else {
			h.Placeholder = m.put(f.kind, v)
			*hits = append(*hits, h)
		}
		b.WriteString(s[last:f.start])
		b.WriteString(h.Placeholder)
		last = f.end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// remaskKnown puts back the placeholder of every value in s that was masked before: in a block
// the vendor signed, the vendor wrote the placeholder where the client now shows the value.
func (m *Masker) remaskKnown(s string, pol Policy, hits *[]Hit) string {
	found := resolve(s, find(s, Policy{Modes: pol.Modes}))
	if len(found) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, f := range found {
		v := s[f.start:f.end]
		p, ok := m.known(f.kind, v)
		if !ok {
			continue
		}
		*hits = append(*hits, Hit{Kind: f.kind, Placeholder: p, Action: ModeMask, Len: utf8.RuneCountInString(v)})
		b.WriteString(s[last:f.start])
		b.WriteString(p)
		last = f.end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

var secretNameRe = regexp.MustCompile(`(?i)(?:` + secretNames + `|authorization|cookie|pwd)`)

// namedSecret says a string under a field named for a secret holds one, whatever it looks like:
// {"api_key": "…"} in a tool call's arguments. Short values, variables and timestamps are left.
func namedSecret(key, s string) bool {
	if key == "" || len(s) < 8 || !notAVariable(s) || stampRe.MatchString(s) || !secretNameRe.MatchString(key) {
		return false
	}
	lk := strings.ToLower(key)
	for _, suf := range []string{"name", "type", "count", "path", "file", "url", "limit", "len", "length", "tokens", "id", "ids"} {
		if strings.HasSuffix(lk, suf) {
			return false
		}
	}
	return true
}
