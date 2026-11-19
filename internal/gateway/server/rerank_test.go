package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

func TestRerankAndVoyageEmbeddings(t *testing.T) {
	gdb, _ := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	_ = gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{})
	db.RefreshModelRouteCache(gdb)

	var seen []map[string]any
	var paths []string
	client := &http.Client{Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		seen = append(seen, m)
		paths = append(paths, req.URL.Path)
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(req.URL.Path, "/rerank") {
			_, _ = rec.WriteString(`{"object":"list","data":[{"index":1,"relevance_score":0.9}],"model":"rerank-2.5","usage":{"total_tokens":12}}`)
		} else {
			_, _ = rec.WriteString(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"total_tokens":3}}`)
		}
		return rec.Result(), nil
	})}
	preset, _ := provider.GetPreset("voyage")
	voy := &provider.Provider{
		ID: "voyage", Preset: "voyage", Enabled: true, NoChat: true,
		Endpoints:    provider.Endpoints{OpenAIChat: "http://mock.voyage/v1"},
		StaticModels: preset.StaticModels,
		Quirks:       codec.Quirks{EmbeddingInputType: true},
		Keys:         provider.NewKeyRing(false, "voy-key"),
	}
	srv := New(provider.NewRegistryWithProviders(client, voy), gdb, nil, LegacyHandlers{})

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"rerank-2.5","query":"what eats seeds","documents":["a stone","a magpie"],"top_k":1}`))
	rec := httptest.NewRecorder()
	srv.HandleRerank(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "relevance_score") || paths[0] != "/v1/rerank" {
		t.Fatalf("rerank failed: %d %s %v", rec.Code, rec.Body.String(), paths)
	}
	if snap := srv.Metrics.ProviderSnapshot("voyage"); snap == nil || snap.InputTokens != 12 {
		t.Fatalf("rerank usage not recorded: %+v", snap)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"voyage-4","input":["n"],"input_type":"passage","dimensions":512,"encoding_format":"float"}`))
	rec = httptest.NewRecorder()
	srv.HandleOpenAIEmbeddings(rec, req)
	got := seen[len(seen)-1]
	if rec.Code != 200 || got["input_type"] != "document" || got["output_dimension"] != float64(512) || got["dimensions"] != nil || got["encoding_format"] != nil {
		t.Fatalf("voyage embedding mapping wrong: %d %v", rec.Code, got)
	}

	// Voyage never serves chat.
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"voyage-4","messages":[{"role":"user","content":"hi"}]}`))
	rec = httptest.NewRecorder()
	srv.HandleOpenAIChat(rec, req)
	if rec.Code == 200 {
		t.Fatalf("voyage must not serve chat: %s", rec.Body.String())
	}
	if _, _, ok := voy.TargetFor(ir.ProtoOpenAIChat); ok {
		t.Fatal("NoChat provider must have no chat target")
	}
}
