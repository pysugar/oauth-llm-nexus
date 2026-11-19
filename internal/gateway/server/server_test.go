package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type mockUpstreamBehavior struct {
	chatCalls        atomic.Int32
	amdCalls         atomic.Int32
	geminiCalls      atomic.Int32
	respCalls        atomic.Int32
	failFirst429     bool
	forceStreamError bool
}

func setupTestServer(t *testing.T, behavior *mockUpstreamBehavior) (*Server, *gorm.DB) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.RefreshModelRouteCache(gdb)

	client := &http.Client{
		Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			switch req.URL.Host {
			case "mock.openai":
				if req.Header.Get("Authorization") == "Bearer key-bad" {
					rec.WriteHeader(http.StatusUnauthorized)
					_, _ = rec.WriteString(`{"error":{"message":"invalid key","type":"authentication_error"}}`)
					return rec.Result(), nil
				}
				if req.URL.Path == "/v1/responses" {
					behavior.respCalls.Add(1)
					bodyBytes, _ := io.ReadAll(req.Body)
					var m map[string]any
					_ = json.Unmarshal(bodyBytes, &m)
					if m["previous_response_id"] == "resp_prev" {
						rec.Header().Set("Content-Type", "application/json")
						_, _ = rec.WriteString(`{"id":"resp_new","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued"}]}]}`)
						return rec.Result(), nil
					}
					rec.Header().Set("Content-Type", "application/json")
					_, _ = rec.WriteString(`{"id":"resp_oai","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"from openai responses"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
					return rec.Result(), nil
				}
				behavior.chatCalls.Add(1)
				bodyBytes, _ := io.ReadAll(req.Body)
				var chatBody map[string]any
				_ = json.Unmarshal(bodyBytes, &chatBody)
				if wantStream, _ := chatBody["stream"].(bool); wantStream {
					rec.Header().Set("Content-Type", "text/event-stream")
					_, _ = rec.WriteString("data: {\"id\":\"cmpl_oai\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"from openai\"}}]}\n\n" +
						"data: {\"id\":\"cmpl_oai\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n" +
						"data: [DONE]\n\n")
					return rec.Result(), nil
				}
				rec.Header().Set("Content-Type", "application/json")
				_, _ = rec.WriteString(`{"id":"cmpl_oai","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"from openai"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
				return rec.Result(), nil

			case "mock.deepseek":
				if behavior.failFirst429 {
					rec.WriteHeader(http.StatusTooManyRequests)
					_, _ = rec.WriteString(`{"error":{"message":"rate limit hit"}}`)
					return rec.Result(), nil
				}
				if behavior.forceStreamError {
					rec.Header().Set("Content-Type", "text/event-stream")
					_, _ = rec.WriteString("data: {\"error\":{\"message\":\"overloaded\"}}\n\n")
					return rec.Result(), nil
				}
				rec.Header().Set("Content-Type", "text/event-stream")
				_, _ = rec.WriteString("data: {\"id\":\"ds_1\",\"model\":\"deepseek-reasoner\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"hmm\"}}]}\n\n" +
					"data: {\"id\":\"ds_1\",\"model\":\"deepseek-reasoner\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"42\"}}]}\n\n" +
					"data: {\"id\":\"ds_1\",\"model\":\"deepseek-reasoner\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\n" +
					"data: [DONE]\n\n")
				return rec.Result(), nil

			case "mock.amd":
				behavior.amdCalls.Add(1)
				rec.Header().Set("Content-Type", "text/event-stream")
				_, _ = rec.WriteString("data: {\"id\":\"amd_1\",\"model\":\"amd/r1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"from amd\"}}]}\n\n" +
					"data: {\"id\":\"amd_1\",\"model\":\"amd/r1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\n" +
					"data: [DONE]\n\n")
				return rec.Result(), nil

			case "mock.gemini":
				behavior.geminiCalls.Add(1)
				if req.Header.Get("x-goog-api-key") != "gem-key" {
					rec.WriteHeader(http.StatusUnauthorized)
					return rec.Result(), nil
				}
				if strings.Contains(req.URL.RawQuery, "alt=sse") {
					rec.Header().Set("Content-Type", "text/event-stream")
					_, _ = rec.WriteString("data: {\"responseId\":\"g_1\",\"modelVersion\":\"gemini-2.5-flash\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"gemini hello\"}]}}]}\n\n" +
						"data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\",\"content\":{\"role\":\"model\",\"parts\":[]}}],\"usageMetadata\":{\"promptTokenCount\":6,\"candidatesTokenCount\":2}}\n\n")
					return rec.Result(), nil
				}
				rec.Header().Set("Content-Type", "application/json")
				_, _ = rec.WriteString(`{"responseId":"g_ns","candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[{"text":"gemini raw"}]}}]}`)
				return rec.Result(), nil
			}
			rec.WriteHeader(http.StatusNotFound)
			return rec.Result(), nil
		}),
	}

	oai := &provider.Provider{
		ID:          "openai",
		DisplayName: "OpenAI",
		Enabled:     true,
		Endpoints: provider.Endpoints{
			OpenAIChat:      "http://mock.openai/v1",
			OpenAIResponses: "http://mock.openai/v1",
		},
		Native: []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoOpenAIResp},
		Prefer: ir.ProtoOpenAIChat,
		Keys:   provider.NewKeyRing(false, "key-bad", "key-good"),
		// Like the openai preset: keeps Responses turns, so previous_response_id passes through.
		ResponsesStateful: true,
	}
	ds := &provider.Provider{
		ID:          "deepseek",
		DisplayName: "DeepSeek",
		Enabled:     true,
		Endpoints:   provider.Endpoints{OpenAIChat: "http://mock.deepseek/v1"},
		Native:      []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:      ir.ProtoOpenAIChat,
		Keys:        provider.NewKeyRing(false, "ds-key"),
	}
	amd := &provider.Provider{
		ID:          "amd",
		DisplayName: "AMD",
		Enabled:     true,
		Endpoints:   provider.Endpoints{OpenAIChat: "http://mock.amd/v1"},
		Native:      []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:      ir.ProtoOpenAIChat,
		Keys:        provider.NewKeyRing(false, "amd-key"),
	}
	gem := &provider.Provider{
		ID:          "gemini",
		DisplayName: "Gemini",
		Enabled:     true,
		Endpoints:   provider.Endpoints{Gemini: "http://mock.gemini/v1beta"},
		Native:      []ir.Protocol{ir.ProtoGemini},
		Prefer:      ir.ProtoGemini,
		Keys:        provider.NewKeyRing(false, "gem-key"),
	}

	reg := provider.NewRegistryWithProviders(client, oai, ds, amd, gem)
	srv := New(reg, gdb, nil, LegacyHandlers{})
	return srv, gdb
}

func TestGatewayKeyRotationFailoverAndRuntimeSwitch(t *testing.T) {
	beh := &mockUpstreamBehavior{failFirst429: true}
	srv, gdb := setupTestServer(t, beh)

	// 1. Key rotation on OpenAI 401 -> succeeds on 2nd key without failing over.
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "gpt-4o", TargetProvider: "openai", TargetModel: "gpt-4o", Priority: 0, IsActive: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]
	}`))
	rec := httptest.NewRecorder()
	srv.HandleOpenAIChat(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from openai") {
		t.Fatalf("expected key rotation success, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Nexus-Provider") != "openai" || rec.Header().Get("X-Nexus-Route") != "passthrough" {
		t.Fatalf("unexpected headers: %+v", rec.Header())
	}

	// 2. Multi-backend failover: deepseek returns 429 -> pre-commit failover to amd succeeds for Anthropic Messages client!
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "r1", TargetProvider: "deepseek", TargetModel: "deepseek-reasoner", Priority: 0, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "r1", TargetProvider: "amd", TargetModel: "amd/r1", Priority: 1, IsActive: true,
	})

	anthReq := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{
		"model":"r1","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hi"}]
	}`))
	rec2 := httptest.NewRecorder()
	srv.HandleAnthropicMessages(rec2, anthReq)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected failover to amd to succeed with 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("X-Nexus-Provider") != "amd" || !strings.HasPrefix(rec2.Header().Get("X-Nexus-Route"), "translated:") {
		t.Fatalf("expected amd translated headers, got %+v", rec2.Header())
	}
	if !strings.Contains(rec2.Body.String(), "from amd") || !strings.Contains(rec2.Body.String(), "message_stop") {
		t.Fatalf("expected Anthropic SSE stream translated from AMD, got %s", rec2.Body.String())
	}

	// 3. Runtime backend switch via Admin API + stream translation back to non-stream Responses client.
	beh.failFirst429 = false
	_ = srv.Router // ensure active
	rtr := chi.NewRouter()
	rtr.Post("/api/gateway/models/{client}/select", srv.HandleSelectModelBackend)
	switchBody := bytes.NewBufferString(`{"provider":"deepseek","clear_cooldown":true}`)
	switchReq := httptest.NewRequest(http.MethodPost, "/api/gateway/models/r1/select", switchBody)
	switchRec := httptest.NewRecorder()
	rtr.ServeHTTP(switchRec, switchReq)
	if switchRec.Code != http.StatusOK {
		t.Fatalf("admin switch failed (%d): %s", switchRec.Code, switchRec.Body.String())
	}

	respReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"r1","input":"answer"
	}`))
	rec3 := httptest.NewRecorder()
	srv.HandleOpenAIResponses(rec3, respReq)
	if rec3.Code != http.StatusOK || rec3.Header().Get("X-Nexus-Provider") != "deepseek" {
		t.Fatalf("expected switched deepseek provider, got code=%d prov=%s body=%s",
			rec3.Code, rec3.Header().Get("X-Nexus-Provider"), rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), `"42"`) || !strings.Contains(rec3.Body.String(), `"hmm"`) {
		t.Fatalf("expected Responses JSON with thinking and answer 42, got %s", rec3.Body.String())
	}
}

func TestResponsesPreviousIDPassthroughVsTranslatedRejectAndGeminiTranslate(t *testing.T) {
	beh := &mockUpstreamBehavior{}
	srv, gdb := setupTestServer(t, beh)

	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "oai-resp", TargetProvider: "openai", TargetModel: "gpt-4o", Priority: 0, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "gem-model", TargetProvider: "gemini", TargetModel: "gemini-2.5-flash", Priority: 0, IsActive: true,
	})

	// Passthrough Responses with previous_response_id succeeds.
	reqOK := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"oai-resp","previous_response_id":"resp_prev","input":"next"
	}`))
	recOK := httptest.NewRecorder()
	srv.HandleOpenAIResponses(recOK, reqOK)
	if recOK.Code != http.StatusOK || !strings.Contains(recOK.Body.String(), "continued") {
		t.Fatalf("expected passthrough previous_response_id to succeed, got %d: %s", recOK.Code, recOK.Body.String())
	}

	// Translated Responses -> Gemini with previous_response_id returns explicit 400.
	reqBad := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gem-model","previous_response_id":"resp_prev","input":"next"
	}`))
	recBad := httptest.NewRecorder()
	srv.HandleOpenAIResponses(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 rejecting previous_response_id on translated backend, got %d: %s", recBad.Code, recBad.Body.String())
	}

	// Chat Completions -> Gemini translates seamlessly and hits /v1beta/models/...:streamGenerateContent.
	reqGem := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gem-model","messages":[{"role":"user","content":"say hi"}]
	}`))
	recGem := httptest.NewRecorder()
	srv.HandleOpenAIChat(recGem, reqGem)
	if recGem.Code != http.StatusOK || !strings.Contains(recGem.Body.String(), "gemini hello") {
		t.Fatalf("expected translated Gemini chat response, got %d: %s", recGem.Code, recGem.Body.String())
	}

	// Gemini native frontend -> OpenAI Chat backend translates cleanly.
	reqGemFront := httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o:generateContent", strings.NewReader(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}]
	}`))
	// Use explicit override to deepseek SSE backend.
	reqGemFront.Header.Set("X-Nexus-Provider", "deepseek")
	recGemFront := httptest.NewRecorder()
	srv.HandleGeminiModels(recGemFront, reqGemFront)
	if recGemFront.Code != http.StatusOK || !strings.Contains(recGemFront.Body.String(), `"42"`) {
		t.Fatalf("expected Gemini frontend -> DeepSeek backend translation, got %d: %s", recGemFront.Code, recGemFront.Body.String())
	}

	// Verify parseGeminiModelsPath treats GET on models with colon tags (:free, :e2b) as model metadata GET.
	mID, act, isList, isGet := parseGeminiModelsPath(http.MethodGet, "/v1beta/models/nvidia/nemotron-3.5-super-vl:free")
	if !isGet || isList || act != "" || mID != "nvidia/nemotron-3.5-super-vl:free" {
		t.Fatalf("unexpected GET parse for colon-tagged model: mID=%q act=%q isList=%v isGet=%v", mID, act, isList, isGet)
	}
}

func TestV2StreamFirstNonSSEErrorCancelAndGeminiCountTokens(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.RefreshModelRouteCache(gdb)

	var sawStreamTrue atomic.Bool
	client := &http.Client{
		Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			switch req.URL.Host {
			case "mock.nvidia":
				if req.Context().Err() != nil {
					return nil, req.Context().Err()
				}
				body, _ := io.ReadAll(req.Body)
				var payload map[string]any
				_ = json.Unmarshal(body, &payload)
				if s, ok := payload["stream"].(bool); ok && s {
					sawStreamTrue.Store(true)
					rec.Header().Set("Content-Type", "text/event-stream")
					_, _ = rec.WriteString("data: {\"id\":\"nv_1\",\"model\":\"nv-m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"streamed ok\"}}]}\n\n" +
						"data: {\"id\":\"nv_1\",\"model\":\"nv-m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n" +
						"data: [DONE]\n\n")
					return rec.Result(), nil
				}
				return nil, errors.New("unexpected non-stream request when StreamFirst is enabled")

			case "mock.bad200":
				// Simulate buggy relay returning HTTP 200 raw non-SSE JSON error even on stream=true.
				rec.Header().Set("Content-Type", "application/json")
				_, _ = rec.WriteString(`{"error":{"message":"upstream busy"}}`)
				return rec.Result(), nil

			case "mock.backup":
				rec.Header().Set("Content-Type", "text/event-stream")
				_, _ = rec.WriteString("data: {\"id\":\"bk_1\",\"model\":\"bk-m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"recovered\"}}]}\n\n" +
					"data: {\"id\":\"bk_1\",\"model\":\"bk-m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n")
				return rec.Result(), nil

			case "mock.gem":
				if strings.HasSuffix(req.URL.Path, ":countTokens") {
					rec.Header().Set("Content-Type", "application/json")
					_, _ = rec.WriteString(`{"totalTokens":42}`)
					return rec.Result(), nil
				}
			}
			rec.WriteHeader(http.StatusNotFound)
			return rec.Result(), nil
		}),
	}

	nv := &provider.Provider{
		ID:        "nvidia",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.nvidia/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "nv-key"),
		Quirks:    codec.Quirks{StreamFirst: true},
	}
	bad := &provider.Provider{
		ID:        "bad200",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.bad200/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "bad-key"),
	}
	backup := &provider.Provider{
		ID:        "backup",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.backup/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "bk-key"),
	}
	gem := &provider.Provider{
		ID:        "gemini",
		Enabled:   true,
		Endpoints: provider.Endpoints{Gemini: "http://mock.gem/v1beta"},
		Native:    []ir.Protocol{ir.ProtoGemini},
		Prefer:    ir.ProtoGemini,
		Keys:      provider.NewKeyRing(false, "gem-key"),
	}

	reg := provider.NewRegistryWithProviders(client, nv, bad, backup, gem)
	srv := New(reg, gdb, nil, LegacyHandlers{})

	// 1. StreamFirst converts client stream=false on nvidia into upstream stream=true SSE collection.
	reqSF := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"nv-m","stream":false,"messages":[{"role":"user","content":"hi"}]
	}`))
	reqSF.Header.Set("X-Nexus-Provider", "nvidia")
	recSF := httptest.NewRecorder()
	srv.HandleOpenAIChat(recSF, reqSF)
	if recSF.Code != http.StatusOK || !sawStreamTrue.Load() || !strings.Contains(recSF.Body.String(), "streamed ok") {
		t.Fatalf("expected StreamFirst collected JSON response, code=%d body=%s", recSF.Code, recSF.Body.String())
	}

	// 2. HTTP 200 raw non-SSE JSON error on stream=true fails over cleanly to backup!
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "fail-200", TargetProvider: "bad200", TargetModel: "m", Priority: 0, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "fail-200", TargetProvider: "backup", TargetModel: "m", Priority: 1, IsActive: true,
	})
	reqFail := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"fail-200","stream":true,"messages":[{"role":"user","content":"hi"}]
	}`))
	recFail := httptest.NewRecorder()
	srv.HandleOpenAIChat(recFail, reqFail)
	if recFail.Code != http.StatusOK || recFail.Header().Get("X-Nexus-Provider") != "backup" || !strings.Contains(recFail.Body.String(), "recovered") {
		t.Fatalf("expected failover after HTTP 200 JSON error, code=%d headers=%+v body=%s", recFail.Code, recFail.Header(), recFail.Body.String())
	}

	// 3. Client disconnect must return 499 without putting nvidia into cooldown.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reqCancel := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"nv-m","messages":[{"role":"user","content":"hi"}]
	}`)).WithContext(ctx)
	reqCancel.Header.Set("X-Nexus-Provider", "nvidia")
	recCancel := httptest.NewRecorder()
	srv.HandleOpenAIChat(recCancel, reqCancel)
	if recCancel.Code != 499 {
		t.Fatalf("expected 499 on client cancellation, got %d", recCancel.Code)
	}
	if cooling, _, _ := srv.Cooldown.IsCoolingDown("nvidia"); cooling {
		t.Fatalf("client cancellation must NOT put provider into cooldown")
	}

	// 4. Gemini :countTokens works directly through configured gateway gemini provider.
	reqCT := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:countTokens", strings.NewReader(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}]
	}`))
	recCT := httptest.NewRecorder()
	srv.HandleGeminiModels(recCT, reqCT)
	if recCT.Code != http.StatusOK || !strings.Contains(recCT.Body.String(), `"totalTokens":42`) {
		t.Fatalf("expected :countTokens via gateway gemini provider, code=%d body=%s", recCT.Code, recCT.Body.String())
	}
}

func TestHandleGatewayTestAcrossThreeProtocols(t *testing.T) {
	behavior := &mockUpstreamBehavior{}
	srv, _ := setupTestServer(t, behavior)

	for _, tc := range []struct {
		name     string
		protocol string
		model    string
		provider string
		wantProv string
		wantText string
	}{
		{
			name:     "openai_chat_native",
			protocol: "openai-chat",
			model:    "gpt-4o",
			provider: "openai",
			wantProv: "openai",
			wantText: "from openai",
		},
		{
			name:     "openai_responses_native",
			protocol: "openai-responses",
			model:    "gpt-4o",
			provider: "openai",
			wantProv: "openai",
			wantText: "from openai",
		},
		{
			name:     "anthropic_translated_to_openai",
			protocol: "anthropic",
			model:    "gpt-4o",
			provider: "openai",
			wantProv: "openai",
			wantText: "from openai",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"protocol":"` + tc.protocol + `","model":"` + tc.model + `","provider":"` + tc.provider + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/gateway/test", strings.NewReader(payload))
			rec := httptest.NewRecorder()
			srv.HandleGatewayTest(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 wrapper, got %d body=%s", rec.Code, rec.Body.String())
			}
			var got gatewayTestResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode gatewayTestResponse: %v body=%s", err, rec.Body.String())
			}
			if !got.OK || got.Provider != tc.wantProv || !strings.Contains(got.Text, tc.wantText) {
				t.Fatalf("unexpected test result for %s: %+v", tc.name, got)
			}
			if got.RequestPayload == "" {
				t.Fatalf("expected RequestPayload populated for %s", tc.name)
			}
		})
	}

	// Verify tool-call probe payload builds tool schema cleanly.
	reqTool := httptest.NewRequest(http.MethodPost, "/api/gateway/test", strings.NewReader(`{"protocol":"openai-chat","model":"gpt-4o","provider":"openai","tools":true}`))
	recTool := httptest.NewRecorder()
	srv.HandleGatewayTest(recTool, reqTool)
	var gotTool gatewayTestResponse
	_ = json.Unmarshal(recTool.Body.Bytes(), &gotTool)
	if !gotTool.OK || !strings.Contains(gotTool.RequestPayload, `"get_weather"`) {
		t.Fatalf("expected tool schema in RequestPayload, got=%+v", gotTool)
	}

	// Verify streaming SSE mode extracts streamed text cleanly.
	reqStream := httptest.NewRequest(http.MethodPost, "/api/gateway/test", strings.NewReader(`{"protocol":"anthropic","model":"gpt-4o","provider":"openai","stream":true}`))
	recStream := httptest.NewRecorder()
	srv.HandleGatewayTest(recStream, reqStream)
	var gotStream gatewayTestResponse
	_ = json.Unmarshal(recStream.Body.Bytes(), &gotStream)
	if !gotStream.OK || !strings.Contains(gotStream.Text, "from openai") {
		t.Fatalf("expected streaming SSE text extracted, got=%+v", gotStream)
	}

	// Verify unknown/unavailable provider reports structured error without failing wrapper.
	reqErr := httptest.NewRequest(http.MethodPost, "/api/gateway/test", strings.NewReader(`{"protocol":"openai-chat","model":"m","provider":"nonexistent"}`))
	recErr := httptest.NewRecorder()
	srv.HandleGatewayTest(recErr, reqErr)
	var gotErr gatewayTestResponse
	_ = json.Unmarshal(recErr.Body.Bytes(), &gotErr)
	if recErr.Code != http.StatusOK || gotErr.OK || gotErr.StatusCode != 404 {
		t.Fatalf("expected structured 404 error result, code=%d got=%+v", recErr.Code, gotErr)
	}
}

func TestOpenAIChatPassthroughAppliesMaxCompletionTokensQuirk(t *testing.T) {
	var seenHasMaxTokens bool
	var seenMaxCompletionTokens float64
	client := &http.Client{
		Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(req.Body)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			_, seenHasMaxTokens = m["max_tokens"]
			if v, ok := m["max_completion_tokens"].(float64); ok {
				seenMaxCompletionTokens = v
			}
			rec := httptest.NewRecorder()
			rec.Header().Set("Content-Type", "application/json")
			_, _ = rec.WriteString(`{"id":"c_1","object":"chat.completion","model":"gpt-5.4-nano","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
			return rec.Result(), nil
		}),
	}
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_ = gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{})
	db.RefreshModelRouteCache(gdb)

	oai := &provider.Provider{
		ID:        "openai",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.oai/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "sk-test"),
		Quirks:    codec.Quirks{MaxCompletionTokens: true},
	}
	srv := New(provider.NewRegistryWithProviders(client, oai), gdb, nil, LegacyHandlers{})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4-nano","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-Nexus-Provider", "openai")
	rec := httptest.NewRecorder()
	srv.HandleOpenAIChat(rec, req)
	// gpt-5* thinks by default, so the small limit also gets a high effort's thinking room.
	if rec.Code != http.StatusOK || seenHasMaxTokens || seenMaxCompletionTokens != 256+16384 {
		t.Fatalf("expected max_tokens rewritten to max_completion_tokens=256+room on passthrough, code=%d hasMT=%v mct=%v body=%s",
			rec.Code, seenHasMaxTokens, seenMaxCompletionTokens, rec.Body.String())
	}
}

func TestP0FeaturesEndToEnd(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_ = gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{})
	db.RefreshModelRouteCache(gdb)

	var seenTurn2Msgs []map[string]any
	var seenReasoningTemp bool
	var seenAuthKeys []string

	client := &http.Client{
		Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			switch req.URL.Host {
			case "mock.oai":
				if req.URL.Path == "/v1/models" {
					_, _ = rec.WriteString(`{"data":[{"id":"gpt-5.4-nano"},{"id":"tts-1"}]}`)
					return rec.Result(), nil
				}
				seenAuthKeys = append(seenAuthKeys, req.Header.Get("Authorization"))
				raw, _ := io.ReadAll(req.Body)
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				if _, ok := m["temperature"]; ok {
					seenReasoningTemp = true
				}
				if len(seenAuthKeys) == 1 {
					rec.Header().Set("x-ratelimit-remaining-requests", "0")
					rec.Header().Set("x-ratelimit-reset-requests", "20s")
				}
				if wantStream, _ := m["stream"].(bool); wantStream {
					rec.Header().Set("Content-Type", "text/event-stream")
					// Emit >520KB of delta chunks so tail usage capture is exercised cleanly.
					pad := strings.Repeat("a", 4096)
					for i := 0; i < 140; i++ {
						_, _ = rec.WriteString("data: {\"id\":\"s1\",\"model\":\"gpt-5.4-nano\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + pad + "\"}}]}\n\n")
					}
					_, _ = rec.WriteString("data: {\"id\":\"s1\",\"model\":\"gpt-5.4-nano\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30}}\n\n")
					_, _ = rec.WriteString("data: [DONE]\n\n")
					return rec.Result(), nil
				}
				rec.Header().Set("Content-Type", "application/json")
				_, _ = rec.WriteString(`{"id":"c1","object":"chat.completion","model":"gpt-5.4-nano","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
				return rec.Result(), nil

			case "mock.deepseek":
				raw, _ := io.ReadAll(req.Body)
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				if msgs, ok := m["messages"].([]any); ok {
					seenTurn2Msgs = nil
					for _, item := range msgs {
						if mm, ok := item.(map[string]any); ok {
							seenTurn2Msgs = append(seenTurn2Msgs, mm)
						}
					}
				}
				rec.Header().Set("Content-Type", "text/event-stream")
				_, _ = rec.WriteString("data: {\"id\":\"ds_turn\",\"model\":\"deepseek-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"reply1\"}}]}\n\n" +
					"data: {\"id\":\"ds_turn\",\"model\":\"deepseek-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":3}}\n\n" +
					"data: [DONE]\n\n")
				return rec.Result(), nil

			case "mock.rl":
				rec.Header().Set("Retry-After", "9")
				rec.WriteHeader(http.StatusTooManyRequests)
				_, _ = rec.WriteString(`{"error":{"message":"slow down"}}`)
				return rec.Result(), nil
			}
			rec.WriteHeader(http.StatusNotFound)
			return rec.Result(), nil
		}),
	}

	oai := &provider.Provider{
		ID:        "openai",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.oai/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "k1", "k2"),
		Quirks:    codec.Quirks{MaxCompletionTokens: true, IncludeUsageInStream: true},
	}
	ds := &provider.Provider{
		ID:        "deepseek",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.deepseek/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "ds-key"),
	}
	rl := &provider.Provider{
		ID:        "rl",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.rl/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "rl-key"),
	}
	srv := New(provider.NewRegistryWithProviders(client, oai, ds, rl), gdb, nil, LegacyHandlers{})

	// 1. Reasoning sampling stripping + proactive x-ratelimit-remaining-requests=0 rotation + large stream tail usage capture.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4-nano","temperature":0.7,"top_p":0.9,"messages":[{"role":"user","content":"hi"}]}`))
	req1.Header.Set("X-Nexus-Provider", "openai")
	rec1 := httptest.NewRecorder()
	srv.HandleOpenAIChat(rec1, req1)
	if rec1.Code != http.StatusOK || seenReasoningTemp {
		t.Fatalf("expected temperature stripped on gpt-5.4-nano, code=%d seenTemp=%v", rec1.Code, seenReasoningTemp)
	}

	// Second request hits large (>512KB) streaming passthrough and must have rotated proactively to k2!
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4-nano","stream":true,"messages":[{"role":"user","content":"big"}]}`))
	req2.Header.Set("X-Nexus-Provider", "openai")
	rec2 := httptest.NewRecorder()
	srv.HandleOpenAIChat(rec2, req2)
	if rec2.Code != http.StatusOK || len(seenAuthKeys) < 2 || seenAuthKeys[0] != "Bearer k1" || seenAuthKeys[1] != "Bearer k2" {
		t.Fatalf("expected proactive key rotation from k1 to k2, code=%d keys=%v", rec2.Code, seenAuthKeys)
	}

	stats := srv.Metrics.Snapshot()
	if stats.InputTokens != 15 || stats.OutputTokens != 22 || stats.Requests != 2 {
		t.Fatalf("expected large-stream tail usage + sync usage captured in always-on Metrics (5+10=15 in, 2+20=22 out), got %+v", stats)
	}

	// 2. Stateful previous_response_id across translated deepseek backend.
	r1 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek-flash","input":"first question"}`))
	r1.Header.Set("X-Nexus-Provider", "deepseek")
	w1 := httptest.NewRecorder()
	srv.HandleOpenAIResponses(w1, r1)
	var respObj struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w1.Body.Bytes(), &respObj)
	if w1.Code != http.StatusOK || respObj.ID == "" {
		t.Fatalf("expected Responses Turn 1 OK with generated ID, code=%d body=%s", w1.Code, w1.Body.String())
	}

	r2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek-flash","previous_response_id":"`+respObj.ID+`","input":"follow up"}`))
	r2.Header.Set("X-Nexus-Provider", "deepseek")
	w2 := httptest.NewRecorder()
	srv.HandleOpenAIResponses(w2, r2)
	if w2.Code != http.StatusOK || len(seenTurn2Msgs) != 3 {
		t.Fatalf("expected Turn 2 to replay [user1, assistant1, user2] = 3 messages, code=%d msgs=%+v body=%s",
			w2.Code, seenTurn2Msgs, w2.Body.String())
	}

	// 3. Retry-After header sets exact cooldown duration on routed upstream 429.
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "rl-model", TargetProvider: "rl", TargetModel: "m", Priority: 0, IsActive: true,
	})
	reqRL := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"rl-model","messages":[{"role":"user","content":"hi"}]}`))
	recRL := httptest.NewRecorder()
	srv.HandleOpenAIChat(recRL, reqRL)
	if cooling, rem, _ := srv.Cooldown.IsCoolingDown("rl"); !cooling || rem > 9*time.Second || rem < 7*time.Second {
		t.Fatalf("expected Retry-After=9s cooldown, cooling=%v rem=%v", cooling, rem)
	}

	// 4. PATCH /api/gateway/providers/{id} updates IncludeNonChat & triggers catalog refresh + GET /api/gateway/stats works.
	rtr := chi.NewRouter()
	rtr.Patch("/api/gateway/providers/{id}", srv.HandleUpdateProvider)
	rtr.Get("/api/gateway/stats", srv.HandleGatewayStats)

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/gateway/providers/openai", strings.NewReader(`{"include_non_chat":true,"refresh":true}`))
	patchRec := httptest.NewRecorder()
	rtr.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK || !strings.Contains(patchRec.Body.String(), `"tts-1"`) {
		t.Fatalf("expected PATCH provider refresh to include tts-1 when include_non_chat=true, code=%d body=%s", patchRec.Code, patchRec.Body.String())
	}

	statReq := httptest.NewRequest(http.MethodGet, "/api/gateway/stats", nil)
	statRec := httptest.NewRecorder()
	rtr.ServeHTTP(statRec, statReq)
	if statRec.Code != http.StatusOK || !strings.Contains(statRec.Body.String(), `"input_tokens"`) {
		t.Fatalf("expected GET /api/gateway/stats JSON, code=%d body=%s", statRec.Code, statRec.Body.String())
	}
}

func TestGeminiRelayMatrixAndFailoverSync(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_ = gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{})
	db.RefreshModelRouteCache(gdb)

	var seenGemKeys []string
	var countTokensRawQuery string

	client := &http.Client{
		Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			switch req.URL.Host {
			case "mock.gem":
				k := req.Header.Get("x-goog-api-key")
				seenGemKeys = append(seenGemKeys, k)
				if strings.HasSuffix(req.URL.Path, ":countTokens") {
					countTokensRawQuery = req.URL.RawQuery
					if k == "gem-busy" {
						rec.WriteHeader(http.StatusServiceUnavailable)
						_, _ = rec.WriteString(`{"error":{"code":503,"message":"high demand"}}`)
						return rec.Result(), nil
					}
					rec.Header().Set("Content-Type", "application/json")
					_, _ = rec.WriteString(`{"totalTokens":17}`)
					return rec.Result(), nil
				}
				switch k {
				case "gem-bad":
					rec.WriteHeader(http.StatusBadRequest)
					_, _ = rec.WriteString(`{"error":{"code":400,"message":"API key not valid.","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`)
					return rec.Result(), nil
				case "gem-busy":
					rec.WriteHeader(http.StatusServiceUnavailable)
					_, _ = rec.WriteString(`{"error":{"code":503,"message":"This model is currently experiencing high demand."}}`)
					return rec.Result(), nil
				default:
					rec.Header().Set("Content-Type", "text/event-stream")
					_, _ = rec.WriteString("data: {\"responseId\":\"g_ok\",\"modelVersion\":\"gemini-4-hydrogen\",\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\",\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"OK\"}]}}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2}}\n\n")
					return rec.Result(), nil
				}
			case "mock.ds":
				_, _ = rec.WriteString(`{"data":[{"id":"deepseek-reasoner"}]}`)
				return rec.Result(), nil
			case "mock.amd":
				_, _ = rec.WriteString(`{"data":[{"id":"DeepSeek-R1"}]}`)
				return rec.Result(), nil
			case "mock.or":
				_, _ = rec.WriteString(`{"data":[{"id":"deepseek/deepseek-r1:free","pricing":{"prompt":"0","completion":"0"}}]}`)
				return rec.Result(), nil
			}
			rec.WriteHeader(http.StatusNotFound)
			return rec.Result(), nil
		}),
	}

	gem := &provider.Provider{
		ID:          "gemini",
		Enabled:     true,
		Endpoints:   provider.Endpoints{Gemini: "http://mock.gem/v1beta"},
		Native:      []ir.Protocol{ir.ProtoGemini},
		Prefer:      ir.ProtoGemini,
		AuthMode:    "x-goog-api-key",
		KeyStrategy: provider.KeyStrategyRoundRobin,
		Keys:        provider.NewKeyRingWithStrategy(false, provider.KeyStrategyRoundRobin, "gem-bad", "gem-busy", "gem-good"),
	}
	ds := &provider.Provider{
		ID:        "deepseek",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.ds/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "ds-k"),
	}
	amd := &provider.Provider{
		ID:        "amd",
		Enabled:   true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.amd/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "amd-k"),
	}
	or := &provider.Provider{
		ID:        "openrouter",
		Enabled:   true,
		FreeOnly:  true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.or/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:    ir.ProtoOpenAIChat,
		Keys:      provider.NewKeyRing(false, "or-k"),
	}

	reg := provider.NewRegistryWithProviders(client, gem, ds, amd, or)
	_, _ = reg.Catalog().Refresh(context.Background(), ds)
	_, _ = reg.Catalog().Refresh(context.Background(), amd)
	_, _ = reg.Catalog().Refresh(context.Background(), or)

	srv := New(reg, gdb, nil, LegacyHandlers{})

	// 1. Anthropic request forced to gemini: gem-bad (400 API_KEY_INVALID) -> gem-busy (503 high demand) -> gem-good (200 OK).
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"gemini-4-hydrogen","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-Nexus-Provider", "gemini")
	rec := httptest.NewRecorder()
	srv.HandleAnthropicMessages(rec, req)
	if rec.Code != http.StatusOK || len(seenGemKeys) != 3 || seenGemKeys[0] != "gem-bad" || seenGemKeys[1] != "gem-busy" || seenGemKeys[2] != "gem-good" {
		t.Fatalf("expected 3-key relay gem-bad -> gem-busy -> gem-good = 200, code=%d keys=%v body=%s", rec.Code, seenGemKeys, rec.Body.String())
	}

	// 2. Non-generation :countTokens strips client ?key=nexus-secret before forwarding upstream.
	ctReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-hydrogen:countTokens?alt=json&key=nexus-secret", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	ctRec := httptest.NewRecorder()
	srv.HandleGeminiModels(ctRec, ctReq)
	if ctRec.Code != http.StatusOK || strings.Contains(countTokensRawQuery, "key=") || !strings.Contains(countTokensRawQuery, "alt=json") {
		t.Fatalf("expected sanitized countTokens query without leaked key, code=%d rawQuery=%q body=%s", ctRec.Code, countTokensRawQuery, ctRec.Body.String())
	}

	// 3. Model matrix groups deepseek-reasoner / DeepSeek-R1 / deepseek/deepseek-r1:free under "deepseek-r1".
	rtr := chi.NewRouter()
	rtr.Get("/api/gateway/models/matrix", srv.HandleModelsMatrix)
	rtr.Post("/api/gateway/models/{client}/failover", srv.HandleSyncModelFailover)
	rtr.Get("/api/gateway/models/{client}", srv.HandleGetGatewayModel)

	mReq := httptest.NewRequest(http.MethodGet, "/api/gateway/models/matrix?multi_only=true&q=deepseek-r1", nil)
	mRec := httptest.NewRecorder()
	rtr.ServeHTTP(mRec, mReq)
	var matResp struct {
		MultiCount int                   `json:"multi_count"`
		Models     []adminMatrixEntryDTO `json:"models"`
	}
	_ = json.Unmarshal(mRec.Body.Bytes(), &matResp)
	if mRec.Code != http.StatusOK || len(matResp.Models) != 1 || matResp.Models[0].CanonicalID != "deepseek-r1" || matResp.Models[0].BackendCount != 3 {
		t.Fatalf("expected deepseek-r1 matrix entry with 3 backends, code=%d body=%s", mRec.Code, mRec.Body.String())
	}

	// 4. POST /api/gateway/models/{client}/failover persists all discovered backends into DB routes.
	syncReq := httptest.NewRequest(http.MethodPost, "/api/gateway/models/deepseek-r1/failover", strings.NewReader(`{"strategy":"failover"}`))
	syncRec := httptest.NewRecorder()
	rtr.ServeHTTP(syncRec, syncReq)
	var view adminModelViewDTO
	_ = json.Unmarshal(syncRec.Body.Bytes(), &view)
	if syncRec.Code != http.StatusOK || len(view.Candidates) != 3 || !view.MultiBackend {
		t.Fatalf("expected failover sync to materialize 3 DB candidates, code=%d view=%+v", syncRec.Code, view)
	}
	for _, cand := range view.Candidates {
		if cand.Source != "db" {
			t.Fatalf("expected synced candidate to have source=db, got %+v", cand)
		}
	}
}
