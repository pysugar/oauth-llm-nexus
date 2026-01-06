package mappers

import (
	"encoding/json"
	"strings"
	"time"
)

// OpenAI Request/Response structures

type OpenAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	Tools       []Tool          `json:"tools,omitempty"`
	ToolChoice  interface{}     `json:"tool_choice,omitempty"` // Can be string ("auto", "none", "required") or object
}

// Tool represents an OpenAI-compatible tool definition
// Supports: "function", "web_search", "web_search_preview"
type Tool struct {
	Type              string              `json:"type"` // "function", "web_search", "web_search_preview"
	Function          *FunctionDefinition `json:"function,omitempty"`
	SearchContextSize string              `json:"search_context_size,omitempty"` // "low", "medium", "high" for web_search
	UserLocation      *UserLocation       `json:"user_location,omitempty"`       // Location info for web_search
}

type FunctionDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"` // JSON Schema
}

// UserLocation for web_search tool localization
type UserLocation struct {
	Type        string               `json:"type"` // "approximate"
	Approximate *ApproximateLocation `json:"approximate,omitempty"`
}

type ApproximateLocation struct {
	Country  string `json:"country,omitempty"`
	City     string `json:"city,omitempty"`
	Region   string `json:"region,omitempty"`
	Timezone string `json:"timezone,omitempty"`
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
	Index        int            `json:"index"`
	Message      OpenAIMessage  `json:"message,omitempty"`
	Delta        *OpenAIMessage `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason,omitempty"`
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
	Project   string               `json:"project"`
	RequestID string               `json:"requestId"`
	Model     string               `json:"model"`
	Request   GeminiRequestPayload `json:"request"`
}

type GeminiRequestPayload struct {
	Contents          []GeminiContent         `json:"contents"`
	SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []GeminiTool            `json:"tools,omitempty"`
	ToolConfig        *GeminiToolConfig       `json:"toolConfig,omitempty"`
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

// Tools and Function Calling structures

type GeminiTool struct {
	FunctionDeclarations  []GeminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
	GoogleSearch          *struct{}                   `json:"googleSearch,omitempty"`
	GoogleSearchRetrieval *struct{}                   `json:"googleSearchRetrieval,omitempty"`
	CodeExecution         *struct{}                   `json:"codeExecution,omitempty"`
}

type GeminiFunctionDeclaration struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"` // OpenAPI Schema
}

type GeminiToolConfig struct {
	FunctionCallingConfig *GeminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type GeminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode"`                           // AUTO, ANY, NONE
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"` // For specific function selection
}

// Grounding metadata structures (from Gemini response)

type GeminiGroundingMetadata struct {
	GroundingSupports []GeminiGroundingSupport `json:"groundingSupports"`
	GroundingChunks   []GeminiGroundingChunk   `json:"groundingChunks"`
	WebSearchQueries  []string                 `json:"webSearchQueries,omitempty"`
}

type GeminiGroundingSupport struct {
	Segment struct {
		StartIndex int `json:"startIndex"`
		EndIndex   int `json:"endIndex"`
	} `json:"segment"`
	GroundingChunkIndices []int `json:"groundingChunkIndices"`
}

type GeminiGroundingChunk struct {
	Web *struct {
		URI   string `json:"uri"`
		Title string `json:"title"`
	} `json:"web,omitempty"`
}

// OpenAI Annotations structures (for converting Grounding to OpenAI format)

type OpenAIAnnotation struct {
	Type        string                       `json:"type"` // "url_citation"
	URLCitation *OpenAIAnnotationURLCitation `json:"url_citation,omitempty"`
}

type OpenAIAnnotationURLCitation struct {
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
	URL        string `json:"url"`
	Title      string `json:"title"`
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

	// Convert tools to Gemini format
	if len(req.Tools) > 0 {
		tools := ConvertToolsToGemini(req.Tools)
		if len(tools) > 0 {
			geminiReq.Request.Tools = tools
		}
	}

	return geminiReq
}

// ConvertToolsToGemini converts OpenAI tools to Gemini format
// Supports: "function", "web_search", "web_search_preview"
// Based on LiteLLM's _map_function implementation
func ConvertToolsToGemini(tools []Tool) []GeminiTool {
	var geminiTools []GeminiTool
	var functionDeclarations []GeminiFunctionDeclaration
	hasGoogleSearch := false

	for _, tool := range tools {
		switch tool.Type {
		case "web_search", "web_search_preview":
			// Map to Gemini's googleSearch
			hasGoogleSearch = true

		case "function":
			// Map function definition to Gemini format
			if tool.Function != nil {
				funcDecl := GeminiFunctionDeclaration{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					Parameters:  ConvertJSONSchemaToOpenAPI(tool.Function.Parameters),
				}
				functionDeclarations = append(functionDeclarations, funcDecl)
			}

		default:
			// Check if this is a special Gemini tool by name pattern
			// Support direct googleSearch, codeExecution, etc. for advanced users
			if tool.Type == "googleSearch" || tool.Type == "google_search" {
				hasGoogleSearch = true
			}
		}
	}

	// Add function declarations as a single tool (Gemini groups all functions together)
	if len(functionDeclarations) > 0 {
		geminiTools = append(geminiTools, GeminiTool{
			FunctionDeclarations: functionDeclarations,
		})
	}

	// Add Google Search as a separate tool
	if hasGoogleSearch {
		geminiTools = append(geminiTools, GeminiTool{
			GoogleSearch: &struct{}{},
		})
	}

	return geminiTools
}

// ConvertJSONSchemaToOpenAPI converts OpenAI JSON Schema to Gemini OpenAPI schema
// Removes unsupported fields like additionalProperties, strict
func ConvertJSONSchemaToOpenAPI(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	result := make(map[string]interface{})
	for k, v := range schema {
		// Skip unsupported fields
		if k == "additionalProperties" || k == "strict" || k == "$schema" {
			continue
		}
		// Recursively handle nested objects
		if nested, ok := v.(map[string]interface{}); ok {
			result[k] = ConvertJSONSchemaToOpenAPI(nested)
		} else {
			result[k] = v
		}
	}
	return result
}

// GeminiToOpenAI converts a Gemini response to OpenAI format
func GeminiToOpenAI(geminiResp map[string]interface{}, model string, isStreaming bool) ([]byte, error) {
	// Extract text from Gemini response
	// Handle both direct and nested response structures
	var candidates []interface{}

	// Check if response is nested (Cloud Code API format)
	if response, ok := geminiResp["response"].(map[string]interface{}); ok {
		if cand, ok := response["candidates"].([]interface{}); ok {
			candidates = cand
		}
	} else {
		// Direct format
		if cand, ok := geminiResp["candidates"].([]interface{}); ok {
			candidates = cand
		}
	}

	text := ""
	if len(candidates) > 0 {
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

	// Extract usage metadata from Gemini response
	var promptTokens, completionTokens, totalTokens int

	// Try nested format first (Cloud Code API)
	if response, ok := geminiResp["response"].(map[string]interface{}); ok {
		if usageData, ok := response["usageMetadata"].(map[string]interface{}); ok {
			if pt, ok := usageData["promptTokenCount"].(float64); ok {
				promptTokens = int(pt)
			}
			if ct, ok := usageData["candidatesTokenCount"].(float64); ok {
				completionTokens = int(ct)
			}
			if tt, ok := usageData["totalTokenCount"].(float64); ok {
				totalTokens = int(tt)
			}
		}
	} else {
		// Direct format
		if usageData, ok := geminiResp["usageMetadata"].(map[string]interface{}); ok {
			if pt, ok := usageData["promptTokenCount"].(float64); ok {
				promptTokens = int(pt)
			}
			if ct, ok := usageData["candidatesTokenCount"].(float64); ok {
				completionTokens = int(ct)
			}
			if tt, ok := usageData["totalTokenCount"].(float64); ok {
				totalTokens = int(tt)
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
		Usage: &OpenAIUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
		},
	}

	return json.Marshal(resp)
}

func stringPtr(s string) *string {
	return &s
}
