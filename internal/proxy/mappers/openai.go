package mappers

import (
	"encoding/json"
	"strings"
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

// UnmarshalJSON handles both string and array content formats
func (m *OpenAIMessage) UnmarshalJSON(data []byte) error {
	// Try simple struct first
	type Alias struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	m.Role = alias.Role

	// Try string content first
	var strContent string
	if err := json.Unmarshal(alias.Content, &strContent); err == nil {
		m.Content = strContent
		return nil
	}

	// Try array content (multimodal format)
	var arrayContent []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(alias.Content, &arrayContent); err == nil {
		// Concatenate all text parts
		var texts []string
		for _, part := range arrayContent {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		m.Content = strings.Join(texts, "\n")
		return nil
	}

	// Fallback: use raw string
	m.Content = string(alias.Content)
	return nil
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
	Contents          []GeminiContent         `json:"contents"`
	SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
}

type GeminiContent struct {
	Role  string       `json:"role,omitempty"` // Role is optional for systemInstruction
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

// Model mapping is now handled via database (see db.ResolveModel)
// Legacy hardcoded map removed in favor of config/model_routes.yaml

// OpenAIToGemini converts an OpenAI chat request to Gemini format
func OpenAIToGemini(req OpenAIChatRequest, resolvedModel, projectID string) GeminiRequest {
	contents := make([]GeminiContent, 0, len(req.Messages))
	var systemParts []GeminiPart
	
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemParts = append(systemParts, GeminiPart{Text: msg.Content})
			continue
		}

		role := msg.Role
		// Map OpenAI roles to Gemini roles
		if role == "assistant" {
			role = "model"
		}
		// Note: "system" is now handled separately above
		
		contents = append(contents, GeminiContent{
			Role: role,
			Parts: []GeminiPart{
				{Text: msg.Content},
			},
		})
	}
	
	// Use resolved model passed from handler
	model := resolvedModel
	
	payload := GeminiRequestPayload{
		Contents: contents,
	}

	// Add system instruction if present
	if len(systemParts) > 0 {
		payload.SystemInstruction = &GeminiContent{
			Parts: systemParts,
		}
	}
	
	geminiReq := GeminiRequest{
		Project:   projectID,
		RequestID: "req-" + time.Now().Format("20060102150405"),
		Model:     model,
		Request:   payload,
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
