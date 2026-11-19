package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
)

func withTransforms(p *provider.Provider) {
	p.ExtraBody = map[string]json.RawMessage{
		"prompt_cache_key": json.RawMessage(`"pkm"`),
		"!temperature":     json.RawMessage(`0.1`),
		"seed":             json.RawMessage(`7`),
	}
	p.SystemPrompts = map[string]string{"m": "House rules."}
}

func TestTransformsOnPassthroughAndTranslated(t *testing.T) {
	up := &upstreamCapture{reply: func(string) (string, []string) { return "text/event-stream", okScript.chunks }}
	srv := setupMaskServer(t, up, withTransforms)
	off := false
	srv.masking.settings.Store(&maskSettings{Enabled: &off})

	check := func(label string) {
		t.Helper()
		var got struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			PromptCacheKey string  `json:"prompt_cache_key"`
			Temperature    float64 `json:"temperature"`
			Seed           int     `json:"seed"`
		}
		if err := json.Unmarshal([]byte(up.last()), &got); err != nil {
			t.Fatalf("%s: %v: %s", label, err, up.last())
		}
		if got.PromptCacheKey != "pkm" || got.Temperature != 0.1 || got.Seed != 3 {
			t.Fatalf("%s: extra_body not merged as expected: %s", label, up.last())
		}
		sys := 0
		for _, m := range got.Messages {
			if m.Role == "system" && m.Content == "House rules." {
				sys++
			}
		}
		if sys != 1 {
			t.Fatalf("%s: system prompt not appended once: %s", label, up.last())
		}
	}

	// passthrough: OpenAI Chat client to an OpenAI Chat upstream (client seed 3 is kept)
	if rec := post(srv, "/v1/chat/completions", `{"model":"m","seed":3,"temperature":0.8,"stream":true,"messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("passthrough: %d %s", rec.Code, rec.Body)
	}
	check("passthrough")
	if !strings.HasPrefix(up.last(), `{"model":"m","seed":3,`) {
		t.Fatalf("untouched fields should keep their bytes: %s", up.last())
	}

	// translated: Anthropic client onto the OpenAI Chat upstream
	if rec := post(srv, "/v1/messages", `{"model":"m","max_tokens":16,"temperature":0.8,"metadata":{"user_id":"u"},"messages":[{"role":"user","content":"hi"}]}`); rec.Code != 200 {
		t.Fatalf("translated: %d %s", rec.Code, rec.Body)
	}
	var tr map[string]any
	_ = json.Unmarshal([]byte(up.last()), &tr)
	if tr["prompt_cache_key"] != "pkm" || tr["temperature"] != 0.1 || tr["seed"] != float64(7) {
		t.Fatalf("translated extra_body: %s", up.last())
	}
	if !strings.Contains(up.last(), `"content":"House rules."`) {
		t.Fatalf("translated system prompt: %s", up.last())
	}
}

func TestTransformOverridesPersistAndValidate(t *testing.T) {
	up := &upstreamCapture{reply: func(string) (string, []string) { return "text/event-stream", okScript.chunks }}
	srv := setupMaskServer(t, up, nil)
	if rec := patchProvider(t, srv, "a", `{"extra_body":{"messages":[]}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved extra_body accepted: %d %s", rec.Code, rec.Body)
	}
	rec := patchProvider(t, srv, "a", `{"unredacted":true,"extra_body":{"!reasoning_effort":"low"},"system_prompts":{"*":"Be brief."}}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unredacted":true`) || !strings.Contains(rec.Body.String(), `"system_prompts":{"*":"Be brief."}`) {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}

	// a restart: a fresh provider gets the persisted values back
	fresh := &provider.Provider{ID: "a", Enabled: true}
	srv.Registry = provider.NewRegistryWithProviders(nil, fresh)
	if _, err := ApplyPersistedProviderOverrides(srv.DB, srv.Registry); err != nil {
		t.Fatal(err)
	}
	if !fresh.IsUnredacted() || string(fresh.GetExtraBody()["!reasoning_effort"]) != `"low"` || fresh.SystemPromptFor("x") != "Be brief." {
		t.Fatalf("not restored: unredacted=%v extra=%v prompts=%v", fresh.IsUnredacted(), fresh.GetExtraBody(), fresh.GetSystemPrompts())
	}

	// null clears, and the cleared state persists over providers.yaml values
	if rec := patchProvider(t, srv, "a", `{"extra_body":null}`); rec.Code != 200 {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	again := &provider.Provider{ID: "a", ExtraBody: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}
	_, _ = ApplyPersistedProviderOverrides(srv.DB, provider.NewRegistryWithProviders(nil, again))
	if again.GetExtraBody() != nil {
		t.Fatalf("cleared extra_body came back: %v", again.GetExtraBody())
	}
}
