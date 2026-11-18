package server

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

type embedCapture struct {
	mu          sync.Mutex
	openaiBody  []map[string]any
	nvidiaBody  []map[string]any
	geminiBody  []map[string]any
	geminiPaths []string
	flaky503    int
}

func (c *embedCapture) record(dst *[]map[string]any, r *http.Request) map[string]any {
	raw, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	c.mu.Lock()
	*dst = append(*dst, m)
	c.mu.Unlock()
	return m
}

func openAIEmbeddingResponse(model string, n int) string {
	items := make([]string, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, `{"object":"embedding","index":`+itoa(i)+`,"embedding":[0.1,0.2]}`)
	}
	return `{"object":"list","model":"` + model + `","data":[` + strings.Join(items, ",") + `],"usage":{"prompt_tokens":5,"total_tokens":5}}`
}

func setupEmbeddingServer(t *testing.T, capt *embedCapture) (*Server, *gorm.DB) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.RefreshModelRouteCache(gdb)

	client := &http.Client{Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		switch req.URL.Host {
		case "mock.openai":
			if req.URL.Path == "/v1/models" {
				_, _ = rec.WriteString(`{"data":[{"id":"gpt-4o"},{"id":"text-embedding-3-small"}]}`)
				return rec.Result(), nil
			}
			if req.Header.Get("Authorization") == "Bearer key-bad" {
				rec.WriteHeader(http.StatusUnauthorized)
				_, _ = rec.WriteString(`{"error":{"message":"invalid key"}}`)
				return rec.Result(), nil
			}
			m := capt.record(&capt.openaiBody, req)
			model, _ := m["model"].(string)
			_, _ = rec.WriteString(openAIEmbeddingResponse(model, 1))
			return rec.Result(), nil
		case "mock.nvidia":
			if req.URL.Path == "/v1/models" {
				_, _ = rec.WriteString(`{"data":[{"id":"meta/llama-3.3-70b-instruct"},{"id":"nvidia/nv-embedqa-e5-v5"}]}`)
				return rec.Result(), nil
			}
			capt.mu.Lock()
			fail := capt.flaky503 > 0
			if fail {
				capt.flaky503--
			}
			capt.mu.Unlock()
			if fail {
				rec.WriteHeader(http.StatusServiceUnavailable)
				_, _ = rec.WriteString(`{"error":{"message":"overloaded"}}`)
				return rec.Result(), nil
			}
			m := capt.record(&capt.nvidiaBody, req)
			model, _ := m["model"].(string)
			_, _ = rec.WriteString(openAIEmbeddingResponse(model, 1))
			return rec.Result(), nil
		case "mock.gemini":
			if strings.HasSuffix(req.URL.Path, "/models") {
				_, _ = rec.WriteString(`{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-embedding-001","supportedGenerationMethods":["embedContent"]}]}`)
				return rec.Result(), nil
			}
			if req.Header.Get("x-goog-api-key") != "gem-key" {
				rec.WriteHeader(http.StatusUnauthorized)
				return rec.Result(), nil
			}
			capt.mu.Lock()
			capt.geminiPaths = append(capt.geminiPaths, req.URL.Path)
			capt.mu.Unlock()
			m := capt.record(&capt.geminiBody, req)
			reqs, _ := m["requests"].([]any)
			embs := make([]string, 0, len(reqs))
			for range reqs {
				embs = append(embs, `{"values":[0.5,-0.25]}`)
			}
			_, _ = rec.WriteString(`{"embeddings":[` + strings.Join(embs, ",") + `]}`)
			return rec.Result(), nil
		}
		rec.WriteHeader(http.StatusNotFound)
		return rec.Result(), nil
	})}

	oai := &provider.Provider{
		ID: "openai", Enabled: true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.openai/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Keys:      provider.NewKeyRing(false, "key-bad", "key-good"),
	}
	nv := &provider.Provider{
		ID: "nvidia", Enabled: true,
		Endpoints: provider.Endpoints{OpenAIChat: "http://mock.nvidia/v1"},
		Native:    []ir.Protocol{ir.ProtoOpenAIChat},
		Keys:      provider.NewKeyRing(false, "nv-key"),
		Quirks:    codec.Quirks{EmbeddingInputType: true},
	}
	gem := &provider.Provider{
		ID: "gemini", Enabled: true,
		Endpoints: provider.Endpoints{Gemini: "http://mock.gemini/v1beta"},
		Native:    []ir.Protocol{ir.ProtoGemini},
		Keys:      provider.NewKeyRing(false, "gem-key"),
	}
	reg := provider.NewRegistryWithProviders(client, oai, nv, gem)
	return New(reg, gdb, nil, LegacyHandlers{}), gdb
}

func postEmbeddings(t *testing.T, srv *Server, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.HandleOpenAIEmbeddings(rec, req)
	return rec
}

func TestEmbeddingsAliasPassthroughStripsHintAndRotatesKey(t *testing.T) {
	capt := &embedCapture{}
	srv, gdb := setupEmbeddingServer(t, capt)
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "pkm-embed", TargetProvider: "openai", TargetModel: "text-embedding-3-small", IsActive: true,
	})

	rec := postEmbeddings(t, srv, `{"model":"pkm-embed","input":["a"],"input_type":"document","dimensions":256}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Nexus-Provider"); got != "openai" {
		t.Fatalf("expected openai provider, got %q", got)
	}
	if len(capt.openaiBody) != 1 {
		t.Fatalf("expected one successful upstream call, got %d", len(capt.openaiBody))
	}
	sent := capt.openaiBody[0]
	if sent["model"] != "text-embedding-3-small" {
		t.Fatalf("model not rewritten: %v", sent["model"])
	}
	if _, ok := sent["input_type"]; ok {
		t.Fatalf("input_type must be stripped for strict OpenAI backends: %v", sent)
	}
	if sent["dimensions"] != float64(256) {
		t.Fatalf("dimensions must pass through: %v", sent)
	}
	snap := srv.Metrics.ProviderSnapshot("openai")
	if snap == nil || snap.Requests == 0 {
		t.Fatalf("expected metrics recorded for openai, got %+v", snap)
	}
}

func TestEmbeddingsGeminiTranslationBatchingTaskTypeAndBase64(t *testing.T) {
	capt := &embedCapture{}
	srv, _ := setupEmbeddingServer(t, capt)

	inputs := make([]string, 150)
	for i := range inputs {
		inputs[i] = "note"
	}
	body, _ := json.Marshal(map[string]any{
		"model":           "gemini-embedding-001",
		"input":           inputs,
		"input_type":      "query",
		"dimensions":      768,
		"encoding_format": "base64",
	})
	rec := postEmbeddings(t, srv, string(body), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Nexus-Provider") != "gemini" || rec.Header().Get("X-Nexus-Route") != "translated:gemini" {
		t.Fatalf("unexpected provenance headers: %+v", rec.Header())
	}
	if len(capt.geminiBody) != 2 {
		t.Fatalf("expected 2 batched gemini calls for 150 inputs, got %d", len(capt.geminiBody))
	}
	if !strings.HasSuffix(capt.geminiPaths[0], "/models/gemini-embedding-001:batchEmbedContents") {
		t.Fatalf("unexpected gemini path %q", capt.geminiPaths[0])
	}
	first := capt.geminiBody[0]["requests"].([]any)[0].(map[string]any)
	if first["taskType"] != "RETRIEVAL_QUERY" || first["outputDimensionality"] != float64(768) || first["model"] != "models/gemini-embedding-001" {
		t.Fatalf("unexpected gemini embed request: %v", first)
	}

	var out struct {
		Object string `json:"object"`
		Data   []struct {
			Index     int    `json:"index"`
			Embedding string `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	if out.Object != "list" || len(out.Data) != 150 || out.Data[149].Index != 149 {
		t.Fatalf("unexpected response shape: object=%s n=%d", out.Object, len(out.Data))
	}
	raw, err := base64.StdEncoding.DecodeString(out.Data[0].Embedding)
	if err != nil || len(raw) != 8 {
		t.Fatalf("expected 2 float32 values in base64, got err=%v len=%d", err, len(raw))
	}
	if v := math.Float32frombits(binary.LittleEndian.Uint32(raw[4:])); v != -0.25 {
		t.Fatalf("unexpected decoded value %v", v)
	}
}

func TestEmbeddingsVendorNamespacedCatalogModelKeepsFullIDAndPassageHint(t *testing.T) {
	capt := &embedCapture{}
	srv, _ := setupEmbeddingServer(t, capt)

	rec := postEmbeddings(t, srv, `{"model":"nvidia/nv-embedqa-e5-v5","input":"hello","input_type":"document"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(capt.nvidiaBody) != 1 {
		t.Fatalf("expected nvidia call, got %d", len(capt.nvidiaBody))
	}
	if capt.nvidiaBody[0]["model"] != "nvidia/nv-embedqa-e5-v5" {
		t.Fatalf("vendor-namespaced model must not be stripped: %v", capt.nvidiaBody[0]["model"])
	}
	if capt.nvidiaBody[0]["input_type"] != "passage" {
		t.Fatalf("expected document→passage mapping for nvidia, got %v", capt.nvidiaBody[0]["input_type"])
	}
}

func TestEmbeddingsFailoverAcrossRoutesAndErrors(t *testing.T) {
	capt := &embedCapture{flaky503: 1}
	srv, gdb := setupEmbeddingServer(t, capt)
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "kb-embed", TargetProvider: "nvidia", TargetModel: "nvidia/nv-embedqa-e5-v5", Priority: 0, IsActive: true,
	})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{
		ClientModel: "kb-embed", TargetProvider: "openai", TargetModel: "text-embedding-3-small", Priority: 1, IsActive: true,
	})

	rec := postEmbeddings(t, srv, `{"model":"kb-embed","input":"x"}`, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Nexus-Provider") != "openai" {
		t.Fatalf("expected failover to openai, got %d provider=%q body=%s",
			rec.Code, rec.Header().Get("X-Nexus-Provider"), rec.Body.String())
	}

	if rec := postEmbeddings(t, srv, `{"model":"no-such-embed","input":"x"}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown embedding model, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postEmbeddings(t, srv, `{"model":"gemini-embedding-001"}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing input, got %d", rec.Code)
	}
	if rec := postEmbeddings(t, srv, `{"model":"gemini-embedding-001","input":[[1,2,3]]}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for token-array input on gemini, got %d: %s", rec.Code, rec.Body.String())
	}
}
