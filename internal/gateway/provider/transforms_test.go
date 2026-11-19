package provider

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestYAMLTransformsAndUnredacted(t *testing.T) {
	raw := `
providers:
  - id: openrouter
    extra_body:
      provider: {order: [anthropic, openai], allow_fallbacks: false}
      "!usage": {include: true}
      messages: []
    system_prompts:
      "*": "Be brief."
      "anthropic/claude-*": "Use British spelling."
  - id: ollama
  - id: lan-ollama
    preset: ollama
    unredacted: false
`
	var fc ProviderFileConfig
	if err := yaml.Unmarshal([]byte(raw), &fc); err != nil {
		t.Fatal(err)
	}
	or := materializeProvider(fc.Providers[0])
	eb := or.GetExtraBody()
	if string(eb["provider"]) != `{"allow_fallbacks":false,"order":["anthropic","openai"]}` || string(eb["!usage"]) != `{"include":true}` {
		t.Fatalf("extra_body %v", eb)
	}
	if _, ok := eb["messages"]; ok {
		t.Fatal("reserved field kept")
	}
	if or.IsUnredacted() {
		t.Fatal("cloud provider unredacted")
	}
	if got := or.SystemPromptFor("anthropic/claude-sonnet-4.5"); got != "Use British spelling." {
		t.Fatalf("specific prompt: %q", got)
	}
	if got := or.SystemPromptFor("openai/gpt-5"); got != "Be brief." {
		t.Fatalf("catch-all prompt: %q", got)
	}
	if !materializeProvider(fc.Providers[1]).IsUnredacted() {
		t.Fatal("ollama preset should skip masking by default")
	}
	if materializeProvider(fc.Providers[2]).IsUnredacted() {
		t.Fatal("unredacted: false not honored")
	}
}
