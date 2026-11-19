package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/modelmeta"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

func sessionCall(srv *Server, session, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("X-Nexus-Session", session)
	}
	rec := httptest.NewRecorder()
	srv.HandleOpenAIChat(rec, req)
	return rec
}

const plainBody = `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestRotateStrategyAndCacheAffinity(t *testing.T) {
	srv := setupStreamServer(t, okScript, okScript, nil)
	if err := db.SetModelPolicy(srv.DB, "m", "rotate"); err != nil {
		t.Fatal(err)
	}
	db.RefreshModelRouteCache(srv.DB)
	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, sessionCall(srv, "", plainBody).Header().Get("X-Nexus-Provider"))
	}
	if strings.Join(got, ",") != "a,b,a,b" {
		t.Fatalf("rotate must alternate, got %v", got)
	}

	// A conversation whose last answer from b left a warm cache sticks to b on the next turn.
	_ = db.SetModelPolicy(srv.DB, "m", "failover")
	db.RefreshModelRouteCache(srv.DB)
	srv.observeRouting("conv-1", "m", "b", ir.Usage{CacheReadTokens: 4000})
	if p := sessionCall(srv, "conv-1", plainBody).Header().Get("X-Nexus-Provider"); p != "b" {
		t.Fatalf("expected cache affinity to b, got %q", p)
	}
	// Without a worthwhile cache it does not stick (primary a wins).
	srv.observeRouting("conv-2", "m", "b", ir.Usage{CacheReadTokens: 10})
	if p := sessionCall(srv, "conv-2", plainBody).Header().Get("X-Nexus-Provider"); p != "a" {
		t.Fatalf("expected no affinity for a cold cache, got %q", p)
	}
}

func TestVisionRequestsPreferVisionModels(t *testing.T) {
	srv := setupStreamServer(t, okScript, okScript, nil)
	yes, no := true, false
	srv.Meta = modelmeta.New(modelmeta.Options{Disabled: true, Overrides: func(p, m string) (modelmeta.Meta, bool) {
		if p == "a" {
			return modelmeta.Meta{Vision: &no, Context: 128000}, true
		}
		return modelmeta.Meta{Vision: &yes, Context: 128000}, true
	}})
	img := `{"model":"m","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`
	if p := sessionCall(srv, "", img).Header().Get("X-Nexus-Provider"); p != "b" {
		t.Fatalf("image request must go to the vision model first, got %q", p)
	}
	if p := sessionCall(srv, "", plainBody).Header().Get("X-Nexus-Provider"); p != "a" {
		t.Fatalf("text request keeps priority order, got %q", p)
	}
}

func TestClassifierRefinements(t *testing.T) {
	cases := []struct {
		ge       *ir.GatewayError
		failover bool
	}{
		{ir.NewError(400, "invalid_request_error", "This model's maximum context length is 128000 tokens"), false},
		{ir.NewError(413, "", "prompt is too long"), false},
		{ir.NewError(400, "", "Insufficient Balance"), true},
		{ir.NewError(400, "", "model not found: foo"), true},
		{ir.NewError(422, "", "field required: messages"), false},
		{ir.NewError(400, "", "invalid tool schema"), false},
	}
	for _, c := range cases {
		if act, _ := router.ClassifyGatewayError(c.ge); act.Failover != c.failover {
			t.Errorf("%q: failover=%v want %v", c.ge.Message, act.Failover, c.failover)
		}
	}
}
