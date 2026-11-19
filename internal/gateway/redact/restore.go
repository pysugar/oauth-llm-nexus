package redact

import (
	"regexp"
	"strings"
)

// placeholderRe is a placeholder as the masker writes it.
var placeholderRe = regexp.MustCompile(`\{\{[A-Z][A-Z0-9_]*_[a-z2-7]{8}\}\}`)

// placeholderBody is a placeholder without its braces, as a model may write one back:
// API_KEY_k3v9x2mq, email_b5puzbfv, EMAIL_B5PUZBFV.
var placeholderBody = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*_[A-Za-z2-7]{8}`)

// maxKind is the longest kind name.
var maxKind = func() int {
	n := 0
	for _, k := range Kinds {
		n = max(n, len(k))
	}
	return n
}()

// maxPlaceholder is the longest placeholder: {{, a kind, _, the 8 of the hash, }}.
var maxPlaceholder = 2 + maxKind + 1 + 8 + 2

// Restore puts the values back in s. escaped is for text that is itself JSON (a tool call's
// arguments), where a value goes in JSON-escaped.
//
// A model doesn't always write a placeholder back as it was given: it writes {{ EMAIL_x }} as a
// template has it, drops a brace or both, or changes the case. Any of those is put back when
// what is left names a placeholder this masker made.
func (m *Masker) Restore(s string, escaped bool) string { return m.restore(s, escaped, true) }

func (m *Masker) restore(s string, escaped, count bool) string {
	if !strings.Contains(s, "_") {
		return s
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.byP) == 0 {
		return s
	}
	var b strings.Builder
	last, n := 0, 0
	for _, loc := range placeholderBody.FindAllStringIndex(s, -1) {
		a, z := loc[0], loc[1]
		if a < last || a > 0 && isWordByte(s[a-1]) || z < len(s) && isWordByte(s[z]) {
			continue
		}
		el, ok := m.byP[canonical(s[a:z])]
		if !ok {
			continue
		}
		a, z = braces(s, a, z)
		if a < last {
			continue
		}
		v := el.Value.(*entry).v
		if escaped {
			v = jsonEscape(v)
		}
		b.WriteString(s[last:a])
		b.WriteString(v)
		last, n = z, n+1
	}
	if n == 0 {
		return s
	}
	if count {
		m.rest.Add(int64(n))
	}
	b.WriteString(s[last:])
	return b.String()
}

// RestoreJSON puts the values back in a JSON response body (a body that isn't JSON is restored
// as text).
func (m *Masker) RestoreJSON(body []byte) []byte {
	if !m.holding() || !strings.Contains(string(body), "_") {
		return body
	}
	typ := typeOf(body)
	out, ok := walk(body, func(_, key, s string) string {
		if sealedText(key, s) {
			return s
		}
		return m.Restore(s, asJSON(key, typ))
	})
	if !ok {
		return []byte(m.Restore(string(body), false))
	}
	return out
}

// sealedText says a string of a vendor's answer goes on as it came: what the vendor sealed and a
// file's encoded data.
func sealedText(key, s string) bool {
	for _, k := range signatureKeys {
		if key == k {
			return true
		}
	}
	return strings.HasPrefix(s, "data:") || key == "data" && len(s) >= 64 && !strings.ContainsAny(s, " \n")
}

// canonical is the placeholder written for body, whatever its case.
func canonical(body string) string {
	cut := len(body) - 8
	return "{{" + strings.ToUpper(body[:cut]) + strings.ToLower(body[cut:]) + "}}"
}

func isWordByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}

// braces widens s[a:z], a placeholder's body, over the braces the model wrote around it and the
// spaces inside them: both of {{ and }}, or one of them dropped ({{X} or {X}}), or just {{ or
// just }}. A single brace on one side alone is the text's own and stays.
func braces(s string, a, z int) (int, int) {
	la, l := a, 0
	for la > 0 && (s[la-1] == ' ' || s[la-1] == '\t') {
		la--
	}
	for l < 2 && la > 0 && s[la-1] == '{' {
		la--
		l++
	}
	rz, r := z, 0
	for rz < len(s) && (s[rz] == ' ' || s[rz] == '\t') {
		rz++
	}
	for r < 2 && rz < len(s) && s[rz] == '}' {
		rz++
		r++
	}
	if l == 2 || l == 1 && r >= 1 {
		a = la
	}
	if r == 2 || r == 1 && l >= 1 {
		z = rz
	}
	return a, z
}

// partialTail is how much of the end of s may be the start of a placeholder that the next piece
// of a stream finishes: {{API_, {{ email_b5, EMAIL_b5puzbfv} — or, with no brace before it, a last
// word that starts like a placeholder (EMA, EMAIL_b5pu), or is one whose }} may still come.
func partialTail(s string) int {
	if n := braceTail(s); n > 0 {
		return n
	}
	return wordTail(s)
}

// braceTail is a tail from {, or {{, on: then spaces, a placeholder's body or the start of one,
// then spaces and one } at most.
func braceTail(s string) int {
	i := strings.LastIndexByte(s, '{')
	if i < 0 {
		return 0
	}
	if i > 0 && s[i-1] == '{' {
		i--
	}
	t := s[i:]
	if len(t) >= maxPlaceholder+4 || strings.Contains(t, "}}") {
		return 0
	}
	body := strings.TrimLeft(t, "{")
	body = strings.TrimLeft(body, " \t")
	if body == "" {
		return len(t) // {, {{, {{␠
	}
	if c := body[0]; !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
		return 0
	}
	j := 0
	for j < len(body) && isWordByte(body[j]) {
		j++
	}
	rest := strings.TrimLeft(body[j:], " \t")
	if rest != "" && rest != "}" {
		return 0
	}
	if j < len(body) && placeholderBody.FindString(body[:j]) != body[:j] {
		return 0 // a space or a brace after something that is no placeholder
	}
	if !kindPrefix(body[:j]) {
		return 0
	}
	return len(t)
}

// kindPrefix says w (in any case) is the start of a placeholder's body of a known kind.
func kindPrefix(w string) bool {
	u := strings.ToUpper(w)
	for _, k := range Kinds {
		k += "_"
		if strings.HasPrefix(k, u) {
			return true
		}
		if strings.HasPrefix(u, k) && len(u)-len(k) <= 8 &&
			strings.Trim(strings.ToLower(u[len(k):]), "abcdefghijklmnopqrstuvwxyz234567") == "" {
			return true
		}
	}
	return false
}

// wordTail is the last word of s when it may be a placeholder's body written without braces:
// the start of a kind, up to the 8 of its hash. A word that only starts like a kind is held for
// one event and then goes on, unchanged.
func wordTail(s string) int {
	i := len(s)
	for i > 0 && isWordByte(s[i-1]) {
		i--
	}
	w := s[i:]
	if w == "" || len(w) > maxKind+9 || !(w[0] >= 'A' && w[0] <= 'Z') {
		return 0
	}
	if kindPrefix(w) {
		return len(w)
	}
	return 0
}
