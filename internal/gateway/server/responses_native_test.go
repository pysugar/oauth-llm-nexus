package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

type upstreamCall struct {
	host, path string
	body       map[string]any
}

// respMock answers Responses (JSON or SSE) and Chat requests, recording each call. refuse makes a
// host's /responses answer 400 "Unsupported input item".
type respMock struct {
	mu     sync.Mutex
	calls  []upstreamCall
	refuse map[string]bool
	n      int
}

func (m *respMock) last(host string) upstreamCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.calls) - 1; i >= 0; i-- {
		if m.calls[i].host == host {
			return m.calls[i]
		}
	}
	return upstreamCall{}
}

func (m *respMock) roundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	m.mu.Lock()
	m.calls = append(m.calls, upstreamCall{host: req.URL.Host, path: req.URL.Path, body: body})
	m.n++
	id := "resp_" + req.URL.Host + "_" + itoa(m.n)
	refuse := m.refuse[req.URL.Host]
	m.mu.Unlock()

	rec := httptest.NewRecorder()
	stream, _ := body["stream"].(bool)
	switch {
	case strings.HasSuffix(req.URL.Path, "/responses") && refuse:
		rec.Header().Set("Content-Type", "application/json")
		rec.WriteHeader(400)
		_, _ = rec.WriteString(`{"error":{"message":"Unsupported input item: agent_message","type":"invalid_request_error"}}`)
	case strings.HasSuffix(req.URL.Path, "/responses") && stream:
		rec.Header().Set("Content-Type", "text/event-stream")
		_, _ = rec.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"" + id + "\",\"model\":\"m\",\"status\":\"in_progress\"}}\n\n" +
			"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"answer from " + req.URL.Host + "\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"" + id + "\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer from " + req.URL.Host + "\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
	case strings.HasSuffix(req.URL.Path, "/responses"):
		rec.Header().Set("Content-Type", "application/json")
		_, _ = rec.WriteString(`{"id":"` + id + `","object":"response","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer from ` + req.URL.Host + `"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	default: // chat completions (translated attempts always stream)
		rec.Header().Set("Content-Type", "text/event-stream")
		_, _ = rec.WriteString("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"chat from " + req.URL.Host + "\"}}]}\n\n" +
			"data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}
	res := rec.Result()
	res.Request = req
	return res, nil
}

// setupResponsesServer: "oa" is stateful (like OpenAI), "ds" is a stateless native Responses
// backend (like DeepSeek/Ollama/OpenRouter); both serve model "m" (oa first).
func setupResponsesServer(t *testing.T, m *respMock) *Server {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	_ = gdb.AutoMigrate(&models.Account{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.Config{})
	db.RefreshModelRouteCache(gdb)
	mk := func(id string, stateful bool) *provider.Provider {
		base := "http://" + id + ".test/v1"
		return &provider.Provider{
			ID: id, Enabled: true, ResponsesStateful: stateful,
			Endpoints: provider.Endpoints{OpenAIChat: base, OpenAIResponses: base},
			Native:    []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoOpenAIResp},
			Prefer:    ir.ProtoOpenAIChat,
			Keys:      provider.NewKeyRing(false, id+"-key"),
		}
	}
	srv := New(provider.NewRegistryWithProviders(&http.Client{Transport: rtFunc(m.roundTrip)}, mk("oa", true), mk("ds", false)), gdb, nil, LegacyHandlers{})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{ClientModel: "m", TargetProvider: "oa", TargetModel: "m", Priority: 0, IsActive: true})
	_ = db.CreateModelRoute(gdb, &models.ModelRoute{ClientModel: "m", TargetProvider: "ds", TargetModel: "m", Priority: 1, IsActive: true})
	return srv
}

func respCall(t *testing.T, srv *Server, provider, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	if provider != "" {
		req.Header.Set("X-Nexus-Provider", provider)
	}
	rec := httptest.NewRecorder()
	srv.HandleOpenAIResponses(rec, req)
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out.ID
}

func inputText(c upstreamCall) string {
	b, _ := json.Marshal(c.body["input"])
	return string(b)
}

func TestNativeResponsesContinuationsAcrossStatefulAndStatelessBackends(t *testing.T) {
	m := &respMock{refuse: map[string]bool{}}
	srv := setupResponsesServer(t, m)

	// Turn 1 on the stateless backend: plain native passthrough.
	rec, id1 := respCall(t, srv, "ds", `{"model":"m","input":"My name is Zed."}`)
	if rec.Code != 200 || rec.Header().Get("X-Nexus-Route") != "passthrough" || m.last("ds.test").path != "/v1/responses" {
		t.Fatalf("turn 1: %d %q %s", rec.Code, rec.Header().Get("X-Nexus-Route"), rec.Body.String())
	}

	// Turn 2 continues on the stateless backend: the gateway expands the conversation and never
	// sends previous_response_id (DeepSeek/Ollama would silently drop the context).
	rec, id2 := respCall(t, srv, "ds", `{"model":"m","previous_response_id":"`+id1+`","input":"What is my name?"}`)
	c := m.last("ds.test")
	if rec.Code != 200 || rec.Header().Get("X-Nexus-Route") != "translated:openai-responses" ||
		c.body["previous_response_id"] != nil || !strings.Contains(inputText(c), "My name is Zed.") || !strings.Contains(inputText(c), "answer from ds.test") {
		t.Fatalf("turn 2 must be expanded: %d %q body=%v", rec.Code, rec.Header().Get("X-Nexus-Route"), c.body)
	}

	// Turn 3 moves to the stateful backend: it did not issue id2, so it gets the history too.
	rec, id3 := respCall(t, srv, "oa", `{"model":"m","previous_response_id":"`+id2+`","input":"And again?"}`)
	c = m.last("oa.test")
	if rec.Code != 200 || c.body["previous_response_id"] != nil || !strings.Contains(inputText(c), "What is my name?") {
		t.Fatalf("turn 3 on a stateful backend for a foreign id must be expanded: %v", c.body)
	}

	// Turn 4 stays on the stateful backend that issued id3: raw passthrough with the id.
	rec, id4 := respCall(t, srv, "oa", `{"model":"m","previous_response_id":"`+id3+`","input":"Once more."}`)
	c = m.last("oa.test")
	if rec.Code != 200 || c.body["previous_response_id"] != id3 || rec.Header().Get("X-Nexus-Route") != "passthrough" {
		t.Fatalf("turn 4 must pass the stateful backend's own id through: %v", c.body)
	}

	// Turn 5 fails over to the stateless backend: the history saved for the passthrough turn 4
	// includes every earlier turn, not just turn 4's new input.
	_, _ = respCall(t, srv, "ds", `{"model":"m","previous_response_id":"`+id4+`","input":"Last."}`)
	c = m.last("ds.test")
	for _, want := range []string{"My name is Zed.", "What is my name?", "And again?", "Once more.", "Last."} {
		if !strings.Contains(inputText(c), want) {
			t.Fatalf("turn 5 history misses %q: %s", want, inputText(c))
		}
	}

	// An id the gateway never saw cannot be expanded for a stateless backend.
	if rec, _ := respCall(t, srv, "ds", `{"model":"m","previous_response_id":"resp_unknown","input":"x"}`); rec.Code != 400 {
		t.Fatalf("unknown id on a stateless backend must be refused, got %d", rec.Code)
	}
}

func TestNativeResponsesShapeRefusalFallsBackToChat(t *testing.T) {
	m := &respMock{refuse: map[string]bool{"ds.test": true}}
	srv := setupResponsesServer(t, m)
	body := `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo=","detail":"original"}]}]}`

	rec, _ := respCall(t, srv, "ds", body)
	if rec.Code != 200 || rec.Header().Get("X-Nexus-Route") != "translated:openai-chat" || !strings.Contains(rec.Body.String(), "chat from ds.test") {
		t.Fatalf("refused shape must be retried through Chat on the same backend: %d %q %s", rec.Code, rec.Header().Get("X-Nexus-Route"), rec.Body.String())
	}
	m.mu.Lock()
	var first upstreamCall
	for _, c := range m.calls {
		if c.host == "ds.test" && strings.HasSuffix(c.path, "/responses") {
			first = c
			break
		}
	}
	before := len(m.calls)
	m.mu.Unlock()
	if !strings.Contains(inputText(first), `"detail":"high"`) || strings.Contains(inputText(first), `"original"`) {
		t.Fatalf("image detail original must go as high to a non-OpenAI Responses API: %s", inputText(first))
	}

	// The refusal is remembered: the next request goes straight to Chat.
	_, _ = respCall(t, srv, "ds", body)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) != before+1 || !strings.HasSuffix(m.calls[len(m.calls)-1].path, "/chat/completions") {
		t.Fatalf("expected one direct chat call after the remembered refusal, got %+v", m.calls[before:])
	}
}

func TestNativeResponsesStreamingPassthrough(t *testing.T) {
	m := &respMock{refuse: map[string]bool{}}
	srv := setupResponsesServer(t, m)
	rec, _ := respCall(t, srv, "ds", `{"model":"m","stream":true,"input":"hi"}`)
	out := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("X-Nexus-Route") != "passthrough" || !strings.Contains(out, "response.completed") || strings.Contains(out, "upstream_incomplete") {
		t.Fatalf("native Responses stream must pass through complete: %d %s", rec.Code, out)
	}
	// The streamed turn was stored, so a continuation on a stateless backend can be expanded.
	var id string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"response.completed"`) {
			var ev struct {
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev)
			id = ev.Response.ID
		}
	}
	if _, ok := srv.RespStore.Lookup(id); !ok || srv.RespStore.Maker(id) != "ds" {
		t.Fatalf("streamed passthrough turn %q not stored with its maker", id)
	}
}
