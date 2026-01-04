package mappers

import (
	"encoding/json"
	"time"
)

// Anthropic (Claude) Request/Response structures

type ClaudeRequest struct {
	Model       string          `json:"model"`
	Messages    []ClaudeMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	System      string          `json:"system,omitempty"`
}

type ClaudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ClaudeResponse struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Role         string              `json:"role"`
	Content      []ClaudeContentBlock `json:"content"`
	Model        string              `json:"model"`
	StopReason   string              `json:"stop_reason,omitempty"`
	StopSequence *string             `json:"stop_sequence,omitempty"`
	Usage        ClaudeUsage         `json:"usage"`
}

type ClaudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ClaudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Claude streaming events
type ClaudeStreamEvent struct {
	Type         string              `json:"type"`
	Message      *ClaudeResponse     `json:"message,omitempty"`
	Index        int                 `json:"index,omitempty"`
	ContentBlock *ClaudeContentBlock `json:"content_block,omitempty"`
	Delta        *ClaudeDelta        `json:"delta,omitempty"`
}

type ClaudeDelta struct {
	Type       string `json:"type,omitempty"`
	Text       string `json:"text,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

// ClaudeToGemini converts a Claude request to Gemini format
func ClaudeToGemini(req ClaudeRequest, resolvedModel, projectID string) GeminiRequest {
	contents := make([]GeminiContent, 0, len(req.Messages)+1)
	
	// Add system message as first user message if present
	if req.System != "" {
		contents = append(contents, GeminiContent{
			Role: "user",
			Parts: []GeminiPart{
				{Text: "[System]: " + req.System},
			},
		})
	}
	
	for _, msg := range req.Messages {
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}
		
		contents = append(contents, GeminiContent{
			Role: role,
			Parts: []GeminiPart{
				{Text: msg.Content},
			},
		})
	}
	
	// Use resolved model passed from handler
	model := resolvedModel
	
	geminiReq := GeminiRequest{
		Project:   projectID,
		RequestID: "req-" + time.Now().Format("20060102150405"),
		Model:     model,
		Request: GeminiRequestPayload{
			Contents: contents,
		},
	}
	
	// Map generation config
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	geminiReq.Request.GenerationConfig = &GeminiGenerationConfig{
		Temperature:     req.Temperature,
		MaxOutputTokens: &maxTokens,
		TopP:            req.TopP,
	}
	
	return geminiReq
}

// GeminiToClaude converts a Gemini response to Claude format
func GeminiToClaude(geminiResp map[string]interface{}, model string) ([]byte, error) {
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
	
	resp := ClaudeResponse{
		ID:         "msg-nexus-" + time.Now().Format("20060102150405"),
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		StopReason: "end_turn",
		Content: []ClaudeContentBlock{
			{
				Type: "text",
				Text: text,
			},
		},
		Usage: ClaudeUsage{
			InputTokens:  0,
			OutputTokens: len(text) / 4, // Rough estimate
		},
	}
	
	return json.Marshal(resp)
}

// CreateClaudeStreamEvent creates a Claude SSE event
func CreateClaudeStreamEvent(eventType string, data interface{}) ([]byte, error) {
	event := ClaudeStreamEvent{
		Type: eventType,
	}
	
	switch eventType {
	case "message_start":
		if msg, ok := data.(*ClaudeResponse); ok {
			event.Message = msg
		}
	case "content_block_delta":
		if delta, ok := data.(*ClaudeDelta); ok {
			event.Index = 0
			event.Delta = delta
		}
	case "message_delta":
		event.Delta = &ClaudeDelta{StopReason: "end_turn"}
	}
	
	return json.Marshal(event)
}
