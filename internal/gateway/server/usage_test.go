package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/modelmeta"
)

func TestUsageLedgerRecordsAndPricesCalls(t *testing.T) {
	srv := setupStreamServer(t, okScript, okScript, nil)
	if err := srv.DB.AutoMigrate(&models.UsageRecord{}); err != nil {
		t.Fatal(err)
	}
	srv.EnableUsageLedger(0)
	srv.Meta = modelmeta.New(modelmeta.Options{Disabled: true, Overrides: func(p, m string) (modelmeta.Meta, bool) {
		return modelmeta.Meta{Input: 1000, Output: 2000, HasPrice: true}, p == "a"
	}})

	if rec := chatCall(srv, true); rec.Code != 200 {
		t.Fatalf("chat failed %d", rec.Code)
	}
	if rec := anthCall(srv, false); rec.Code != 200 {
		t.Fatalf("anthropic failed %d", rec.Code)
	}
	srv.Ledger.Flush()

	var rows []models.UsageRecord
	srv.DB.Order("id").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("expected 2 ledger rows, got %d", len(rows))
	}
	if r := rows[0]; r.Provider != "a" || r.Kind != "chat" || r.Route != "passthrough" || r.KeyFP == "" || r.InputTokens != 3 || r.SessionID == "" {
		t.Fatalf("unexpected stream row %+v", r)
	}
	if r := rows[1]; r.Route != "translated:openai-chat" || r.Proto != "anthropic" || r.OutputTokens != 1 {
		t.Fatalf("unexpected translated row %+v", r)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/gateway/usage?group_by=model&from=1h", nil)
	rec := httptest.NewRecorder()
	srv.HandleUsage(rec, req)
	var out struct {
		Summary struct {
			Total struct {
				Requests int64   `json:"requests"`
				CostUSD  float64 `json:"cost_usd"`
			} `json:"total"`
		} `json:"summary"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	want := 2 * (3*1000 + 1*2000) / 1e6
	if out.Summary.Total.Requests != 2 || math.Abs(out.Summary.Total.CostUSD-want) > 1e-9 {
		t.Fatalf("unexpected summary %s (want cost %v)", rec.Body.String(), want)
	}
}
