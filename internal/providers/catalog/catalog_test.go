package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogLoadAndModelScopes(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "openai_compat_providers.yaml")
	cfg := `providers:
  - id: openrouter
    enabled: true
    base_url: https://openrouter.ai/api/v1
    auth_mode: bearer
    model_scope: all_models
    capabilities: [openai.chat]
  - id: nvidia
    enabled: true
    base_url: https://integrate.api.nvidia.com/v1
    auth_mode: bearer
    model_scope: unknown_prefix_only
    capabilities: [openai.chat]
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("NEXUS_OPENAI_COMPAT_PROVIDERS_FILE", cfgPath)
	t.Setenv("NEXUS_OPENROUTER_API_KEY", "or-test-key")
	t.Setenv("NEXUS_NVIDIA_API_KEY", "nv-test-key")

	if err := InitFromEnvAndConfig(); err != nil {
		t.Fatalf("init catalog: %v", err)
	}

	openrouter, ok := GetProvider("openrouter")
	if !ok {
		t.Fatal("expected openrouter provider")
	}
	if !openrouter.Enabled || !openrouter.RuntimeEnabled {
		t.Fatalf("expected openrouter enabled/runtime_enabled true, got %+v", openrouter)
	}

	nvidia, ok := GetProvider("nvidia")
	if !ok {
		t.Fatal("expected nvidia provider")
	}
	if !nvidia.Enabled || !nvidia.RuntimeEnabled {
		t.Fatalf("expected nvidia enabled/runtime_enabled true, got %+v", nvidia)
	}

	gptAllowed := AllowedProviderIDsForModel("gpt-4o")
	if !contains(gptAllowed, "openrouter") {
		t.Fatalf("expected gpt model to include openrouter, got %v", gptAllowed)
	}
	if contains(gptAllowed, "nvidia") {
		t.Fatalf("expected gpt model to exclude nvidia, got %v", gptAllowed)
	}

	unknownAllowed := AllowedProviderIDsForModel("my-company-model")
	if !contains(unknownAllowed, "openrouter") || !contains(unknownAllowed, "nvidia") {
		t.Fatalf("expected unknown model to include openrouter+nvidia, got %v", unknownAllowed)
	}

	openAIChat := ProviderIDsByCapability(CapabilityOpenAIChat)
	if !contains(openAIChat, "openrouter") || !contains(openAIChat, "nvidia") {
		t.Fatalf("expected openai.chat providers openrouter+nvidia, got %v", openAIChat)
	}
}

func TestCatalogEnvOverrides(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "openai_compat_providers.yaml")
	cfg := `providers:
  - id: openrouter
    enabled: true
    base_url: https://openrouter.ai/api/v1
    auth_mode: bearer
    model_scope: all_models
    capabilities: [openai.chat]
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("NEXUS_OPENAI_COMPAT_PROVIDERS_FILE", cfgPath)
	t.Setenv("NEXUS_OPENROUTER_API_KEY", "or-test-key")
	t.Setenv("NEXUS_OPENROUTER_BASE_URL", "https://example.com/v1")
	t.Setenv("NEXUS_OPENROUTER_STATIC_HEADERS", `{"X-Test":"yes"}`)

	if err := InitFromEnvAndConfig(); err != nil {
		t.Fatalf("init catalog: %v", err)
	}

	info, ok := GetProvider("openrouter")
	if !ok {
		t.Fatal("expected openrouter provider")
	}
	if info.BaseURL != "https://example.com/v1" {
		t.Fatalf("expected env base URL override, got %s", info.BaseURL)
	}
	if strings.TrimSpace(info.StaticHeaders["X-Test"]) != "yes" {
		t.Fatalf("expected static header override, got %+v", info.StaticHeaders)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
