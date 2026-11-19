package redact

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func testMasker(t *testing.T) *Masker {
	t.Helper()
	return NewWithKey(bytes.Repeat([]byte{7}, 32), 0)
}

const (
	anthKey = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
	ghToken = "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"
)

func TestRulesFindAndLeave(t *testing.T) {
	m := testMasker(t)
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nabc\n-----END RSA PRIVATE KEY-----"
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	cases := []struct {
		in, kind, value string
	}{
		{"key " + anthKey + " here", KindAPIKey, anthKey},
		{"sk-proj-abcDEF1234567890abcdefXYZ", KindAPIKey, "sk-proj-abcDEF1234567890abcdefXYZ"},
		{"token " + ghToken, KindAPIKey, ghToken},
		{"github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ", KindAPIKey, "github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"},
		{"AIzaSyA1234567890abcdefghijklmnopqrstuv", KindAPIKey, "AIzaSyA1234567890abcdefghijklmnopqrstuv"},
		{"xoxb-1234567890-abcdefghij", KindAPIKey, "xoxb-1234567890-abcdefghij"},
		{"aws AKIAIOSFODNN7EXAMPLE ok", KindAPIKey, "AKIAIOSFODNN7EXAMPLE"},
		{"hf_abcdefghijklmnopqrstuvwxyz0123456789", KindAPIKey, "hf_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"gsk_" + strings.Repeat("a1", 22), KindAPIKey, "gsk_" + strings.Repeat("a1", 22)},
		{"xai-" + strings.Repeat("b2", 22), KindAPIKey, "xai-" + strings.Repeat("b2", 22)},
		{"jwt " + jwt, KindToken, jwt},
		{pem, KindPrivateKey, pem},
		{"postgres://admin:s3cr3tPw@db.internal:5432/app", KindPassword, "s3cr3tPw"},
		{"DB_PASSWORD=hunter2isNotSafe9", KindSecret, "hunter2isNotSafe9"},
		{`"client_secret": "abc123def456ghi"`, KindSecret, "abc123def456ghi"},
		{"STRIPE_WEBHOOK_KEY=whsec9abcdef0123", KindSecret, "whsec9abcdef0123"},
		{"GET /\nAuthorization: Bearer opaque.token-123456\n", KindSecret, "Bearer opaque.token-123456"},
		{`curl -H "Authorization: Bearer abc.def-1234567"`, KindSecret, "Bearer abc.def-1234567"},
		{"Cookie: session=abcd1234efgh; theme=dark", KindSecret, "session=abcd1234efgh; theme=dark"},
		{"mail alice.smith@acme-corp.com now", KindEmail, "alice.smith@acme-corp.com"},
		{"mail bob@example.co.uk.", KindEmail, "bob@example.co.uk"},
		{"card 4111 1111 1111 1111 exp", KindBankCard, "4111 1111 1111 1111"},
		{"id 11010519491231002X ok", KindIDCard, "11010519491231002X"},
	}
	for _, c := range cases {
		out, hits := m.Preview(c.in, Policy{})
		if len(hits) != 1 || hits[0].Kind != c.kind {
			t.Errorf("%q: hits %+v, want one %s", c.in, hits, c.kind)
			continue
		}
		if strings.Contains(out, c.value) || !strings.Contains(out, m.Tag(c.kind, c.value)) {
			t.Errorf("%q: masked to %q", c.in, out)
		}
	}

	leave := []string{
		"pip install sk-learn-preprocessing-utils",
		"password = ${DB_PASSWORD}",
		"token: getToken()",
		"const opts = {token: myToken, other: 1}",
		"max_tokens: 4096",
		"write to alice@example.com",
		"see logo@2x.png and notes@AGENTS.md",
		"git@github.com:org/repo.git",
		"reply to noreply@github.com",
		"card 4111 1111 1111 1112",
		"id 110105194912310021",
		"pi is 3.14159265358979323846",
		"Authorization: is required for every call",
		"version_token = 1.2.3",
		"https://user:user@host.com/",
	}
	for _, s := range leave {
		if out, hits := m.Preview(s, Policy{}); len(hits) != 0 {
			t.Errorf("%q: masked as %+v → %q", s, hits, out)
		}
	}
}

func TestPlaceholderShapeAndDeterminism(t *testing.T) {
	m := testMasker(t)
	p := m.Tag(KindEmail, "a@b.io")
	if !regexp.MustCompile(`^\{\{EMAIL_[a-z2-7]{8}\}\}$`).MatchString(p) {
		t.Fatalf("placeholder %q", p)
	}
	if p != m.Tag(KindEmail, "a@b.io") || p == m.Tag(KindSecret, "a@b.io") {
		t.Fatal("placeholder must depend on kind and value only")
	}
	other := NewWithKey(bytes.Repeat([]byte{8}, 32), 0)
	if other.Tag(KindEmail, "a@b.io") == p {
		t.Fatal("placeholder must depend on the key")
	}
}

func TestKeyFileCreatedOnceWith0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "nexus.redact.key")
	m1, err := New(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v %v", st, err)
	}
	m2, err := New(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Tag(KindEmail, "x@y.io") != m2.Tag(KindEmail, "x@y.io") {
		t.Fatal("key not reused across restarts")
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, 0); err == nil {
		t.Fatal("a short key file must be an error (fail closed)")
	}
}

func TestLRUEvicts(t *testing.T) {
	m := NewWithKey(bytes.Repeat([]byte{1}, 32), 2)
	a, _, _ := m.MaskText("a1@corp-a.com", Policy{})
	m.MaskText("b1@corp-b.com", Policy{})
	m.MaskText("a1@corp-a.com", Policy{}) // touch a
	m.MaskText("c1@corp-c.com", Policy{})
	if m.Size() != 2 {
		t.Fatalf("size %d", m.Size())
	}
	if _, ok := m.Value(a); !ok {
		t.Fatal("recently used value evicted")
	}
	if _, ok := m.Value(m.Tag(KindEmail, "b1@corp-b.com")); ok {
		t.Fatal("least recently used value kept")
	}
}

func TestMaskJSONSkipsProtocolFields(t *testing.T) {
	m := testMasker(t)
	body := `{"model":"sk-proj-abcDEF1234567890abcdefXYZ","messages":[` +
		`{"role":"user","content":"my key is ` + anthKey + ` and mail me at jo@quill.dev","name":"jo@quill.dev"},` +
		`{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,c2stcHJvai1hYmNERUYxMjM0NTY3ODkwYWJjZGVmWFla"}}]},` +
		`{"role":"tool","tool_call_id":"call_4111111111111111","content":"ok"}],` +
		`"tools":[{"type":"function","function":{"name":"send_mail","description":"Send to jo@quill.dev","parameters":{"type":"object","properties":{"to":{"type":"string","default":"jo@quill.dev"}}}}}],` +
		`"metadata":{"user_id":"jo@quill.dev"}}`
	out, hits, err := m.MaskJSON([]byte(body), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid JSON %s", out)
	}
	var doc map[string]any
	_ = json.Unmarshal(out, &doc)
	s := string(out)
	email := m.Tag(KindEmail, "jo@quill.dev")
	if strings.Contains(s, anthKey) || !strings.Contains(s, "and mail me at "+email) {
		t.Fatalf("content not masked: %s", s)
	}
	for _, keep := range []string{`"model":"sk-proj-`, `"name":"jo@quill.dev"`, `"tool_call_id":"call_4111111111111111"`,
		`"url":"data:image/png`, `"default":"jo@quill.dev"`, `"user_id":"jo@quill.dev"`, `"name":"send_mail"`} {
		if !strings.Contains(s, keep) {
			t.Errorf("%s was changed: %s", keep, s)
		}
	}
	if !strings.Contains(s, `"description":"Send to `+email+`"`) {
		t.Errorf("tool description not masked: %s", s)
	}
	if Masked(hits) != 3 {
		t.Errorf("masked %d, want 3: %+v", Masked(hits), hits)
	}
}

func TestMaskJSONToolArguments(t *testing.T) {
	m := testMasker(t)
	// Anthropic tool_use input: keys are the tool's own, a name there is masked; a field named
	// for a secret is masked whole.
	body := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"deploy",` +
		`"input":{"name":"jo@quill.dev","api_key":"plainopaquevalue","path":"/tmp/x"}}]}]}`
	out, _, err := m.MaskJSON([]byte(body), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "jo@quill.dev") || strings.Contains(s, "plainopaquevalue") || !strings.Contains(s, `"name":"deploy"`) {
		t.Fatalf("tool input: %s", s)
	}
	if !strings.Contains(s, m.Tag(KindSecret, "plainopaquevalue")) {
		t.Fatalf("named secret not masked by name: %s", s)
	}

	// OpenAI Chat function.arguments is JSON text: a value with a quote stays valid JSON.
	secret := `pa"ss` + "\\" + `word9X`
	inner, _ := json.Marshal(map[string]string{"password": secret, "to": "jo@quill.dev"})
	outer, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{
		"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_1", "type": "function",
			"function": map[string]any{"name": "login", "arguments": string(inner)}}}}}})
	out, _, err = m.MaskJSON(outer, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Messages []struct {
			ToolCalls []struct {
				Function struct{ Arguments string } `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	args := doc.Messages[0].ToolCalls[0].Function.Arguments
	var argDoc map[string]string
	if err := json.Unmarshal([]byte(args), &argDoc); err != nil {
		t.Fatalf("arguments no longer JSON: %q", args)
	}
	if argDoc["password"] != m.Tag(KindSecret, secret) || argDoc["to"] != m.Tag(KindEmail, "jo@quill.dev") {
		t.Fatalf("arguments: %v", argDoc)
	}
	// and the restored arguments are the original JSON text again
	if got := m.Restore(args, true); got != string(inner) {
		t.Fatalf("restore: %q want %q", got, inner)
	}
}

func TestMaskJSONSignedBlocksOnlyGetKnownValuesBack(t *testing.T) {
	m := testMasker(t)
	// first turn: the address was masked, the model thought about the placeholder
	if _, _, err := m.MaskJSON([]byte(`{"messages":[{"role":"user","content":"jo@quill.dev"}]}`), Policy{}); err != nil {
		t.Fatal(err)
	}
	p := m.Tag(KindEmail, "jo@quill.dev")
	// next turn: the client replays the thinking block with the value restored
	body := `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"write to jo@quill.dev, cc ann@quill.dev",` +
		`"signature":"EqQBCkYIBRgCKkB` + strings.Repeat("A", 40) + `"}]}]}`
	out, _, err := m.MaskJSON([]byte(body), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "write to "+p+", cc ann@quill.dev") {
		t.Fatalf("signed block: %s", s)
	}
	if !strings.Contains(s, `"signature":"EqQBCkYIBRgCKkB`) {
		t.Fatalf("signature changed: %s", s)
	}
}

func TestModesAndAllow(t *testing.T) {
	m := testMasker(t)
	text := "mail jo@quill.dev and ops@mycorp.com with " + ghToken
	pol := Policy{
		Modes: map[string]Mode{KindAPIKey: ModeWarn},
		Allow: []string{"@mycorp.com"},
	}
	out, hits, _ := m.MaskText(text, pol)
	if !strings.Contains(out, ghToken) || !strings.Contains(out, "ops@mycorp.com") || strings.Contains(out, "jo@quill.dev") {
		t.Fatalf("out %q", out)
	}
	if len(hits) != 2 || hits[1].Action != ModeWarn || Blocked(hits) {
		t.Fatalf("hits %+v", hits)
	}
	_, hits, _ = m.MaskText(text, Policy{Modes: map[string]Mode{KindAPIKey: ModeBlock}})
	if !Blocked(hits) {
		t.Fatal("block mode not reported")
	}
	out, hits, _ = m.MaskText(text, Policy{Modes: map[string]Mode{KindEmail: ModeOff, KindAPIKey: ModeOff}})
	if out != text || len(hits) != 0 {
		t.Fatalf("off mode: %q %+v", out, hits)
	}
}

func TestRestoreTolerant(t *testing.T) {
	m := testMasker(t)
	_, _, _ = m.MaskText("jo@quill.dev", Policy{})
	p := m.Tag(KindEmail, "jo@quill.dev")
	body := strings.TrimSuffix(strings.TrimPrefix(p, "{{"), "}}")
	for _, in := range []string{p, "{{ " + body + " }}", body, strings.ToLower(body), "{" + body + "}", "{{" + body + "}"} {
		if got := m.Restore("to "+in+".", false); got != "to jo@quill.dev." {
			t.Errorf("%q → %q", in, got)
		}
	}
	if got := m.Restore("x_"+body, false); got != "x_"+body {
		t.Errorf("inside a word: %q", got)
	}
	if got := m.Restore("{{EMAIL_aaaaaaaa}}", false); got != "{{EMAIL_aaaaaaaa}}" {
		t.Errorf("unknown placeholder changed: %q", got)
	}
}

func TestRestoreJSONEscapesInArguments(t *testing.T) {
	m := testMasker(t)
	secret := `q"uo\te9Xabc`
	maskToolSecret(t, m, secret)
	p := m.Tag(KindSecret, secret)
	if _, ok := m.Value(p); !ok {
		t.Fatalf("secret %q not masked", secret)
	}
	args, _ := json.Marshal(map[string]string{"pw": p})
	resp, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
		"content": "pw is " + p, "tool_calls": []any{map[string]any{"function": map[string]any{"arguments": string(args)}}}}}}})
	out := m.RestoreJSON(resp)
	var doc struct {
		Choices []struct {
			Message struct {
				Content   string
				ToolCalls []struct {
					Function struct{ Arguments string }
				} `json:"tool_calls"`
			}
		}
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if doc.Choices[0].Message.Content != "pw is "+secret {
		t.Fatalf("content %q", doc.Choices[0].Message.Content)
	}
	var a map[string]string
	if err := json.Unmarshal([]byte(doc.Choices[0].Message.ToolCalls[0].Function.Arguments), &a); err != nil || a["pw"] != secret {
		t.Fatalf("arguments %q (%v)", doc.Choices[0].Message.ToolCalls[0].Function.Arguments, err)
	}
}

func TestMaskJSONPanicsBecomeErrors(t *testing.T) {
	m := testMasker(t)
	m.byP = nil // put writes into it: a panic inside the walk
	_, _, err := m.MaskJSON([]byte(`{"messages":[{"role":"user","content":"jo@quill.dev"}]}`), Policy{})
	if err == nil {
		t.Fatal("a panic while masking must be an error")
	}
	if _, _, err := testMasker(t).MaskJSON([]byte(`not json`), Policy{}); err != ErrNotJSON {
		t.Fatalf("err %v", err)
	}
}

// maskToolSecret masks secret as a tool_use input's password field (named secrets are masked
// whole, so the value may hold quotes and backslashes).
func maskToolSecret(t *testing.T, m *Masker, secret string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "tool_use", "id": "t1", "name": "login", "input": map[string]any{"password": secret}}}}}})
	if _, _, err := m.MaskJSON(body, Policy{}); err != nil {
		t.Fatal(err)
	}
}
