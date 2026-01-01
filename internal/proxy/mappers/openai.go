package mappers

import (
	"encoding/json"
	"time"
)

// OpenAI Request/Response structures

type OpenAIChatRequest struct {
	Model       string           `json:"model"`
	Messages    []OpenAIMessage  `json:"messages"`
	Stream      bool             `json:"stream,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	TopP        *float64         `json:"top_p,omitempty"`
	Stop        []string         `json:"stop,omitempty"`
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIChatResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

type OpenAIChoice struct {
	Index        int          `json:"index"`
	Message      OpenAIMessage `json:"message,omitempty"`
	Delta        *OpenAIMessage `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason,omitempty"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAI streaming chunk
type OpenAIStreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
}

// Gemini Internal structures
type GeminiRequest struct {
	Project   string                 `json:"project"`
	RequestID string                 `json:"requestId"`
	Model     string                 `json:"model"`
	Request   GeminiRequestPayload   `json:"request"`
}

type GeminiRequestPayload struct {
	Contents         []GeminiContent         `json:"contents"`
	GenerationConfig *GeminiGenerationConfig `json:"generationConfig,omitempty"`
}

type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text string `json:"text,omitempty"`
}

type GeminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// Model mapping from OpenAI names to Gemini internal names
var ModelMapping = map[string]string{
	// GPT-4 family -> Gemini 3 Pro
	"gpt-4":             "gemini-3-pro-high",
	"gpt-4-turbo":       "gemini-3-pro-high",
	"gpt-4o":            "gemini-3-pro-high",
	"gpt-4o-mini":       "gemini-3-pro-low",
	
	// GPT-3.5 -> Gemini Flash
	"gpt-3.5-turbo":     "gemini-3-flash",
	"gpt-3.5":           "gemini-3-flash",
	
	// o1 family -> Gemini 2.5 Pro (thinking)
	"o1":                "gemini-2.5-pro",
	"o1-preview":        "gemini-2.5-pro",
	"o1-mini":           "gemini-2.5-flash",
	
	// Claude family -> mapped models
	"claude-3-opus":     "gemini-3-pro-high",
	"claude-3-sonnet":   "claude-sonnet-4-5",
	"claude-3-haiku":    "gemini-3-flash",
	"claude-3.5-sonnet": "claude-sonnet-4-5-thinking",
	
	// Direct Gemini names
	"gemini-pro":        "gemini-2.5-pro",
	"gemini-flash":      "gemini-2.5-flash",
	"gemini-2.5-pro":    "gemini-2.5-pro",
	"gemini-2.5-flash":  "gemini-2.5-flash",
	"gemini-3-pro":      "gemini-3-pro-high",
	"gemini-3-flash":    "gemini-3-flash",
}

// OpenAIToGemini converts an OpenAI chat request to Gemini format
func OpenAIToGemini(req OpenAIChatRequest, projectID string) GeminiRequest {
	contents := make([]GeminiContent, 0, len(req.Messages))
	
	for _, msg := range req.Messages {
		role := msg.Role
		// Map OpenAI roles to Gemini roles
		if role == "assistant" {
			role = "model"
		} else if role == "system" {
			role = "user" // Gemini doesn't have system role, treat as user
		}
		
		contents = append(contents, GeminiContent{
			Role: role,
			Parts: []GeminiPart{
				{Text: msg.Content},
			},
		})
	}
	
	// Map model name
	model := req.Model
	if mapped, ok := ModelMapping[model]; ok {
		model = mapped
	}
	
	geminiReq := GeminiRequest{
		Project:   projectID,
		RequestID: "req-" + time.Now().Format("20060102150405"),
		Model:     model,
		Request: GeminiRequestPayload{
			Contents: contents,
		},
	}
	
	// Map generation config
	if req.Temperature != nil || req.MaxTokens != nil || req.TopP != nil || len(req.Stop) > 0 {
		geminiReq.Request.GenerationConfig = &GeminiGenerationConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
			TopP:            req.TopP,
			StopSequences:   req.Stop,
		}
	}
	
	return geminiReq
}

// GeminiToOpenAI converts a Gemini response to OpenAI format
func GeminiToOpenAI(geminiResp map[string]interface{}, model string, isStreaming bool) ([]byte, error) {
	// Extract text from Gemini response
	text := ""
	if candidates, ok := geminiResp["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					if part, ok := parts[0].(map[string]interface{}); ok {
						if t, ok := part["text"].(string); ok {
							text = t
						}
					}
				}
			}
		}
	}
	
	if isStreaming {
		chunk := OpenAIStreamChunk{
			ID:      "chatcmpl-nexus",
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []OpenAIChoice{
				{
					Index: 0,
					Delta: &OpenAIMessage{
						Role:    "assistant",
						Content: text,
					},
				},
			},
		}
		return json.Marshal(chunk)
	}
	
	resp := OpenAIChatResponse{
		ID:      "chatcmpl-nexus-" + time.Now().Format("20060102150405"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: text,
				},
				FinishReason: stringPtr("stop"),
			},
		},
	}
	
	return json.Marshal(resp)
}

func stringPtr(s string) *string {
	return &s
}
