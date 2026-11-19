package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/modelmeta"
)

func TestSpendCapsForCallersAndUpstreamKeys(t *testing.T) {
	srv := setupStreamServer(t, okScript, okScript, nil)
	if err := srv.DB.AutoMigrate(&models.UsageRecord{}, &models.SpendCap{}); err != nil {
		t.Fatal(err)
	}
	srv.EnableUsageLedger(0)
	srv.Meta = modelmeta.New(modelmeta.Options{Disabled: true, Overrides: func(string, string) (modelmeta.Meta, bool) {
		return modelmeta.Meta{Input: 1e6, Output: 1e6, HasPrice: true}, true // $1 per token: one call spends $4
	}})
	asCaller := func(req *http.Request) *http.Request {
		return req.WithContext(clientkey.WithCaller(req.Context(), &clientkey.Caller{ID: 7, Name: "pkm-app"}))
	}
	call := func() *httptest.ResponseRecorder {
		req := asCaller(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
		rec := httptest.NewRecorder()
		srv.HandleOpenAIChat(rec, req)
		return rec
	}

	srv.DB.Create(&models.SpendCap{Subject: "caller:pkm-app", Period: "day", USD: 3})
	srv.loadCaps()
	if rec := call(); rec.Code != 200 {
		t.Fatalf("first call under the cap must pass, got %d %s", rec.Code, rec.Body.String())
	}
	srv.Ledger.Flush()
	srv.caps.memo = map[string]capSpend{} // skip the memo window
	rec := call()
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "gateway_key_limit") || rec.Header().Get("Retry-After") == "" || rec.Header().Get("X-Nexus-Limit-Reset") == "" {
		t.Fatalf("expected 429 over the caller cap, got %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}

	// Upstream key cap: provider a's only key is capped -> failover to b, no cooldown on a.
	srv.DB.Where("1 = 1").Delete(&models.SpendCap{})
	pa, _ := srv.Registry.Get("a")
	fp := keyFingerprint(pa.Keys.Current())[:12]
	srv.DB.Create(&models.SpendCap{Subject: "key:a#" + fp, Period: "month", USD: 1})
	srv.loadCaps()
	rec = call()
	if rec.Code != 200 || rec.Header().Get("X-Nexus-Provider") != "b" {
		t.Fatalf("expected failover to b when a's key is capped, got %d %q", rec.Code, rec.Header().Get("X-Nexus-Provider"))
	}
	if cooling, _, _ := srv.Cooldown.IsCoolingDown("a"); cooling {
		t.Fatal("a capped key must not cool the provider down")
	}
}
