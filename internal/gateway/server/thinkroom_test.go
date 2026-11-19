package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/modelmeta"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

func TestThinksByDefault(t *testing.T) {
	for m, want := range map[string]bool{
		"gemini-2.5-flash": true, "gemini-3-pro-preview": true, "models/gemini-4-argon": true, "gemini-2.5-flash-image": false,
		"gemini-2.0-flash": false, "o3-mini": true, "gpt-5-mini": true, "gpt-5-chat-latest": false, "gpt-4o-mini": false,
		"deepseek-reasoner": true, "deepseek/deepseek-r1:free": true, "deepseek-chat": false, "claude-opus-5-5": false,
	} {
		if got := thinksByDefault(m); got != want {
			t.Errorf("%s: thinksByDefault=%v want %v", m, got, want)
		}
	}
}

func TestSmallReplyLimitGetsThinkingRoom(t *testing.T) {
	gdb, _ := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	_ = gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{})
	db.RefreshModelRouteCache(gdb)

	var mu sync.Mutex
	var bodies []map[string]any
	emptyGemini := false
	client := &http.Client{Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		bodies = append(bodies, b)
		empty := emptyGemini
		mu.Unlock()
		rec := httptest.NewRecorder()
		if req.URL.Host == "g.test" {
			rec.Header().Set("Content-Type", "text/event-stream")
			if !empty {
				_, _ = rec.WriteString("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}]}\n\n")
			}
		} else {
			rec.Header().Set("Content-Type", "application/json")
			_, _ = rec.WriteString(`{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}
		res := rec.Result()
		res.Request = req
		return res, nil
	})}
	gem := &provider.Provider{ID: "g", Enabled: true, Endpoints: provider.Endpoints{Gemini: "http://g.test/v1beta"},
		Native: []ir.Protocol{ir.ProtoGemini}, Prefer: ir.ProtoGemini, Keys: provider.NewKeyRing(false, "gk")}
	oai := &provider.Provider{ID: "o", Enabled: true, Endpoints: provider.Endpoints{OpenAIChat: "http://o.test/v1"},
		Native: []ir.Protocol{ir.ProtoOpenAIChat}, Prefer: ir.ProtoOpenAIChat, Keys: provider.NewKeyRing(false, "ok")}
	srv := New(provider.NewRegistryWithProviders(client, gem, oai), gdb, nil, LegacyHandlers{})
	srv.Meta = modelmeta.New(modelmeta.Options{Disabled: true, Overrides: func(p, m string) (modelmeta.Meta, bool) {
		if m == "gemini-2.5-flash" {
			return modelmeta.Meta{MaxOutput: 8000}, true
		}
		return modelmeta.Meta{}, false
	}})
	call := func(provider, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("X-Nexus-Provider", provider)
		rec := httptest.NewRecorder()
		srv.HandleOpenAIChat(rec, req)
		return rec
	}
	lastBody := func() map[string]any { mu.Lock(); defer mu.Unlock(); return bodies[len(bodies)-1] }
	geminiLimit := func() any { return lastBody()["generationConfig"].(map[string]any)["maxOutputTokens"] }

	// Translated to Gemini: 10 + room, capped at the model's own output limit (8000).
	if rec := call("g", `{"model":"gemini-2.5-flash","max_tokens":10,"messages":[{"role":"user","content":"Say ok"}]}`); rec.Code != 200 || geminiLimit() != float64(8000) {
		t.Fatalf("expected room capped at 8000, got %d %v", rec.Code, geminiLimit())
	}
	// The client chose its reasoning: its limit goes as sent.
	call("g", `{"model":"gemini-2.5-flash","max_tokens":10,"reasoning_effort":"low","messages":[{"role":"user","content":"Say ok"}]}`)
	if geminiLimit() != float64(10) {
		t.Fatalf("explicit reasoning must keep the limit, got %v", geminiLimit())
	}
	// Passthrough to a reasoning model: the field the client used is raised (no cap known).
	call("o", `{"model":"gpt-5-mini","max_completion_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)
	if b := lastBody(); b["max_completion_tokens"] != float64(50+16384) || b["max_tokens"] != nil {
		t.Fatalf("expected max_completion_tokens raised, got %v", b)
	}
	// A model that does not think keeps its small limit.
	call("o", `{"model":"gpt-4o-mini","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	if lastBody()["max_tokens"] != float64(5) {
		t.Fatalf("non-thinking model must keep its limit, got %v", lastBody())
	}
	// A reply that still comes back empty says what to do.
	mu.Lock()
	emptyGemini = true
	mu.Unlock()
	rec := call("g", `{"model":"gemini-2.5-flash","max_tokens":10,"messages":[{"role":"user","content":"Say ok"}]}`)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "max_tokens=10 may be too small") {
		t.Fatalf("expected an explained empty reply, got %d %s", rec.Code, rec.Body.String())
	}
}
