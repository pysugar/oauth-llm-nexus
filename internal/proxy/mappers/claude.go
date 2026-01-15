package mappers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
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
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []ClaudeContentBlock `json:"content"`
	Model        string               `json:"model"`
	StopReason   string               `json:"stop_reason,omitempty"`
	StopSequence *string              `json:"stop_sequence,omitempty"`
	Usage        ClaudeUsage          `json:"usage"`
}

type ClaudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Fields for tool_use type
	ID    string      `json:"id,omitempty"`
	Name  string      `json:"name,omitempty"`
	Input interface{} `json:"input,omitempty"`
}

type ClaudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Claude streaming events
type ClaudeStreamEvent struct {
	Type         string              `json:"type"`
	Message      *ClaudeResponse     `json:"message,omitempty"`
	Index        *int                `json:"index,omitempty"`
	ContentBlock *ClaudeContentBlock `json:"content_block,omitempty"`
	Delta        *ClaudeDelta        `json:"delta,omitempty"`
}

type ClaudeDelta struct {
	Type       string `json:"type,omitempty"`
	Text       string `json:"text,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

// GenerateToolUseID creates a tool use ID in format: {funcName}-{random8hex}
// This format allows stateless extraction of function name from tool_use_id in tool_result
func GenerateToolUseID(funcName string) string {
	b := make([]byte, 4)
	rand.Read(b)
	randomHex := hex.EncodeToString(b)
	return funcName + "-" + randomHex
}

// GenerateToolUseIDFromHandler is an alias for GenerateToolUseID for use in handlers package
func GenerateToolUseIDFromHandler(funcName string) string {
	return GenerateToolUseID(funcName)
}

// ExtractFunctionName extracts function name from tool_use_id
// Format: {funcName}-{random8hex} or legacy format (toolu_*)
// If parsing fails, returns the original ID with a warning log
func ExtractFunctionName(toolUseId string) string {
	// Try to find the last '-' separator
	if idx := strings.LastIndex(toolUseId, "-"); idx > 0 {
		suffix := toolUseId[idx+1:]
		// Verify suffix is 8-character hex
		if len(suffix) == 8 {
			if _, err := hex.DecodeString(suffix); err == nil {
				return toolUseId[:idx]
			}
		}
	}
	// Fallback: return original ID and log warning
	log.Printf("⚠️ [ExtractFunctionName] Cannot parse function name from tool_use_id: %s (using as-is)", toolUseId)
	return toolUseId
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
// Handles both text parts and functionCall parts (for tool use)
func GeminiToClaude(geminiResp map[string]interface{}, model string) ([]byte, error) {
	var contentBlocks []ClaudeContentBlock

	// Extract usage metadata from Gemini response
	var inputTokens, outputTokens int
	if usageMetadata, ok := geminiResp["usageMetadata"].(map[string]interface{}); ok {
		if promptTokens, ok := usageMetadata["promptTokenCount"].(float64); ok {
			inputTokens = int(promptTokens)
		}
		if candidatesTokens, ok := usageMetadata["candidatesTokenCount"].(float64); ok {
			outputTokens = int(candidatesTokens)
		}
	}

	if candidates, ok := geminiResp["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok {
					for _, part := range parts {
						if p, ok := part.(map[string]interface{}); ok {
							// Check for functionCall (tool use)
							if functionCall, ok := p["functionCall"].(map[string]interface{}); ok {
								name, _ := functionCall["name"].(string)
								args := functionCall["args"]

								// Generate a unique ID for the tool use with embedded function name
								// Format: {funcName}-{random8hex} for stateless name extraction
								toolUseID := GenerateToolUseID(name)

								contentBlocks = append(contentBlocks, ClaudeContentBlock{
									Type:  "tool_use",
									ID:    toolUseID,
									Name:  name,
									Input: args,
								})
								continue
							}

							// Check for text
							if text, ok := p["text"].(string); ok && text != "" {
								contentBlocks = append(contentBlocks, ClaudeContentBlock{
									Type: "text",
									Text: text,
								})
							}
						}
					}
				}
			}
		}
	}

	// If no content blocks found, add empty text block
	if len(contentBlocks) == 0 {
		contentBlocks = []ClaudeContentBlock{{Type: "text", Text: ""}}
	}

	// Determine stop_reason based on content
	stopReason := "end_turn"
	for _, block := range contentBlocks {
		if block.Type == "tool_use" {
			stopReason = "tool_use"
			break
		}
	}

	resp := ClaudeResponse{
		ID:         "msg-nexus-" + time.Now().Format("20060102150405"),
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		StopReason: stopReason,
		Content:    contentBlocks,
		Usage: ClaudeUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
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
			index := 0
			event.Index = &index
			event.Delta = delta
		}
	case "message_delta":
		event.Delta = &ClaudeDelta{StopReason: "end_turn"}
	}

	return json.Marshal(event)
}
