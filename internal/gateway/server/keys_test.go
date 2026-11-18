package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/middleware"
)

func withCaller(req *http.Request, c *clientkey.Caller) *http.Request {
	return req.WithContext(clientkey.WithCaller(req.Context(), c))
}

func TestClientKeyProviderAllowlistAcrossProtocols(t *testing.T) {
	beh := &mockUpstreamBehavior{}
	srv, gdb := setupTestServer(t, beh)
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "r1", TargetProvider: "deepseek", TargetModel: "deepseek-reasoner", Priority: 0, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "r1", TargetProvider: "amd", TargetModel: "amd/r1", Priority: 1, IsActive: true,
	})

	// Allowed subset: deepseek (priority 0) is filtered out, so amd serves the request.
	amdOnly := &clientkey.Caller{Name: "pkm-app", AllowedProviders: []string{"amd"}}
	req := withCaller(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"r1","messages":[{"role":"user","content":"hi"}]}`)), amdOnly)
	rec := httptest.NewRecorder()
	srv.HandleOpenAIChat(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Nexus-Provider") != "amd" || beh.amdCalls.Load() != 1 {
		t.Fatalf("expected amd to serve filtered plan, got %d provider=%q body=%s",
			rec.Code, rec.Header().Get("X-Nexus-Provider"), rec.Body.String())
	}

	// Nothing permitted remains: protocol-correct 403 without contacting any upstream.
	openaiOnly := &clientkey.Caller{Name: "pkm-app", AllowedProviders: []string{"openai"}}
	anthReq := withCaller(httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages",
		strings.NewReader(`{"model":"r1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)), openaiOnly)
	rec = httptest.NewRecorder()
	srv.HandleAnthropicMessages(rec, anthReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	var anthErr struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &anthErr); err != nil || anthErr.Type != "error" ||
		anthErr.Error.Type != "permission_error" || !strings.Contains(anthErr.Error.Message, "deepseek") {
		t.Fatalf("expected Anthropic permission_error envelope, got %s", rec.Body.String())
	}
	if beh.amdCalls.Load() != 1 {
		t.Fatalf("denied request must not reach upstream, amd calls=%d", beh.amdCalls.Load())
	}

	gemReq := withCaller(httptest.NewRequest(http.MethodPost, "/v1beta/models/r1:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)), openaiOnly)
	rec = httptest.NewRecorder()
	srv.HandleGeminiModels(rec, gemReq)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "PERMISSION_DENIED") {
		t.Fatalf("expected Gemini PERMISSION_DENIED, got %d: %s", rec.Code, rec.Body.String())
	}

	// Explicit provider selection cannot escape the allowlist.
	hdrReq := withCaller(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"r1","messages":[{"role":"user","content":"hi"}]}`)), amdOnly)
	hdrReq.Header.Set("X-Nexus-Provider", "deepseek")
	rec = httptest.NewRecorder()
	srv.HandleOpenAIChat(rec, hdrReq)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "forbidden_by_key_policy") {
		t.Fatalf("expected 403 for header-forced disallowed provider, got %d: %s", rec.Code, rec.Body.String())
	}

	// Non-generation Gemini actions always reach gemini and are gated too.
	countReq := withCaller(httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:countTokens",
		strings.NewReader(`{}`)), openaiOnly)
	rec = httptest.NewRecorder()
	srv.HandleGeminiModels(rec, countReq)
	if rec.Code != http.StatusForbidden || beh.geminiCalls.Load() != 0 {
		t.Fatalf("expected 403 before Gemini forwarding, got %d calls=%d: %s", rec.Code, beh.geminiCalls.Load(), rec.Body.String())
	}

	// Per-key counters: 1 success + 4 denials attributed to pkm-app.
	ks := srv.Metrics.KeySnapshot("pkm-app")
	if ks == nil || ks.Requests != 5 || ks.Success != 1 || ks.Errors != 4 || ks.LastStatus != http.StatusForbidden {
		t.Fatalf("unexpected key metrics %+v", ks)
	}
	if snap := srv.Metrics.Snapshot(); len(snap.Keys) != 1 || snap.Keys[0].Name != "pkm-app" {
		t.Fatalf("expected keys in stats snapshot, got %+v", snap.Keys)
	}
}

func TestClientKeyModelAllowlist(t *testing.T) {
	beh := &mockUpstreamBehavior{}
	srv, gdb := setupTestServer(t, beh)
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "gpt-4o", TargetProvider: "openai", TargetModel: "gpt-4o", IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "r1", TargetProvider: "amd", TargetModel: "amd/r1", IsActive: true,
	})
	agent := &clientkey.Caller{Name: "agent", AllowedModels: []string{"gpt-*"}}

	post := func(model string) *httptest.ResponseRecorder {
		req := withCaller(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)), agent)
		rec := httptest.NewRecorder()
		srv.HandleOpenAIChat(rec, req)
		return rec
	}
	if rec := post("gpt-4o"); rec.Code != http.StatusOK {
		t.Fatalf("expected allowed model, got %d: %s", rec.Code, rec.Body.String())
	}
	rec := post("r1")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "forbidden_by_key_policy") || beh.amdCalls.Load() != 0 {
		t.Fatalf("expected 403 for model outside allowlist, got %d calls=%d: %s", rec.Code, beh.amdCalls.Load(), rec.Body.String())
	}
}

func TestClientKeyLegacyDelegationCountsAsProvider(t *testing.T) {
	beh := &mockUpstreamBehavior{}
	srv, gdb := setupTestServer(t, beh)
	legacyCalls := 0
	legacyAvail := func(id string) bool { return id == "google" }
	srv.Legacy = LegacyHandlers{
		OpenAIChat: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			legacyCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"legacy","choices":[]}`))
		}),
		Available: legacyAvail,
	}
	srv.Router.LegacyAvailable = legacyAvail
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "oauth-only", TargetProvider: "google", TargetModel: "gemini-3-pro", IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "mixed", TargetProvider: "google", TargetModel: "gemini-3-pro", Priority: 0, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "mixed", TargetProvider: "amd", TargetModel: "amd/r1", Priority: 1, IsActive: true,
	})

	post := func(model string, c *clientkey.Caller) *httptest.ResponseRecorder {
		req := withCaller(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)), c)
		rec := httptest.NewRecorder()
		srv.HandleOpenAIChat(rec, req)
		return rec
	}

	amdOnly := &clientkey.Caller{Name: "pkm-app", AllowedProviders: []string{"amd"}}
	if rec := post("oauth-only", amdOnly); rec.Code != http.StatusForbidden || legacyCalls != 0 {
		t.Fatalf("expected legacy google delegation denied, got %d calls=%d: %s", rec.Code, legacyCalls, rec.Body.String())
	}
	googleOnly := &clientkey.Caller{Name: "claude-code", AllowedProviders: []string{"google"}}
	if rec := post("oauth-only", googleOnly); rec.Code != http.StatusOK || legacyCalls != 1 ||
		rec.Header().Get("X-Nexus-Route") != "legacy-oauth" {
		t.Fatalf("expected legacy delegation allowed, got %d calls=%d", rec.Code, legacyCalls)
	}
	// Gateway candidates filtered away: the permitted legacy fallback is promoted to primary.
	if rec := post("mixed", googleOnly); rec.Code != http.StatusOK || legacyCalls != 2 || beh.amdCalls.Load() != 0 {
		t.Fatalf("expected promoted legacy fallback, got %d calls=%d amd=%d", rec.Code, legacyCalls, beh.amdCalls.Load())
	}
	// Legacy fallback is dropped for callers that may not use it.
	if rec := post("mixed", amdOnly); rec.Code != http.StatusOK || rec.Header().Get("X-Nexus-Provider") != "amd" || legacyCalls != 2 {
		t.Fatalf("expected amd only, got %d provider=%q calls=%d", rec.Code, rec.Header().Get("X-Nexus-Provider"), legacyCalls)
	}
}

func TestClientKeyEmbeddingsAllowlist(t *testing.T) {
	capt := &embedCapture{}
	srv, gdb := setupEmbeddingServer(t, capt)
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "kb-embed", TargetProvider: "openai", TargetModel: "text-embedding-3-small", Priority: 0, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "kb-embed", TargetProvider: "nvidia", TargetModel: "nvidia/nv-embedqa-e5-v5", Priority: 1, IsActive: true,
	})

	post := func(c *clientkey.Caller) *httptest.ResponseRecorder {
		req := withCaller(httptest.NewRequest(http.MethodPost, "/v1/embeddings",
			strings.NewReader(`{"model":"kb-embed","input":"private note"}`)), c)
		rec := httptest.NewRecorder()
		srv.HandleOpenAIEmbeddings(rec, req)
		return rec
	}
	rec := post(&clientkey.Caller{Name: "pkm-app", AllowedProviders: []string{"nvidia"}})
	if rec.Code != http.StatusOK || rec.Header().Get("X-Nexus-Provider") != "nvidia" || len(capt.openaiBody) != 0 {
		t.Fatalf("expected nvidia only, got %d provider=%q openai calls=%d", rec.Code, rec.Header().Get("X-Nexus-Provider"), len(capt.openaiBody))
	}
	rec = post(&clientkey.Caller{Name: "pkm-app", AllowedProviders: []string{"gemini"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "permission_error") || len(capt.nvidiaBody) != 1 {
		t.Fatalf("expected OpenAI-format 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if ks := srv.Metrics.KeySnapshot("pkm-app"); ks == nil || ks.Requests != 2 || ks.Errors != 1 || ks.InputTokens != 5 || ks.LastProvider != "nvidia" {
		t.Fatalf("unexpected key metrics %+v", ks)
	}
}

func TestClientKeysAdminAPIEndToEnd(t *testing.T) {
	beh := &mockUpstreamBehavior{}
	srv, gdb := setupTestServer(t, beh)
	if err := gdb.AutoMigrate(&models.ClientKey{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "r1", TargetProvider: "deepseek", TargetModel: "deepseek-reasoner", Priority: 0, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "r1", TargetProvider: "amd", TargetModel: "amd/r1", Priority: 1, IsActive: true,
	})

	rtr := chi.NewRouter()
	rtr.Get("/api/gateway/keys", srv.HandleListClientKeys)
	rtr.Post("/api/gateway/keys", srv.HandleCreateClientKey)
	rtr.Patch("/api/gateway/keys/{id}", srv.HandleUpdateClientKey)
	rtr.Delete("/api/gateway/keys/{id}", srv.HandleDeleteClientKey)
	rtr.With(middleware.APIKeyAuth(gdb)).Post("/v1/chat/completions", srv.HandleOpenAIChat)
	do := func(method, path, body string, header map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		rtr.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodPost, "/api/gateway/keys", `{"name":"pkm-app","allowed_providers":["AMD"," amd "],"allowed_models":["r1"]}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create failed %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Key    adminClientKeyDTO `json:"key"`
		APIKey string            `json:"api_key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if !strings.HasPrefix(created.APIKey, clientkey.KeyPrefix) || created.Key.MaskedKey == created.APIKey ||
		strings.Join(created.Key.AllowedProviders, ",") != "amd" || !created.Key.Restricted {
		t.Fatalf("unexpected create response %s", rec.Body.String())
	}
	var row models.ClientKey
	gdb.First(&row, created.Key.ID)
	if row.KeyHash != clientkey.Hash(created.APIKey) {
		t.Fatal("stored hash does not match issued key")
	}

	for _, c := range []struct {
		body string
		want int
	}{
		{`{"name":"pkm-app"}`, http.StatusConflict},
		{`{"name":"other","allowed_providers":["no-such-provider"]}`, http.StatusBadRequest},
		{`{"name":"legacy"}`, http.StatusBadRequest},
		{`{"name":""}`, http.StatusBadRequest},
	} {
		if rec := do(http.MethodPost, "/api/gateway/keys", c.body, nil); rec.Code != c.want {
			t.Fatalf("body %s: expected %d, got %d: %s", c.body, c.want, rec.Code, rec.Body.String())
		}
	}

	// The issued key authenticates and is restricted to amd: deepseek (priority 0) is skipped.
	auth := map[string]string{"Authorization": "Bearer " + created.APIKey}
	rec = do(http.MethodPost, "/v1/chat/completions", `{"model":"r1","messages":[{"role":"user","content":"hi"}]}`, auth)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Nexus-Provider") != "amd" {
		t.Fatalf("expected amd via client key, got %d provider=%q: %s", rec.Code, rec.Header().Get("X-Nexus-Provider"), rec.Body.String())
	}
	if rec := do(http.MethodPost, "/v1/chat/completions", `{"model":"r1","messages":[]}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key once client keys exist, got %d", rec.Code)
	}

	rec = do(http.MethodGet, "/api/gateway/keys", "", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.APIKey) {
		t.Fatalf("list must not leak plaintext: %d %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Keys []adminClientKeyDTO `json:"keys"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	if len(listed.Keys) != 1 || listed.Keys[0].Stats == nil || listed.Keys[0].Stats.Success != 1 || listed.Keys[0].LastUsedAt == "" {
		t.Fatalf("expected one key with stats and last_used_at, got %s", rec.Body.String())
	}

	// PATCH by name: clear the provider allowlist, keep models; PATCH by id: disable.
	rec = do(http.MethodPatch, "/api/gateway/keys/pkm-app", `{"allowed_providers":[]}`, nil)
	var patched adminClientKeyDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &patched)
	if rec.Code != http.StatusOK || len(patched.AllowedProviders) != 0 || strings.Join(patched.AllowedModels, ",") != "r1" {
		t.Fatalf("unexpected patch result %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodPost, "/v1/chat/completions", `{"model":"r1","messages":[{"role":"user","content":"hi"}]}`, auth)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Nexus-Provider") != "deepseek" {
		t.Fatalf("expected deepseek after clearing allowlist, got %d provider=%q", rec.Code, rec.Header().Get("X-Nexus-Provider"))
	}
	idPath := "/api/gateway/keys/" + strconv.FormatUint(uint64(created.Key.ID), 10)
	if rec := do(http.MethodPatch, idPath, `{"disabled":true}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("disable failed %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/v1/chat/completions", `{"model":"r1","messages":[]}`, auth); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected disabled key rejected, got %d", rec.Code)
	}
	if rec := do(http.MethodPatch, idPath, `{"name":"renamed"}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected rename rejected, got %d", rec.Code)
	}
	if rec := do(http.MethodPatch, "/api/gateway/keys/legacy", `{"disabled":true}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected legacy patch rejected, got %d", rec.Code)
	}

	if rec := do(http.MethodDelete, idPath, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete failed %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodDelete, idPath, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d", rec.Code)
	}
	rec = do(http.MethodGet, "/api/gateway/keys", "", nil)
	if strings.Contains(rec.Body.String(), "pkm-app") {
		t.Fatalf("deleted key still listed: %s", rec.Body.String())
	}
}
