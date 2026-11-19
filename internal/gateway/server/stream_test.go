package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

const (
	chatContent = "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n"
	chatRole    = "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"
	chatFinish  = "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n"
	chatDone    = "data: [DONE]\n\n"
)

// streamScript describes one upstream SSE reply: chunks written in order, each after its delay.
type streamScript struct {
	chunks []string
	delays []time.Duration
}

func (s streamScript) body() io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		for i, c := range s.chunks {
			if i < len(s.delays) && s.delays[i] > 0 {
				time.Sleep(s.delays[i])
			}
			if _, err := pw.Write([]byte(c)); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	return pr
}

// setupStreamServer registers providers "a" (primary) and "b" (secondary) serving model "m",
// each answering with its script; protocol is OpenAI Chat upstream.
func setupStreamServer(t *testing.T, a, b streamScript, mutate func(pa, pb *provider.Provider)) *Server {
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
		script := a
		if req.URL.Host == "b.test" {
			script = b
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       script.body(),
			Request:    req,
		}, nil
	})}
	mk := func(id string) *provider.Provider {
		return &provider.Provider{
			ID: id, Enabled: true, Timeout: 5 * time.Second,
			Endpoints: provider.Endpoints{OpenAIChat: "http://" + id + ".test/v1"},
			Native:    []ir.Protocol{ir.ProtoOpenAIChat},
			Prefer:    ir.ProtoOpenAIChat,
			Keys:      provider.NewKeyRing(false, id+"-key"),
		}
	}
	pa, pb := mk("a"), mk("b")
	if mutate != nil {
		mutate(pa, pb)
	}
	srv := New(provider.NewRegistryWithProviders(client, pa, pb), gdb, nil, LegacyHandlers{})
	srv.Stream = &StreamPolicy{
		KeepaliveAfter: 150 * time.Millisecond, KeepaliveEvery: 150 * time.Millisecond,
		AnthropicPingEvery: time.Minute, HoldMax: 3 * time.Second, HoldMaxBytes: 1 << 20,
	}
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{ClientModel: "m", TargetProvider: "a", TargetModel: "m", Priority: 0, IsActive: true})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{ClientModel: "m", TargetProvider: "b", TargetModel: "m", Priority: 1, IsActive: true})
	return srv
}

func chatCall(srv *Server, stream bool) *httptest.ResponseRecorder {
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]` + map[bool]string{true: `,"stream":true}`, false: `}`}[stream]
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.HandleOpenAIChat(rec, req)
	return rec
}

func anthCall(srv *Server, stream bool) *httptest.ResponseRecorder {
	body := `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]` + map[bool]string{true: `,"stream":true}`, false: `}`}[stream]
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.HandleAnthropicMessages(rec, req)
	return rec
}

var okScript = streamScript{chunks: []string{chatContent, chatFinish, chatDone}}

func TestTruncatedStreamFailsOverBeforeCommit(t *testing.T) {
	truncated := streamScript{chunks: []string{chatRole}} // ends with no finish_reason and no [DONE]
	// Translated non-stream client (Anthropic -> OpenAI Chat upstream).
	srv := setupStreamServer(t, truncated, okScript, nil)
	rec := anthCall(srv, false)
	if rec.Code != 200 || rec.Header().Get("X-Nexus-Provider") != "b" || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected failover to b, got %d %q %s", rec.Code, rec.Header().Get("X-Nexus-Provider"), rec.Body.String())
	}
	// Passthrough non-stream client with stream-collect off: upstream still streams for stream clients.
	rec = chatCall(srv, true)
	if rec.Header().Get("X-Nexus-Provider") != "b" || !strings.Contains(rec.Body.String(), "hello") || strings.Count(rec.Body.String(), "[DONE]") != 1 {
		t.Fatalf("expected passthrough stream from b, got %q %s", rec.Header().Get("X-Nexus-Provider"), rec.Body.String())
	}
}

func TestEmptyReplyFailsOver(t *testing.T) {
	empty := streamScript{chunks: []string{chatRole, chatFinish, chatDone}}
	srv := setupStreamServer(t, empty, okScript, nil)
	rec := chatCall(srv, true)
	if rec.Header().Get("X-Nexus-Provider") != "b" || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected empty reply to fail over, got %q %s", rec.Header().Get("X-Nexus-Provider"), rec.Body.String())
	}
	if strings.Count(rec.Body.String(), `"role":"assistant"`) != 1 {
		t.Fatalf("held role chunk of the failed attempt must not leak: %s", rec.Body.String())
	}
}

func TestPostCommitTruncationIsAnInStreamError(t *testing.T) {
	cut := streamScript{chunks: []string{chatContent}}
	srv := setupStreamServer(t, cut, okScript, nil)
	rec := anthCall(srv, true) // translated stream
	out := rec.Body.String()
	if !strings.Contains(out, "hello") || !strings.Contains(out, "event: error") || strings.Contains(out, "message_stop") {
		t.Fatalf("expected in-stream error without fabricated message_stop, got %s", out)
	}
	rec = chatCall(srv, true) // passthrough stream
	out = rec.Body.String()
	if !strings.Contains(out, "upstream_incomplete") || strings.Contains(out, "[DONE]") {
		t.Fatalf("expected passthrough in-stream error without [DONE], got %s", out)
	}
}

func TestKeepalivesWhileHeldAndFirstTokenTimeout(t *testing.T) {
	slow := streamScript{chunks: []string{chatContent, chatFinish, chatDone}, delays: []time.Duration{600 * time.Millisecond}}
	srv := setupStreamServer(t, slow, okScript, nil)
	rec := chatCall(srv, true)
	out := rec.Body.String()
	if !strings.HasPrefix(out, ": keepalive\n") || strings.Contains(out, "keepalive\n\n") {
		t.Fatalf("expected comment-line keepalives without blank lines, got %q", out)
	}
	if !strings.Contains(out, "hello") || rec.Code != 200 {
		t.Fatalf("expected content after keepalives, got %d %q", rec.Code, out)
	}

	// Anthropic clients get event: ping (once per window); Gemini clients get no keepalive bytes.
	rec = anthCall(srv, true)
	if strings.Count(rec.Body.String(), `{"type": "ping"}`) != 1 || !strings.HasPrefix(rec.Body.String(), "event: ping") {
		t.Fatalf("expected exactly one ping, got %q", rec.Body.String())
	}

	// first_token_timeout fails over to b even though a would answer eventually.
	srv = setupStreamServer(t, slow, okScript, func(pa, _ *provider.Provider) { pa.FirstTokenTimeout = 200 * time.Millisecond })
	rec = chatCall(srv, true)
	if !strings.Contains(rec.Body.String(), "hello") || rec.Header().Get("X-Nexus-Provider") == "a" {
		t.Fatalf("expected first-token failover, got %q %s", rec.Header().Get("X-Nexus-Provider"), rec.Body.String())
	}

	// All candidates failing after keepalives were sent: 200 with an in-stream error.
	bad := streamScript{chunks: []string{chatRole}, delays: []time.Duration{400 * time.Millisecond}}
	srv = setupStreamServer(t, bad, bad, nil)
	rec = chatCall(srv, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "keepalive") || !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("expected in-stream error after keepalives, got %d %q", rec.Code, rec.Body.String())
	}
}
