package provider

import (
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

// Preset defines the default configuration template for a known LLM API provider.
type Preset struct {
	ID             string
	DisplayName    string
	Endpoints      Endpoints
	Native         []ir.Protocol
	Prefer         ir.Protocol
	AuthMode       string // "bearer", "x-api-key", "x-goog-api-key", "none"
	KeyStrategy    string // "sticky" (default) or "round-robin"
	NoKey          bool
	FreeOnly       bool
	IncludeNonChat bool
	StaticHeaders  map[string]string
	Timeout        time.Duration
	Quirks         codec.Quirks
	StaticModels   []string // models of a provider with no /models listing
	NoChat         bool     // embeddings/rerank only: never a chat target
}

// DefaultPresetOrder lists the 7 built-in API aggregation presets in default fallback priority order.
var DefaultPresetOrder = []string{
	"openai",
	"openrouter",
	"nvidia",
	"amd",
	"ollama",
	"deepseek",
	"gemini",
	"voyage",
}

var builtInPresets = map[string]Preset{
	"openai": {
		ID:          "openai",
		DisplayName: "OpenAI",
		Endpoints: Endpoints{
			OpenAIChat:      "https://api.openai.com/v1",
			OpenAIResponses: "https://api.openai.com/v1",
		},
		Native:   []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoOpenAIResp},
		Prefer:   ir.ProtoOpenAIChat,
		AuthMode: "bearer",
		Timeout:  180 * time.Second,
		Quirks: codec.Quirks{
			MaxCompletionTokens:  true,
			IncludeUsageInStream: true,
		},
	},
	"openrouter": {
		ID:          "openrouter",
		DisplayName: "OpenRouter",
		Endpoints: Endpoints{
			OpenAIChat: "https://openrouter.ai/api/v1",
			Anthropic:  "https://openrouter.ai/api",
		},
		Native:   []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoAnthropic},
		Prefer:   ir.ProtoOpenAIChat,
		AuthMode: "bearer",
		FreeOnly: true,
		StaticHeaders: map[string]string{
			"HTTP-Referer": "https://github.com/pysugar/oauth-llm-nexus",
			"X-Title":      "oauth-llm-nexus",
		},
		Timeout: 180 * time.Second,
		Quirks: codec.Quirks{
			IncludeUsageInStream: true,
			ThoughtTagParsing:    true,
		},
	},
	"nvidia": {
		ID:          "nvidia",
		DisplayName: "NVIDIA NIM",
		Endpoints: Endpoints{
			OpenAIChat: "https://integrate.api.nvidia.com/v1",
		},
		Native:   []ir.Protocol{ir.ProtoOpenAIChat},
		Prefer:   ir.ProtoOpenAIChat,
		AuthMode: "bearer",
		Timeout:  180 * time.Second,
		Quirks: codec.Quirks{
			IncludeUsageInStream: true,
			ThoughtTagParsing:    true,
			OmitStrictTools:      true,
			StreamFirst:          true,
			EmbeddingInputType:   true,
		},
	},
	"amd": {
		ID:          "amd",
		DisplayName: "AMD Radeon Token Factory",
		Endpoints: Endpoints{
			OpenAIChat: "https://developer.amd.com.cn/radeon/api/v1",
			Anthropic:  "https://developer.amd.com.cn/radeon/api",
		},
		Native:   []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoAnthropic},
		Prefer:   ir.ProtoOpenAIChat,
		AuthMode: "bearer",
		Timeout:  180 * time.Second,
		Quirks: codec.Quirks{
			IncludeUsageInStream: true,
			ThoughtTagParsing:    true,
			OmitStrictTools:      true,
		},
	},
	"ollama": {
		ID:          "ollama",
		DisplayName: "Ollama Local",
		Endpoints: Endpoints{
			OpenAIChat: "http://localhost:11434/v1",
			Anthropic:  "http://localhost:11434",
		},
		Native:   []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoAnthropic},
		Prefer:   ir.ProtoOpenAIChat,
		AuthMode: "none",
		NoKey:    true,
		Timeout:  300 * time.Second,
		Quirks: codec.Quirks{
			IncludeUsageInStream: true,
			ThoughtTagParsing:    true,
			OmitStrictTools:      true,
		},
	},
	"deepseek": {
		ID:          "deepseek",
		DisplayName: "DeepSeek API",
		Endpoints: Endpoints{
			OpenAIChat: "https://api.deepseek.com/v1",
			Anthropic:  "https://api.deepseek.com/anthropic",
		},
		Native:   []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoAnthropic},
		Prefer:   ir.ProtoOpenAIChat,
		AuthMode: "bearer",
		Timeout:  180 * time.Second,
		Quirks: codec.Quirks{
			IncludeUsageInStream: true,
			EchoReasoningContent: true,
			OmitReasoningEffort:  true,
		},
	},
	"gemini": {
		ID:          "gemini",
		DisplayName: "Google Gemini API",
		Endpoints: Endpoints{
			Gemini: "https://generativelanguage.googleapis.com/v1beta",
		},
		Native:      []ir.Protocol{ir.ProtoGemini},
		Prefer:      ir.ProtoGemini,
		AuthMode:    "x-goog-api-key",
		KeyStrategy: KeyStrategyRoundRobin,
		Timeout:     180 * time.Second,
		Quirks:      codec.Quirks{},
	},
	"voyage": {
		ID:          "voyage",
		DisplayName: "Voyage AI",
		Endpoints:   Endpoints{OpenAIChat: "https://api.voyageai.com/v1"},
		AuthMode:    "bearer",
		Timeout:     60 * time.Second,
		NoChat:      true,
		Quirks:      codec.Quirks{EmbeddingInputType: true},
		StaticModels: []string{
			"voyage-4-large", "voyage-4", "voyage-4-lite", "voyage-code-4", "voyage-3-large", "voyage-3.5",
			"voyage-3.5-lite", "voyage-code-3", "voyage-finance-2", "voyage-law-2",
			"rerank-3", "rerank-3-lite", "rerank-2.5", "rerank-2.5-lite",
		},
	},
}

// GetPreset returns a built-in provider preset by name, if one exists.
func GetPreset(name string) (Preset, bool) {
	p, ok := builtInPresets[name]
	return p, ok
}
