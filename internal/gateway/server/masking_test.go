package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/redact"
	"gorm.io/gorm"
)

const (
	privEmail = "jo@quill.dev"
	privKey   = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
)

var emailPH = regexp.MustCompile(`\{\{EMAIL_[a-z2-7]{8}\}\}`)

// upstreamCapture records what upstream got and answers with reply(body).
type upstreamCapture struct {
	mu     sync.Mutex
	bodies []string
	reply  func(body string) (contentType string, chunks []string)
}

func (u *upstreamCapture) last() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return ""
	}
	return u.bodies[len(u.bodies)-1]
}

func (u *upstreamCapture) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

// setupMaskServer serves model "m" from provider "a" (OpenAI Chat upstream) with masking on.
func setupMaskServer(t *testing.T, up *upstreamCapture, mutate func(*provider.Provider)) *Server {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{}); err != nil {
		t.Fatal(err)
	}
	db.RefreshModelRouteCache(gdb)
	client := &http.Client{Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		up.mu.Lock()
		up.bodies = append(up.bodies, string(raw))
		up.mu.Unlock()
		ct, chunks := up.reply(string(raw))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}},
			Body: streamScript{chunks: chunks}.body(), Request: req}, nil
	})}
	pa := &provider.Provider{
		ID: "a", Enabled: true, Timeout: 5 * time.Second,
		Endpoints: provider.Endpoints{OpenAIChat: "http://a.test/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "a-key"),
	}
	if mutate != nil {
		mutate(pa)
	}
	srv := New(provider.NewRegistryWithProviders(client, pa), gdb, nil, LegacyHandlers{})
	srv.Stream = &StreamPolicy{KeepaliveAfter: time.Second, KeepaliveEvery: time.Second,
		AnthropicPingEvery: time.Minute, HoldMax: 3 * time.Second, HoldMaxBytes: 1 << 20}
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{ClientModel: "m", TargetProvider: "a", TargetModel: "m", IsActive: true})
	srv.EnableMasking("") // in-memory key
	on := true
	srv.masking.settings.Store(&maskSettings{Enabled: &on})
	return srv
}

func sseData(v any) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

func chatChunk(delta map[string]any) string {
	return sseData(map[string]any{"id": "c1", "model": "m", "choices": []any{map[string]any{"index": 0, "delta": delta}}})
}

func post(srv *Server, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	switch path {
	case "/v1/messages":
		srv.HandleAnthropicMessages(rec, req)
	default:
		srv.HandleOpenAIChat(rec, req)
	}
	return rec
}

const userMsg = `"messages":[{"role":"user","content":"mail ` + privEmail + ` with key ` + privKey + `"}]`

func TestMaskingNonStreamPassthroughRoundTrip(t *testing.T) {
	up := &upstreamCapture{reply: func(body string) (string, []string) {
		p := emailPH.FindString(body)
		return "application/json", []string{`{"id":"r1","object":"chat.completion","model":"m","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"Sent to ` + p + `."},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":3,"completion_tokens":2}}`}
	}}
	srv := setupMaskServer(t, up, nil)
	rec := post(srv, "/v1/chat/completions", `{"model":"m",`+userMsg+`}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := up.last()
	if strings.Contains(got, privEmail) || strings.Contains(got, privKey) || !emailPH.MatchString(got) {
		t.Fatalf("upstream saw: %s", got)
	}
	if !strings.Contains(rec.Body.String(), `"content":"Sent to `+privEmail+`."`) {
		t.Fatalf("client got: %s", rec.Body)
	}
	if rec.Header().Get("X-Nexus-Masked") != "2" {
		t.Fatalf("X-Nexus-Masked %q", rec.Header().Get("X-Nexus-Masked"))
	}
}

func TestMaskingStreamRestoresSplitPlaceholder(t *testing.T) {
	up := &upstreamCapture{reply: func(body string) (string, []string) {
		p := emailPH.FindString(body)
		return "text/event-stream", []string{
			chatChunk(map[string]any{"role": "assistant", "content": "Sent to " + p[:9]}),
			chatChunk(map[string]any{"content": p[9:] + "."}),
			chatFinish, chatDone,
		}
	}}
	srv := setupMaskServer(t, up, nil)
	rec := post(srv, "/v1/chat/completions", `{"model":"m","stream":true,`+userMsg+`}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var text strings.Builder
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		d, ok := strings.CutPrefix(line, "data: ")
		if !ok || !strings.HasPrefix(d, "{") {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
		}
		_ = json.Unmarshal([]byte(d), &c)
		if len(c.Choices) > 0 {
			text.WriteString(c.Choices[0].Delta.Content)
		}
	}
	if text.String() != "Sent to "+privEmail+"." {
		t.Fatalf("text %q\n%s", text.String(), rec.Body)
	}
}

// An Anthropic client translated onto an OpenAI Chat upstream: text and tool-call arguments
// streamed in pieces are restored in the client's own protocol, arguments JSON-escaped.
func TestMaskingTranslatedStreamRestoresTextAndToolArgs(t *testing.T) {
	up := &upstreamCapture{reply: func(body string) (string, []string) {
		p := emailPH.FindString(body)
		args := `{"to":"` + p + `","subject":"hi"}`
		cut := strings.Index(args, p) + 4
		tc := func(extra map[string]any, frag string) string {
			fn := map[string]any{"arguments": frag}
			for k, v := range extra {
				fn[k] = v
			}
			call := map[string]any{"index": 0, "function": fn}
			if extra != nil {
				call["id"], call["type"] = "call_1", "function"
			}
			return chatChunk(map[string]any{"tool_calls": []any{call}})
		}
		return "text/event-stream", []string{
			chatChunk(map[string]any{"role": "assistant", "content": "Mailing {{" + p[2:7]}),
			chatChunk(map[string]any{"content": p[7:] + " now"}),
			tc(map[string]any{"name": "send_mail"}, args[:cut]),
			tc(nil, args[cut:]),
			sseData(map[string]any{"id": "c1", "model": "m", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
			chatDone,
		}
	}}
	srv := setupMaskServer(t, up, nil)
	rec := post(srv, "/v1/messages", `{"model":"m","max_tokens":64,"stream":true,`+userMsg+`}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(up.last(), privEmail) {
		t.Fatalf("upstream saw the address: %s", up.last())
	}
	var text, args strings.Builder
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		d, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(d), &ev) != nil || ev.Type != "content_block_delta" {
			continue
		}
		text.WriteString(ev.Delta.Text)
		args.WriteString(ev.Delta.PartialJSON)
	}
	if text.String() != "Mailing "+privEmail+" now" {
		t.Fatalf("text %q\n%s", text.String(), rec.Body)
	}
	var a map[string]string
	if err := json.Unmarshal([]byte(args.String()), &a); err != nil || a["to"] != privEmail {
		t.Fatalf("tool args %q (%v)\n%s", args.String(), err, rec.Body)
	}
}

func TestMaskingFailsClosedAndBlocks(t *testing.T) {
	up := &upstreamCapture{reply: func(string) (string, []string) { return "text/event-stream", okScript.chunks }}
	srv := setupMaskServer(t, up, nil)
	on := true
	srv.masking = &maskState{err: errors.New("key unreadable")}
	srv.masking.settings.Store(&maskSettings{Enabled: &on})
	rec := post(srv, "/v1/chat/completions", `{"model":"m",`+userMsg+`}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "masking") {
		t.Fatalf("broken masker: %d %s", rec.Code, rec.Body)
	}

	srv = setupMaskServer(t, up, nil)
	srv.masking.settings.Store(&maskSettings{Enabled: &on, Modes: map[string]redact.Mode{redact.KindAPIKey: redact.ModeBlock}})
	rec = post(srv, "/v1/messages", `{"model":"m","max_tokens":8,`+userMsg+`}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "API_KEY") || strings.Contains(rec.Body.String(), privKey) {
		t.Fatalf("block: %d %s", rec.Code, rec.Body)
	}
	if up.count() != 0 {
		t.Fatalf("upstream was called %d times", up.count())
	}

	off := false
	srv.masking.settings.Store(&maskSettings{Enabled: &off})
	rec = post(srv, "/v1/chat/completions", `{"model":"m",`+userMsg+`}`)
	if rec.Code != 200 || !strings.Contains(up.last(), privEmail) {
		t.Fatalf("masking off: %d, upstream %s", rec.Code, up.last())
	}
}

func TestUnredactedProviderGetsPlainRequest(t *testing.T) {
	up := &upstreamCapture{reply: func(string) (string, []string) { return "text/event-stream", okScript.chunks }}
	srv := setupMaskServer(t, up, func(p *provider.Provider) { p.Unredacted = true })
	rec := post(srv, "/v1/chat/completions", `{"model":"m","stream":true,`+userMsg+`}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(up.last(), privEmail) || !strings.Contains(up.last(), privKey) {
		t.Fatalf("unredacted provider got a masked body: %s", up.last())
	}
}

func TestEmbeddingsNotMaskedByDefault(t *testing.T) {
	capt := &embedCapture{}
	srv, _ := setupEmbeddingServer(t, capt)
	srv.EnableMasking("")
	on := true
	srv.masking.settings.Store(&maskSettings{Enabled: &on})
	body := `{"model":"text-embedding-3-small","input":["notes for ` + privEmail + `"]}`
	hdr := map[string]string{"X-Nexus-Provider": "openai"}
	if rec := postEmbeddings(t, srv, body, hdr); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if in, _ := json.Marshal(capt.openaiBody[len(capt.openaiBody)-1]["input"]); !strings.Contains(string(in), privEmail) {
		t.Fatalf("embeddings input masked by default: %s", in)
	}
	srv.masking.settings.Store(&maskSettings{Enabled: &on, MaskEmbeddings: true})
	if rec := postEmbeddings(t, srv, body, hdr); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if in, _ := json.Marshal(capt.openaiBody[len(capt.openaiBody)-1]["input"]); strings.Contains(string(in), privEmail) {
		t.Fatalf("mask_embeddings did not mask: %s", in)
	}
}

func TestMaskingSettingsAPI(t *testing.T) {
	up := &upstreamCapture{reply: func(string) (string, []string) { return "text/event-stream", okScript.chunks }}
	srv := setupMaskServer(t, up, nil)
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.HandlePutMasking(rec, httptest.NewRequest(http.MethodPut, "/api/gateway/masking", strings.NewReader(body)))
		return rec
	}
	if rec := put(`{"modes":{"EMAIL":"sometimes"}}`); rec.Code != 400 {
		t.Fatalf("bad mode accepted: %d", rec.Code)
	}
	if rec := put(`{"enabled":true,"modes":{"email":"warn"},"allow":["@mycorp.com"]}`); rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	var rows []models.Config
	srv.DB.Where("key = ?", maskingConfigKey).Find(&rows)
	if len(rows) != 1 || !strings.Contains(rows[0].Value, `"EMAIL":"warn"`) {
		t.Fatalf("not persisted: %+v", rows)
	}
	// a restart reads it back
	srv.EnableMasking("")
	if !srv.maskingOn() || srv.maskPolicy().Modes[redact.KindEmail] != redact.ModeWarn {
		t.Fatalf("settings not reloaded: %+v", srv.maskSettings())
	}
	rec := httptest.NewRecorder()
	srv.HandleMaskingPreview(rec, httptest.NewRequest(http.MethodPost, "/api/gateway/masking/preview",
		strings.NewReader(`{"text":"key `+privKey+`"}`)))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), privKey) || !strings.Contains(rec.Body.String(), "API_KEY") {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
}
