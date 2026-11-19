package modelmeta

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

func TestLookupFetchCacheAndOverrides(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/api.json")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "models.dev.json")

	r := New(Options{CachePath: cache, URL: srv.URL})
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, ok := r.Lookup("openai", "openai", "gpt-5")
	if !ok || m.Input != 1.25 || m.Context != 400000 || m.Vision == nil || !*m.Vision {
		t.Fatalf("unexpected exact lookup %+v %v", m, ok)
	}
	// A relay without its own entry falls back to the canonical first-party listing.
	if m, ok := r.Lookup("nvidia", "nvidia", "deepseek-ai/deepseek-reasoner"); !ok || m.Input != 0.28 {
		t.Fatalf("expected canonical deepseek price, got %+v %v", m, ok)
	}
	if m, _ := r.Lookup("openrouter", "openrouter", "openai/gpt-5"); m.Input != 1.3 {
		t.Fatalf("provider listing must win over canonical, got %+v", m)
	}
	if m, ok := r.Lookup("ollama", "ollama", "qwen3"); !ok || !m.HasPrice || m.Input != 0 {
		t.Fatalf("ollama is free, got %+v", m)
	}

	// Cost: 1000 uncached in, 500 cached, 100 out at gpt-5 prices.
	cost := m0(r).Cost(ir.Usage{InputTokens: 1500, CacheReadTokens: 500, OutputTokens: 100})
	want := (1000*1.25 + 500*0.125 + 100*10) / 1e6
	if math.Abs(cost-want) > 1e-12 {
		t.Fatalf("cost %v want %v", cost, want)
	}

	// Cache file is reused without network; overrides win.
	r2 := New(Options{CachePath: cache, Disabled: true, Overrides: func(p, model string) (Meta, bool) {
		if model == "gpt-5" {
			return Meta{Input: 9, HasPrice: true, Source: "override"}, true
		}
		return Meta{}, false
	}})
	if m, _ := r2.Lookup("openai", "openai", "gpt-5"); m.Input != 9 {
		t.Fatalf("override must win, got %+v", m)
	}
	if m, ok := r2.Lookup("deepseek", "deepseek", "deepseek-reasoner"); !ok || m.Output != 0.42 {
		t.Fatalf("expected cached entry without network, got %+v", m)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected one fetch, got %d", hits.Load())
	}
	if err := r2.Refresh(context.Background()); err == nil {
		t.Fatal("disabled registry must not fetch")
	}
}

func m0(r *Registry) Meta {
	m, _ := r.Lookup("openai", "openai", "gpt-5")
	return m
}
