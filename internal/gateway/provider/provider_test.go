package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func mockClient(h http.HandlerFunc) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			h(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		}),
	}
}

func TestKeyRingStickyRotation(t *testing.T) {
	kr := NewKeyRing(false, "sk-111111111111", "sk-222222222222", "sk-111111111111")
	if kr.Count() != 2 {
		t.Fatalf("expected deduplicated count 2, got %d", kr.Count())
	}
	if kr.Current() != "sk-111111111111" {
		t.Fatalf("unexpected initial key: %s", kr.Current())
	}

	// Mismatched failedKey should not rotate concurrently.
	next, rotated := kr.Rotate("sk-other")
	if rotated || next != "sk-111111111111" {
		t.Fatalf("expected no rotation for stale failedKey, got next=%s rotated=%v", next, rotated)
	}

	next, rotated = kr.Rotate("sk-111111111111")
	if !rotated || next != "sk-222222222222" {
		t.Fatalf("expected rotation to sk-2222, got next=%s rotated=%v", next, rotated)
	}

	// Verify per-key cooldown causes Current() and Rotate() to skip unhealthy keys.
	now := time.Now()
	kr.AddKeys("sk-333333333333")
	kr.nowFunc = func() time.Time { return now }
	kr.MarkFailure("sk-111111111111", 401)
	if kr.HealthyCount() != 2 {
		t.Fatalf("expected HealthyCount=2, got %d", kr.HealthyCount())
	}
	next, rotated = kr.Rotate("sk-222222222222")
	if !rotated || next != "sk-333333333333" {
		t.Fatalf("expected rotation to skip cooled sk-1111 and pick sk-3333, got %s", next)
	}
	kr.nowFunc = func() time.Time { return now.Add(15 * time.Minute) }
	if kr.HealthyCount() != 3 {
		t.Fatalf("expected sk-1111 cooldown expiry restoring HealthyCount=3, got %d", kr.HealthyCount())
	}
}

func TestPresetsAndTargetSelection(t *testing.T) {
	t.Setenv("NEXUS_OPENAI_API_KEY", "sk-oai-test")
	t.Setenv("NEXUS_DEEPSEEK_API_KEY", "sk-ds-test")
	t.Setenv("NEXUS_GEMINI_API_KEY", "gem-test")

	reg, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry error: %v", err)
	}

	for _, id := range DefaultPresetOrder {
		if _, ok := reg.Get(id); !ok {
			t.Fatalf("expected preset %q loaded", id)
		}
	}

	oai, _ := reg.Get("openai")
	if tgt, pass, ok := oai.TargetFor(ir.ProtoOpenAIResp); !ok || !pass || tgt != ir.ProtoOpenAIResp {
		t.Fatalf("expected openai responses passthrough, got (%s, %v, %v)", tgt, pass, ok)
	}
	if tgt, pass, ok := oai.TargetFor(ir.ProtoAnthropic); !ok || pass || tgt != ir.ProtoOpenAIChat {
		t.Fatalf("expected anthropic->openai-chat translation, got (%s, %v, %v)", tgt, pass, ok)
	}

	gem, _ := reg.Get("gemini")
	if tgt, pass, ok := gem.TargetFor(ir.ProtoGemini); !ok || !pass || tgt != ir.ProtoGemini {
		t.Fatalf("expected gemini passthrough, got (%s, %v, %v)", tgt, pass, ok)
	}
	if tgt, pass, ok := gem.TargetFor(ir.ProtoOpenAIChat); !ok || pass || tgt != ir.ProtoGemini {
		t.Fatalf("expected chat->gemini translation, got (%s, %v, %v)", tgt, pass, ok)
	}

	// Verify dual auth headers between Gemini native and OpenAI Chat.
	reqGem, _ := http.NewRequest("POST", "https://example.com", nil)
	gem.ApplyHeaders(reqGem, ir.ProtoGemini, "gem-secret")
	if reqGem.Header.Get("x-goog-api-key") != "gem-secret" {
		t.Fatalf("expected x-goog-api-key for Gemini native")
	}

	// Verify AMD and Ollama presets support native Anthropic passthrough.
	amd, _ := reg.Get("amd")
	if tgt, pass, ok := amd.TargetFor(ir.ProtoAnthropic); !ok || !pass || tgt != ir.ProtoAnthropic {
		t.Fatalf("expected amd anthropic passthrough, got (%s, %v, %v)", tgt, pass, ok)
	}
	ollama, _ := reg.Get("ollama")
	if tgt, pass, ok := ollama.TargetFor(ir.ProtoAnthropic); !ok || !pass || tgt != ir.ProtoAnthropic {
		t.Fatalf("expected ollama anthropic passthrough, got (%s, %v, %v)", tgt, pass, ok)
	}

	// Verify nested AMD detail.error and RFC 7807 problem+json error parsing.
	geAMD := codec.ParseUpstreamError(404, []byte(`{"detail":{"error":{"message":"Model not found","type":"invalid_request_error","code":404}}}`))
	if geAMD.Message != "Model not found" || geAMD.Code != "404" {
		t.Fatalf("unexpected AMD nested error parse: %+v", geAMD)
	}
	geNV := codec.ParseUpstreamError(429, []byte(`{"status":429,"title":"Too Many Requests"}`))
	if geNV.Message != "Too Many Requests" {
		t.Fatalf("unexpected RFC 7807 error parse: %+v", geNV)
	}
}

func TestCatalogDiscoveryAndExactMatch(t *testing.T) {
	client := mockClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3:32b","owned_by":"ollama"},{"id":"deepseek-r1:70b"}]}`))
			return
		}
		http.NotFound(w, r)
	})

	p := &Provider{
		ID:          "ollama",
		DisplayName: "Ollama",
		Enabled:     true,
		NoKey:       true,
		Endpoints:   Endpoints{OpenAIChat: "http://mock.ollama/v1"},
		Native:      []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:      ir.ProtoOpenAIChat,
	}
	reg := NewRegistryWithProviders(client, p)

	if _, err := reg.Catalog().Refresh(context.Background(), p); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	if !reg.Catalog().HasExactModel(context.Background(), p, "qwen3:32b") {
		t.Fatalf("expected catalog to match qwen3:32b")
	}
	if reg.Catalog().HasExactModel(context.Background(), p, "nonexistent-model") {
		t.Fatalf("expected nonexistent-model not to match")
	}

	// Verify multi-key catalog refresh rotates past 401 key cleanly.
	var calls atomic.Int32
	rotClient := mockClient(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer bad-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m-ok"}]}`))
	})
	p2 := &Provider{
		ID:        "openrouter",
		Enabled:   true,
		Endpoints: Endpoints{OpenAIChat: "http://mock.or/v1"},
		Keys:      NewKeyRing(false, "bad-key", "good-key"),
	}
	reg2 := NewRegistryWithProviders(rotClient, p2)
	mods, err := reg2.Catalog().Refresh(context.Background(), p2)
	if err != nil || len(mods) != 1 || mods[0].ID != "m-ok" || calls.Load() != 2 {
		t.Fatalf("expected catalog refresh key rotation success, err=%v mods=%+v calls=%d", err, mods, calls.Load())
	}
}

func TestOpenRouterFreeOnlyFiltering(t *testing.T) {
	client := mockClient(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": "openrouter/free",
					"pricing": {"prompt": "-1", "completion": "-1"}
				},
				{
					"id": "meta-llama/llama-3.3-70b-instruct:free",
					"pricing": {"prompt": "0", "completion": "0", "request": "0", "internal_reasoning": "0"},
					"architecture": {"output_modalities": ["text"]}
				},
				{
					"id": "openai/gpt-4o",
					"pricing": {"prompt": "0.0000025", "completion": "0.00001"},
					"architecture": {"output_modalities": ["text"]}
				},
				{
					"id": "vendor/image-only-zero-text",
					"pricing": {"prompt": "0", "completion": "0", "image_output": "0.04"},
					"architecture": {"output_modalities": ["image"]}
				},
				{
					"id": "vendor/zero-with-request-fee:free",
					"pricing": {"prompt": "0", "completion": "0", "request": "0.005"},
					"architecture": {"output_modalities": ["text"]}
				}
			]
		}`))
	})

	// Preset and providers.yaml default openrouter to FreeOnly=true.
	t.Setenv("NEXUS_OPENROUTER_API_KEY", "or-test")
	reg, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry error: %v", err)
	}
	orPreset, ok := reg.Get("openrouter")
	if !ok || !orPreset.FreeOnly {
		t.Fatalf("expected openrouter preset to default FreeOnly=true, got ok=%v freeOnly=%v", ok, orPreset != nil && orPreset.FreeOnly)
	}

	// Verify env override can disable FreeOnly.
	t.Setenv("NEXUS_OPENROUTER_FREE_ONLY", "false")
	regAll, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry error with override: %v", err)
	}
	orAll, _ := regAll.Get("openrouter")
	if orAll.FreeOnly {
		t.Fatalf("expected NEXUS_OPENROUTER_FREE_ONLY=false to disable free filtering")
	}

	pFree := &Provider{
		ID:        "openrouter",
		Enabled:   true,
		FreeOnly:  true,
		Endpoints: Endpoints{OpenAIChat: "http://mock.or/v1"},
		Keys:      NewKeyRing(false, "good-key"),
	}
	regFree := NewRegistryWithProviders(client, pFree)
	mods, err := regFree.Catalog().Refresh(context.Background(), pFree)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	if len(mods) != 2 || mods[0].ID != "openrouter/free" || mods[1].ID != "meta-llama/llama-3.3-70b-instruct:free" {
		t.Fatalf("expected only free text models when FreeOnly=true, got %+v", mods)
	}
}

func TestNonChatModelFilteringAndRuntimePatch(t *testing.T) {
	amdClient := mockClient(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"Clef"},{"id":"d3-flash"},{"id":"DeepSeek-V4.1-Flash"}]}`))
	})
	amd := &Provider{
		ID:        "amd",
		Enabled:   true,
		Endpoints: Endpoints{OpenAIChat: "http://mock.amd/v1", Anthropic: "http://mock.amd/anthropic"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoAnthropic},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      NewKeyRing(false, "k1"),
	}
	reg := NewRegistryWithProviders(amdClient, amd)
	mods, err := reg.Catalog().Refresh(context.Background(), amd)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	if len(mods) != 1 || mods[0].ID != "DeepSeek-V4.1-Flash" {
		t.Fatalf("expected non-chat Clef and d3-flash filtered out, got %+v", mods)
	}

	// Verify runtime patch enables IncludeNonChat cleanly and surfaces all models on refresh.
	inc := true
	anth := ir.ProtoAnthropic
	affected, err := amd.ApplyRuntimePatch(nil, nil, &inc, &anth, "k2")
	if err != nil || !affected {
		t.Fatalf("expected ApplyRuntimePatch catalogAffected=true, err=%v affected=%v", err, affected)
	}
	if amd.PreferredProtocol() != ir.ProtoAnthropic || amd.Keys.Count() != 2 {
		t.Fatalf("unexpected provider state after runtime patch: prefer=%s keys=%d", amd.PreferredProtocol(), amd.Keys.Count())
	}
	allMods, err := reg.Catalog().Refresh(context.Background(), amd)
	if err != nil || len(allMods) != 3 {
		t.Fatalf("expected all 3 models with IncludeNonChat=true, err=%v mods=%+v", err, allMods)
	}

	// Verify proactive exhaustion rotation across multiple keys.
	now := time.Now()
	kr := NewKeyRing(false, "key-a", "key-b")
	kr.nowFunc = func() time.Time { return now }
	next, rotated := kr.MarkExhaustedAndRotate("key-a", 20*time.Second)
	if !rotated || next != "key-b" || kr.HealthyCount() != 1 {
		t.Fatalf("expected proactive rotation to key-b with 1 healthy left, got next=%s rotated=%v healthy=%d", next, rotated, kr.HealthyCount())
	}
}

func TestGeminiAPIKeysLoadingRoundRobinAndCanonicalModelID(t *testing.T) {
	t.Setenv("NEXUS_GEMINI_API_KEYS", "")
	t.Setenv("GEMINI_API_KEYS", `"gkey-11111111","gkey-22222222";gkey-33333333`)
	t.Setenv("NEXUS_GEMINI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gkey-22222222")

	reg, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}
	gem, ok := reg.Get("gemini")
	if !ok || !gem.Available() {
		t.Fatalf("expected gemini available with GEMINI_API_KEYS")
	}
	if gem.Keys.Count() != 3 {
		t.Fatalf("expected 3 deduplicated keys from GEMINI_API_KEYS, got %d (%v)", gem.Keys.Count(), gem.Keys.MaskedKeys())
	}
	if gem.GetKeyStrategy() != KeyStrategyRoundRobin {
		t.Fatalf("expected gemini default key strategy round-robin, got %s", gem.GetKeyStrategy())
	}

	got1 := gem.Keys.Acquire()
	got2 := gem.Keys.Acquire()
	got3 := gem.Keys.Acquire()
	if got1 == got2 || got2 == got3 || got1 == got3 {
		t.Fatalf("expected round-robin Acquire spread across all 3 keys, got [%s, %s, %s]", got1, got2, got3)
	}

	// Simulate 503 failure on got1 and verify NextHealthyExcluding relays to healthy sibling.
	gem.Keys.MarkFailureDetailed(got1, 503, "high demand", 0)
	tried := map[string]bool{got1: true}
	nextKey, rotated := gem.Keys.NextHealthyExcluding(tried)
	if !rotated || nextKey == "" || nextKey == got1 {
		t.Fatalf("expected relay past cooled key %s, got next=%s ok=%v", got1, nextKey, rotated)
	}
	st := gem.Keys.StatusSnapshot()
	if len(st) != 3 {
		t.Fatalf("expected 3 key statuses, got %d", len(st))
	}

	// Verify runtime RemoveByMasked & ClearCooldowns.
	gem.Keys.ClearCooldowns()
	if gem.Keys.HealthyCount() != 3 {
		t.Fatalf("expected ClearCooldowns to restore all 3 keys, got %d", gem.Keys.HealthyCount())
	}
	if !gem.Keys.RemoveByMasked(maskSecret(got3)) || gem.Keys.Count() != 2 {
		t.Fatalf("expected RemoveByMasked to shrink key ring to 2, count=%d", gem.Keys.Count())
	}

	// Verify CanonicalModelID cross-provider normalization.
	cases := map[string]string{
		"deepseek/deepseek-r1:free":     "deepseek-r1",
		"deepseek-ai/deepseek-r1":       "deepseek-r1",
		"DeepSeek-R1":                   "deepseek-r1",
		"deepseek-reasoner":             "deepseek-r1",
		"google/gemma-4-31b-it":         "gemma-4-31b-it",
		"Gemma-4-31B-IT":                "gemma-4-31b-it",
		"models/gemini-4-hydrogen":      "gemini-4-hydrogen",
		"nvidia/nemotron-3.5:free":      "nemotron-3.5",
		"meta-llama/llama-3.3-70b:free": "llama-3.3-70b",
	}
	for raw, want := range cases {
		if got := CanonicalModelID(raw); got != want {
			t.Fatalf("CanonicalModelID(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestEmbeddingModelsExcludedFromChatCatalog(t *testing.T) {
	for _, id := range []string{
		"nvidia/nemotron-3.5-embed-8b", "snowflake/arctic-embed-l", "nvidia/embed-qa-4",
		"gemini-embedding-001", "nomic-embed-text:latest", "baai/bge-m3",
	} {
		if IsChatCapableModel("nvidia", id) {
			t.Errorf("%s must not be chat-capable", id)
		}
		if !IsEmbeddingModel(id) {
			t.Errorf("%s must be detected as embedding model", id)
		}
	}
	for _, id := range []string{"meta/llama-3.3-70b-instruct", "gemini-2.5-flash", "deepseek-reasoner", "gpt-5"} {
		if !IsChatCapableModel("nvidia", id) || IsEmbeddingModel(id) {
			t.Errorf("%s must stay chat-capable", id)
		}
	}
}
