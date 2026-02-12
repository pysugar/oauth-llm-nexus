package db

import "testing"

func TestAllowedProvidersByClientModel(t *testing.T) {
	tests := []struct {
		model string
		want  []string
	}{
		{model: "gpt-4o", want: []string{"codex", "google"}},
		{model: "GEMINI-3-FLASH", want: []string{"google", "vertex", "gemini"}},
		{model: "claude-sonnet-4-5", want: []string{"google"}},
		{model: "unknown-model", want: []string{"google"}},
	}

	for _, tc := range tests {
		got := AllowedProvidersByClientModel(tc.model)
		if len(got) != len(tc.want) {
			t.Fatalf("model=%s expected %v, got %v", tc.model, tc.want, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("model=%s expected %v, got %v", tc.model, tc.want, got)
			}
		}
	}
}

func TestValidateRouteProvider(t *testing.T) {
	if err := ValidateRouteProvider("gpt-4o", "codex"); err != nil {
		t.Fatalf("expected gpt -> codex to be valid, got err=%v", err)
	}
	if err := ValidateRouteProvider("gpt-4o", "vertex"); err == nil {
		t.Fatal("expected gpt -> vertex to be invalid")
	}
	if err := ValidateRouteProvider("gemini-3-flash-preview", "gemini"); err != nil {
		t.Fatalf("expected gemini -> gemini to be valid, got err=%v", err)
	}
	if err := ValidateRouteProvider("claude-opus-4-6", "google"); err != nil {
		t.Fatalf("expected claude -> google to be valid, got err=%v", err)
	}
	if err := ValidateRouteProvider("claude-opus-4-6", "codex"); err == nil {
		t.Fatal("expected claude -> codex to be invalid")
	}
}

func TestValidateProviderForProtocol(t *testing.T) {
	if err := ValidateProviderForProtocol("codex", string(ProtocolOpenAI)); err != nil {
		t.Fatalf("expected codex for openai to be valid, got err=%v", err)
	}
	if err := ValidateProviderForProtocol("vertex", string(ProtocolOpenAI)); err == nil {
		t.Fatal("expected vertex for openai to be invalid")
	}
	if err := ValidateProviderForProtocol("gemini", string(ProtocolGenAI)); err != nil {
		t.Fatalf("expected gemini for genai to be valid, got err=%v", err)
	}
	if err := ValidateProviderForProtocol("codex", string(ProtocolGenAI)); err == nil {
		t.Fatal("expected codex for genai to be invalid")
	}
}
