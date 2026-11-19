package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newMockRegistry(t *testing.T) *provider.Registry {
	t.Helper()
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			switch req.URL.Host {
			case "mock.deepseek":
				_, _ = rec.WriteString(`{"data":[{"id":"deepseek-chat"}]}`)
			case "mock.openrouter":
				_, _ = rec.WriteString(`{"data":[{"id":"deepseek/deepseek-chat"},{"id":"nvidia/nemotron-3.5-super-vl:free"},{"id":"shared-model"}]}`)
			case "mock.nvidia":
				_, _ = rec.WriteString(`{"data":[{"id":"nvidia/nemotron-3.5-super-vl-120b-a12b"},{"id":"google/gemma-4-31b-it"}]}`)
			case "mock.amd":
				_, _ = rec.WriteString(`{"data":[{"id":"shared-model"},{"id":"Gemma-4-31B-IT"}]}`)
			default:
				http.NotFound(rec, req)
			}
			return rec.Result(), nil
		}),
	}

	ds := &provider.Provider{
		ID:        "deepseek",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.deepseek/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "ds-key"),
	}
	or := &provider.Provider{
		ID:        "openrouter",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.openrouter/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "or-key"),
	}
	amd := &provider.Provider{
		ID:        "amd",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.amd/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "amd-key"),
	}
	nv := &provider.Provider{
		ID:        "nvidia",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.nvidia/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "nv-key"),
	}
	return provider.NewRegistryWithProviders(client, ds, or, nv, amd)
}

func newRouterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.RefreshModelRouteCache(gdb)
	return gdb
}

func TestRouterPrecedenceCooldownAndPolicy(t *testing.T) {
	gdb := newRouterTestDB(t)
	reg := newMockRegistry(t)
	cd := NewCooldown()
	r := New(reg, cd, gdb)

	// Explicit <provider>/<model> prefix handles nested slashes like openrouter/anthropic/claude.
	plan, ge := r.Resolve(context.Background(), "openrouter/anthropic/claude-sonnet-4.5", "")
	if ge != nil {
		t.Fatalf("prefix resolve failed: %v", ge)
	}
	if !plan.Forced || len(plan.Candidates) != 1 ||
		plan.Candidates[0].Provider.ID != "openrouter" ||
		plan.Candidates[0].UpstreamModel != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("unexpected prefix plan: %+v", plan)
	}

	// Explicit X-Nexus-Provider override ignores catalog/DB ordering and pins target backend.
	plan, ge = r.Resolve(context.Background(), "shared-model", "amd")
	if ge != nil {
		t.Fatalf("header resolve failed: %v", ge)
	}
	if !plan.Forced || plan.Candidates[0].Provider.ID != "amd" {
		t.Fatalf("expected header override to amd, got %+v", plan)
	}

	// Implicit catalog exact-name discovery in provider priority order.
	plan, ge = r.Resolve(context.Background(), "shared-model", "")
	if ge != nil {
		t.Fatalf("catalog resolve failed: %v", ge)
	}
	if len(plan.Candidates) != 2 || plan.Candidates[0].Provider.ID != "openrouter" || plan.Candidates[1].Provider.ID != "amd" {
		t.Fatalf("expected openrouter then amd via catalog order, got %+v", plan.Candidates)
	}

	// DB multi-backend routes + runtime switch + cooldown skipping.
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "reasoning-alpha", TargetProvider: "deepseek", TargetModel: "deepseek-reasoner", Priority: 1, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "reasoning-alpha", TargetProvider: "amd", TargetModel: "amd/r1", Priority: 2, IsActive: true,
	})
	cd.MarkFailure("deepseek", 429, "rate limited")

	plan, ge = r.Resolve(context.Background(), "reasoning-alpha", "")
	if ge != nil {
		t.Fatalf("failover resolve failed: %v", ge)
	}
	// Cooling backends move behind healthy ones and stay as a marked last resort.
	if len(plan.Candidates) != 2 || plan.Candidates[0].Provider.ID != "amd" ||
		plan.Candidates[1].Provider.ID != "deepseek" || !plan.Candidates[1].Cooling {
		t.Fatalf("expected amd first and cooling deepseek last; got %+v", plan)
	}

	// Clearing cooldown + pinned policy restricts to single primary candidate.
	cd.Clear("deepseek")
	_ = db.SetModelPolicy(gdb, "reasoning-alpha", models.StrategyPinned)
	plan, ge = r.Resolve(context.Background(), "reasoning-alpha", "")
	if ge != nil {
		t.Fatalf("pinned resolve failed: %v", ge)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Provider.ID != "deepseek" {
		t.Fatalf("expected pinned single deepseek candidate, got %+v", plan.Candidates)
	}

	// Unknown model returns 404 when no OAuth account fallback is present.
	if _, ge := r.Resolve(context.Background(), "does-not-exist", ""); ge == nil || ge.HTTPStatus != 404 {
		t.Fatalf("expected 404 for unknown model, got %v", ge)
	}

	// Verify cooldown expiration helper.
	now := time.Now()
	cd.nowFunc = func() time.Time { return now }
	cd.MarkFailure("amd", 500, "boom")
	if cooling, _, _ := cd.IsCoolingDown("amd"); !cooling {
		t.Fatalf("expected amd to be in cooldown")
	}
	cd.nowFunc = func() time.Time { return now.Add(5 * time.Minute) }
	if cooling, _, _ := cd.IsCoolingDown("amd"); cooling {
		t.Fatalf("expected amd cooldown to expire after 5m")
	}

	// Vendor-namespaced model on NVIDIA must preserve the full "nvidia/..." ID rather than stripping "nvidia/".
	plan, ge = r.Resolve(context.Background(), "nvidia/nemotron-3.5-super-vl-120b-a12b", "")
	if ge != nil {
		t.Fatalf("resolve nvidia vendor model failed: %v", ge)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Provider.ID != "nvidia" ||
		plan.Candidates[0].UpstreamModel != "nvidia/nemotron-3.5-super-vl-120b-a12b" {
		t.Fatalf("expected unstripped nvidia/nemotron-3.5-super-vl-120b-a12b on nvidia, got %+v", plan.Candidates)
	}

	// OpenRouter ":free" model starting with "nvidia/" must route to openrouter without prefix stripping.
	plan, ge = r.Resolve(context.Background(), "nvidia/nemotron-3.5-super-vl:free", "")
	if ge != nil {
		t.Fatalf("resolve openrouter :free vendor model failed: %v", ge)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Provider.ID != "openrouter" ||
		plan.Candidates[0].UpstreamModel != "nvidia/nemotron-3.5-super-vl:free" {
		t.Fatalf("expected nvidia/nemotron-3.5-super-vl:free on openrouter, got %+v", plan.Candidates)
	}

	// Vendor-namespaced "google/gemma-4-31b-it" must route to nvidia catalog rather than legacy google OAuth.
	plan, ge = r.Resolve(context.Background(), "google/gemma-4-31b-it", "")
	if ge != nil {
		t.Fatalf("resolve google/gemma-4-31b-it failed: %v", ge)
	}
	if plan.LegacyProvider != "" || len(plan.Candidates) != 1 ||
		plan.Candidates[0].Provider.ID != "nvidia" ||
		plan.Candidates[0].UpstreamModel != "google/gemma-4-31b-it" {
		t.Fatalf("expected google/gemma-4-31b-it on nvidia, got %+v", plan)
	}

	// Explicit "amd/Gemma-4-31B-IT" prefix strips "amd/" because "Gemma-4-31B-IT" is in amd's catalog.
	plan, ge = r.Resolve(context.Background(), "amd/Gemma-4-31B-IT", "")
	if ge != nil {
		t.Fatalf("resolve amd/Gemma-4-31B-IT failed: %v", ge)
	}
	if !plan.Forced || len(plan.Candidates) != 1 ||
		plan.Candidates[0].Provider.ID != "amd" ||
		plan.Candidates[0].UpstreamModel != "Gemma-4-31B-IT" {
		t.Fatalf("expected stripped Gemma-4-31B-IT on amd, got %+v", plan.Candidates)
	}

	// Verify 402 and 410 classification.
	if act := ClassifyFailure(402, nil); !act.RotateKey || !act.Failover || !act.Cooldown {
		t.Fatalf("expected 402 to rotate key, failover, and cooldown: %+v", act)
	}
	if act := ClassifyFailure(410, nil); act.RotateKey || !act.Failover || act.Cooldown {
		t.Fatalf("expected 410 to failover without cooldown: %+v", act)
	}

	// Seeded legacy google route should fall through to catalog gateway backend when google OAuth is unavailable.
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "Gemma-4-31B-IT", TargetProvider: "google", TargetModel: "Gemma-4-31B-IT", Priority: 0, IsActive: true,
	})
	plan, ge = r.Resolve(context.Background(), "Gemma-4-31B-IT", "")
	if ge != nil {
		t.Fatalf("expected inactive google route to fall through to amd catalog, got %v", ge)
	}
	if plan.LegacyProvider != "" || len(plan.Candidates) != 1 || plan.Candidates[0].Provider.ID != "amd" {
		t.Fatalf("expected amd catalog candidate when google OAuth unavailable, got %+v", plan)
	}
}

func TestParseRateLimitHeadersAndCooldownOverride(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	h1 := make(http.Header)
	h1.Set("Retry-After", "42")
	h1.Set("x-ratelimit-remaining-requests", "0")
	hints1 := ParseRateLimitHeaders(h1, now)
	if hints1.RetryAfter != 42*time.Second || !hints1.ProactivelyExhausted {
		t.Fatalf("expected 42s retry-after and exhaustion flag, got %+v", hints1)
	}

	h2 := make(http.Header)
	h2.Set("x-ratelimit-reset-requests", "1m30s")
	hints2 := ParseRateLimitHeaders(h2, now)
	if hints2.RetryAfter != 90*time.Second {
		t.Fatalf("expected 90s reset duration, got %+v", hints2)
	}

	cd := NewCooldown()
	cd.nowFunc = func() time.Time { return now }
	dur := cd.MarkFailureWithRetry("openrouter", 429, "rate limit", 7*time.Second)
	if dur != 7*time.Second {
		t.Fatalf("expected exact 7s retry override, got %v", dur)
	}
	cooling, rem, _ := cd.IsCoolingDown("openrouter")
	if !cooling || rem != 7*time.Second {
		t.Fatalf("expected active 7s cooldown, cooling=%v rem=%v", cooling, rem)
	}
}

func TestClassifyGatewayErrorAndCanonicalCatalogResolution(t *testing.T) {
	act503, dur503 := ClassifyGatewayError(ir.NewError(503, "UNAVAILABLE", "This model is currently experiencing high demand"))
	if !act503.RotateKey || !act503.Failover || dur503 != 15*time.Second {
		t.Fatalf("expected 503 key rotation with 15s cooldown, got act=%+v dur=%v", act503, dur503)
	}

	act400Key, dur400Key := ClassifyGatewayError(ir.NewError(400, "API_KEY_INVALID", "API key not valid. Please pass a valid API key."))
	if !act400Key.RotateKey || !act400Key.Failover || dur400Key != 1*time.Hour {
		t.Fatalf("expected 400 API_KEY_INVALID key quarantine 1h, got act=%+v dur=%v", act400Key, dur400Key)
	}

	act400User, _ := ClassifyGatewayError(ir.NewError(400, "invalid_request_error", "temperature must be >= 0"))
	if act400User.RotateKey || act400User.Failover {
		t.Fatalf("expected normal client 400 not to rotate/failover, got %+v", act400User)
	}

	// Verify bare canonical ID "gemma-4-31b-it" fans out to BOTH nvidia and amd with native IDs.
	gdb := newRouterTestDB(t)
	reg := newMockRegistry(t)
	r := New(reg, NewCooldown(), gdb)
	plan, ge := r.Resolve(context.Background(), "gemma-4-31b-it", "")
	if ge != nil {
		t.Fatalf("canonical resolve failed: %v", ge)
	}
	if len(plan.Candidates) != 2 ||
		plan.Candidates[0].Provider.ID != "nvidia" || plan.Candidates[0].UpstreamModel != "google/gemma-4-31b-it" ||
		plan.Candidates[1].Provider.ID != "amd" || plan.Candidates[1].UpstreamModel != "Gemma-4-31B-IT" {
		t.Fatalf("expected canonical fan-out to nvidia and amd native IDs, got %+v", plan.Candidates)
	}
}
