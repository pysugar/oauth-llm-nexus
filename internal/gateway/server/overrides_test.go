package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

const (
	envKeyA = "sk-env-aaaa1111"
	envKeyB = "sk-env-bbbb2222"
	uiKeyC  = "sk-ui-cccc3333"
)

func newOverrideTestProvider() *provider.Provider {
	return &provider.Provider{
		ID:        "openrouter",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.invalid/v1", Anthropic: "http://mock.invalid"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoAnthropic},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, envKeyA, envKeyB),
	}
}

func patchProvider(t *testing.T, srv *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	rtr := chi.NewRouter()
	rtr.Patch("/api/gateway/providers/{id}", srv.HandleUpdateProvider)
	rtr.Delete("/api/gateway/providers/{id}/overrides", srv.HandleClearProviderOverrides)
	method := http.MethodPatch
	path := "/api/gateway/providers/" + id
	if body == "" {
		method = http.MethodDelete
		path += "/overrides"
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, req)
	return rec
}

func TestProviderOverridesPersistAcrossRestart(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Config{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Account{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	noNet := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusServiceUnavailable)
		return rec.Result(), nil
	})}

	srv := New(provider.NewRegistryWithProviders(noNet, newOverrideTestProvider()), gdb, nil, LegacyHandlers{})
	rec := patchProvider(t, srv, "openrouter", `{"enabled":false,"free_only":false,"prefer":"anthropic","key_strategy":"rr","add_api_key":"`+uiKeyC+`","remove_api_key":"sk-e...2222"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"persisted_override":true`) {
		t.Fatalf("patch failed (%d): %s", rec.Code, rec.Body.String())
	}

	var stored models.Config
	if err := gdb.Where("key = ?", providerOverridesConfigKey).First(&stored).Error; err != nil {
		t.Fatalf("override row missing: %v", err)
	}
	if strings.Contains(stored.Value, envKeyB) {
		t.Fatalf("removed env key must be stored as fingerprint only: %s", stored.Value)
	}

	// Simulated restart: fresh registry from the same env/config defaults.
	p2 := newOverrideTestProvider()
	reg2 := provider.NewRegistryWithProviders(noNet, p2)
	applied, err := ApplyPersistedProviderOverrides(gdb, reg2)
	if err != nil || len(applied) != 1 || applied[0] != "openrouter" {
		t.Fatalf("apply overrides: applied=%v err=%v", applied, err)
	}
	if p2.IsEnabled() || p2.IsFreeOnly() || p2.PreferredProtocol() != ir.ProtoAnthropic || p2.GetKeyStrategy() != provider.KeyStrategyRoundRobin {
		t.Fatalf("flags not restored: enabled=%v free=%v prefer=%s strategy=%s",
			p2.IsEnabled(), p2.IsFreeOnly(), p2.PreferredProtocol(), p2.GetKeyStrategy())
	}
	if got := strings.Join(p2.Keys.MaskedKeys(), ","); got != "sk-e...1111,sk-u...3333" {
		t.Fatalf("unexpected restored key set: %s", got)
	}

	// Removing a UI-added key drops it from the stored list instead of fingerprinting it.
	srv2 := New(reg2, gdb, nil, LegacyHandlers{})
	if rec := patchProvider(t, srv2, "openrouter", `{"remove_api_key":"sk-u...3333"}`); rec.Code != http.StatusOK {
		t.Fatalf("remove ui key failed: %s", rec.Body.String())
	}
	all, _ := loadProviderOverrides(gdb)
	if o := all["openrouter"]; o == nil || len(o.AddedKeys) != 0 || len(o.RemovedKeys) != 1 {
		t.Fatalf("unexpected override after removing ui key: %+v", o)
	}

	// DELETE forgets persisted state; next restart falls back to defaults.
	if rec := patchProvider(t, srv2, "openrouter", ""); rec.Code != http.StatusOK {
		t.Fatalf("clear overrides failed: %s", rec.Body.String())
	}
	p3 := newOverrideTestProvider()
	if applied, _ := ApplyPersistedProviderOverrides(gdb, provider.NewRegistryWithProviders(noNet, p3)); len(applied) != 0 {
		t.Fatalf("expected no overrides after clear, got %v", applied)
	}
	if !p3.IsEnabled() || p3.Keys.Count() != 2 {
		t.Fatalf("expected defaults after clear: enabled=%v keys=%d", p3.IsEnabled(), p3.Keys.Count())
	}
}
