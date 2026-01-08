package mappers

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

// OpenAI Request/Response structures

const ThoughtSignatureSeparator = "__thought__"

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

// OpenAIToolCall represents a tool call in the response (assistant wants to call a function)
type OpenAIToolCall struct {
	Index    *int                `json:"index,omitempty"`
	ID       string              `json:"id,omitempty"`
	Type     string              `json:"type,omitempty"` // "function"
	Function *OpenAIFunctionCall `json:"function,omitempty"`
}

// OpenAIFunctionCall contains the function name and arguments
type OpenAIFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // JSON string of arguments
}

// OpenAIMessage represents a message in OpenAI format
// Supports: user, assistant, system, tool roles
type OpenAIMessage struct {
	Role       string           `json:"role,omitempty"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`   // For assistant messages with function calls
	ToolCallID string           `json:"tool_call_id,omitempty"` // For tool role messages (function result)
	Name       string           `json:"name,omitempty"`         // Function name for tool role messages
}

// UnmarshalJSON handles both string and array content formats
func (m *OpenAIMessage) UnmarshalJSON(data []byte) error {
	// Try full struct first to get all fields
	type Alias struct {
		Role       string           `json:"role"`
		Content    json.RawMessage  `json:"content"`
		ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
		ToolCallID string           `json:"tool_call_id,omitempty"`
		Name       string           `json:"name,omitempty"`
	}
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	m.Role = alias.Role
	m.ToolCalls = alias.ToolCalls
	m.ToolCallID = alias.ToolCallID
	m.Name = alias.Name

	// Handle content field - can be string, array, or null
	if len(alias.Content) == 0 || string(alias.Content) == "null" {
		m.Content = ""
		return nil
	}

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
	Project     string               `json:"project"`
	RequestID   string               `json:"requestId"`
	Model       string               `json:"model"`
	Request     GeminiRequestPayload `json:"request"`
	UserAgent   string               `json:"userAgent,omitempty"`   // Required: "antigravity"
	RequestType string               `json:"requestType,omitempty"` // Required: "agent" or "web_search"
}

type GeminiRequestPayload struct {
	Contents          []GeminiContent         `json:"contents"`
	SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []GeminiTool            `json:"tools,omitempty"`
	ToolConfig        *GeminiToolConfig       `json:"toolConfig,omitempty"`
}

// ThinkingConfig for Gemini 3 Pro models (thinking/reasoning models)
type ThinkingConfig struct {
	ThinkingLevel  string `json:"thinkingLevel,omitempty"`  // "minimal", "low", "medium", "high"
	ThinkingBudget *int   `json:"thinkingBudget,omitempty"` // Direct token budget (for older models)
}

type GeminiContent struct {
	Role  string       `json:"role,omitempty"` // Role is optional for systemInstruction
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`      // For model's tool call request
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`  // For user's tool result
	ThoughtSignature string                  `json:"thought_signature,omitempty"` // Required for Gemini 3 models (at part level)
}

// GeminiFunctionCall represents a function call from the model
type GeminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
	// Non-standard field to support Claude-on-Vertex via Cloud Code API
	// Cloud Code translation layer needs this ID to generate valid Anthropic tool_use blocks
	ID string `json:"id,omitempty"`
}

// GeminiFunctionResponse represents a function response from the user
type GeminiFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
	// Non-standard field to support Claude-on-Vertex via Cloud Code API
	// Cloud Code translation layer needs this ID to match tool_result with tool_use
	ID string `json:"id,omitempty"`
}

type GeminiGenerationConfig struct {
	Temperature     *float64        `json:"temperature,omitempty"`
	MaxOutputTokens *int            `json:"maxOutputTokens,omitempty"`
	TopP            *float64        `json:"topP,omitempty"`
	StopSequences   []string        `json:"stopSequences,omitempty"`
	ThinkingConfig  *ThinkingConfig `json:"thinkingConfig,omitempty"` // For Gemini 3 Pro models
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

		// Handle tool role (function result)
		if msg.Role == "tool" {
			// Convert tool result to Gemini functionResponse
			// Parse content as JSON if possible, otherwise wrap in result key
			var responseData map[string]interface{}
			if err := json.Unmarshal([]byte(msg.Content), &responseData); err != nil {
				// Not JSON, wrap in result key
				responseData = map[string]interface{}{"result": msg.Content}
			}

			// Get function name from Name field or ToolCallID
			funcName := msg.Name
			toolCallID := msg.ToolCallID

			// Clean toolCallID if it contains a smuggled signature
			if strings.Contains(toolCallID, ThoughtSignatureSeparator) {
				parts := strings.SplitN(toolCallID, ThoughtSignatureSeparator, 2)
				toolCallID = parts[0]
			}

			if funcName == "" {
				funcName = toolCallID // Fallback to ToolCallID if Name not provided
			}

			contents = append(contents, GeminiContent{
				Role: "user", // Gemini expects function responses as user role
				Parts: []GeminiPart{
					{
						FunctionResponse: &GeminiFunctionResponse{
							Name:     funcName,
							Response: responseData,
							ID:       toolCallID, // Pass the ID to upstream for correlation
						},
					},
				},
			})
			continue
		}

		// Handle assistant role with tool_calls
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			var parts []GeminiPart
			// If there's also text content, include it
			if msg.Content != "" {
				parts = append(parts, GeminiPart{Text: msg.Content})
			}
			// Add function calls
			for _, tc := range msg.ToolCalls {
				// Parse arguments JSON string to map
				var args map[string]interface{}
				json.Unmarshal([]byte(tc.Function.Arguments), &args)

				// Extract thought_signature from tool_call_id
				var thoughtSignature string

				// ID Smuggling: try extracting from ID
				cleanID := tc.ID
				if strings.Contains(tc.ID, ThoughtSignatureSeparator) {
					parts := strings.SplitN(tc.ID, ThoughtSignatureSeparator, 2)
					cleanID = parts[0]
					thoughtSignature = parts[1]
				}

				parts = append(parts, GeminiPart{
					FunctionCall: &GeminiFunctionCall{
						Name: tc.Function.Name,
						Args: args,
						ID:   cleanID, // Pass the ID (without signature) to upstream
					},
					ThoughtSignature: thoughtSignature, // At part level, not inside functionCall
				})
			}
			contents = append(contents, GeminiContent{
				Role:  "model",
				Parts: parts,
			})
			continue
		}

		// Regular message handling
		role := msg.Role
		// Map OpenAI roles to Gemini roles
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

	// Antigravity identity injection (v3.3.17 feature)
	// Prepend identity prompt to systemInstruction for better API compatibility
	const antigravityIdentity = `You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.
You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.
**Absolute paths only**
**Proactiveness**`

	// Check if already contains Antigravity identity
	hasAntigravity := false
	for _, part := range systemParts {
		if strings.Contains(part.Text, "You are Antigravity") {
			hasAntigravity = true
			break
		}
	}

	// Prepend identity if not present
	if !hasAntigravity {
		systemParts = append([]GeminiPart{{Text: antigravityIdentity}}, systemParts...)
	}

	payload := GeminiRequestPayload{
		Contents: contents,
	}

	// Add system instruction (always present now due to identity injection)
	if len(systemParts) > 0 {
		payload.SystemInstruction = &GeminiContent{
			Parts: systemParts,
		}
	}

	geminiReq := GeminiRequest{
		Project:     projectID,
		RequestID:   "agent-" + time.Now().Format("20060102150405"), // Must use agent- prefix like Antigravity
		Model:       model,
		Request:     payload,
		UserAgent:   "antigravity",
		RequestType: "agent", // "agent" for normal, "web_search" when using googleSearch
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

	// Add thinkingConfig for Gemini 3 Pro models (inside GenerationConfig per official API)
	// Example: generationConfig: { thinkingConfig: { thinkingLevel: "low" } }
	if thinkingLevel := getThinkingLevelForModel(resolvedModel); thinkingLevel != "" {
		if geminiReq.Request.GenerationConfig == nil {
			geminiReq.Request.GenerationConfig = &GeminiGenerationConfig{}
		}
		geminiReq.Request.GenerationConfig.ThinkingConfig = &ThinkingConfig{
			ThinkingLevel: thinkingLevel,
		}

		// IMPORTANT: Gemini 3 Pro thinking models use tokenBudget from maxOutputTokens
		// If maxOutputTokens is too small, all tokens go to thinking with no output
		// Auto-scale: ensure minimum 8000 tokens for thinking models (4000 thinking + 4000 output)
		minTokensForThinking := 8000
		if geminiReq.Request.GenerationConfig.MaxOutputTokens == nil {
			geminiReq.Request.GenerationConfig.MaxOutputTokens = &minTokensForThinking
		} else if *geminiReq.Request.GenerationConfig.MaxOutputTokens < minTokensForThinking {
			geminiReq.Request.GenerationConfig.MaxOutputTokens = &minTokensForThinking
		}
	}

	// Convert tools to Gemini format
	if len(req.Tools) > 0 {
		tools := ConvertToolsToGemini(req.Tools, resolvedModel)
		if len(tools) > 0 {
			geminiReq.Request.Tools = tools
		}
	}

	return geminiReq
}

// getThinkingLevelForModel returns the appropriate thinkingLevel for Gemini 3 Pro models
// Based on official API docs: low, medium, high, minimal (for Flash)
func getThinkingLevelForModel(model string) string {
	if strings.Contains(model, "gemini-3-pro-low") {
		return "low"
	}
	if strings.Contains(model, "gemini-3-pro-high") {
		return "high"
	}
	if strings.Contains(model, "gemini-3-pro-medium") {
		return "medium"
	}
	// gemini-3-pro without suffix defaults to "low"
	if strings.Contains(model, "gemini-3-pro") && !strings.Contains(model, "image") {
		return "low"
	}
	return ""
}

// ConvertToolsToGemini converts OpenAI tools to Gemini format
// Supports: "function", "web_search", "web_search_preview"
// Note: googleSearch is skipped for gemini-3 models in Cloud Code API
// because thinking mode (default for gemini-3) conflicts with grounding
func ConvertToolsToGemini(tools []Tool, targetModel string) []GeminiTool {
	var geminiTools []GeminiTool
	var functionDeclarations []GeminiFunctionDeclaration
	hasGoogleSearch := false

	// Check if this is a gemini-3 model (thinking mode conflicts with grounding in Cloud Code API)
	isGemini3 := strings.Contains(targetModel, "gemini-3")

	for _, tool := range tools {
		switch tool.Type {
		case "web_search", "web_search_preview":
			// Map to Gemini's googleSearch
			hasGoogleSearch = true

		case "function":
			// Map function definition to Gemini format
			if tool.Function != nil {
				// Special case: google_search function should use Gemini's built-in GoogleSearch grounding
				if tool.Function.Name == "google_search" || tool.Function.Name == "googleSearch" {
					hasGoogleSearch = true
					continue
				}

				params := ConvertJSONSchemaToOpenAPI(tool.Function.Parameters)
				// Strict root-level filtering for Claude/Vertex: only allow compliant keys
				if params != nil {
					allowedKeys := map[string]bool{
						"type":                 true,
						"properties":           true,
						"required":             true,
						"additionalProperties": true,
						"$defs":                true,
						"strict":               true,
					}
					for k := range params {
						if !allowedKeys[k] {
							delete(params, k)
						}
					}
				}

				funcDecl := GeminiFunctionDeclaration{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					Parameters:  params,
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
	// Note: Skip for gemini-3 models - thinking mode conflicts with grounding in Cloud Code API
	if hasGoogleSearch && !isGemini3 {
		geminiTools = append(geminiTools, GeminiTool{
			GoogleSearch: &struct{}{},
		})
	} else if hasGoogleSearch && isGemini3 {
		log.Printf("⚠️ Skipping googleSearch for %s - thinking mode conflicts with grounding in Cloud Code API", targetModel)
	}

	return geminiTools
}

// ConvertJSONSchemaToOpenAPI converts OpenAI JSON Schema to Gemini/Claude OpenAPI schema
// Removes unsupported fields like additionalProperties, strict, nullable, description at root
// ConvertJSONSchemaToOpenAPI converts OpenAI JSON Schema to Gemini/Claude OpenAPI schema
// Removes unsupported fields like additionalProperties, strict, nullable, description at root
// Flattens 'anyOf' constructs containing enums into a single 'enum' array for Claude/Vertex parsing
func ConvertJSONSchemaToOpenAPI(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	result := make(map[string]interface{})

	// Check for 'anyOf' that can be flattened to 'enum'
	// Claude on Vertex dislikes complex nested schemas.
	// Pattern: "anyOf": [{"enum": ["a"]}, {"enum": ["b"]}] -> "enum": ["a", "b"]
	if anyOf, ok := schema["anyOf"].([]interface{}); ok {
		var flattenedEnums []interface{}
		canFlatten := true
		for _, item := range anyOf {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				canFlatten = false
				break
			}
			// Extract enum if present
			if enums, hasEnum := itemMap["enum"].([]interface{}); hasEnum {
				flattenedEnums = append(flattenedEnums, enums...)
			} else {
				// If any item is not a simple enum wrapper, abort flattening
				canFlatten = false
				break
			}
		}

		if canFlatten && len(flattenedEnums) > 0 {
			result["enum"] = flattenedEnums
			// Inherit type from the first item if avail, usually "string" for enums
			if firstItem, ok := anyOf[0].(map[string]interface{}); ok {
				if t, ok := firstItem["type"]; ok {
					result["type"] = t
				}
			}
			// Skip copying "anyOf" to result since we replaced it with "enum"
			// Also skip "description" processing below for this level as we handled the core structure
			// However, we still need to process other fields if they exist in schema (like description at root)
		} else {
			// Cannot flatten, process recursively
			newAnyOf := make([]interface{}, len(anyOf))
			for i, item := range anyOf {
				if itemMap, ok := item.(map[string]interface{}); ok {
					newAnyOf[i] = ConvertJSONSchemaToOpenAPI(itemMap)
				} else {
					newAnyOf[i] = item
				}
			}
			result["anyOf"] = newAnyOf
		}
	}

	for k, v := range schema {
		// Skip fields we already handled or want to remove
		if k == "anyOf" && result["enum"] != nil {
			continue // Already flattened
		}
		if k == "anyOf" {
			continue // Already processed in the else block above if not flattened
		}

		// Skip unsupported fields according to Gemini/Anthropic requirements
		// Note: description at root level of parameters is often redundant or causing 400s in Vertex/Claude
		if k == "additionalProperties" || k == "strict" || k == "$schema" || k == "nullable" || k == "example" || k == "examples" || k == "title" {
			continue
		}

		// Remove default values if they are null, as they are often invalid for non-nullable types in strict Draft 2020-12
		if k == "default" && v == nil {
			continue
		}

		// Recursively handle nested objects or arrays (exclude anyOf as it's handled above)
		if nested, ok := v.(map[string]interface{}); ok {
			result[k] = ConvertJSONSchemaToOpenAPI(nested)
		} else if arr, ok := v.([]interface{}); ok {
			// Also process arrays (e.g. allOf, items)
			newArr := make([]interface{}, len(arr))
			for i, item := range arr {
				if itemMap, ok := item.(map[string]interface{}); ok {
					newArr[i] = ConvertJSONSchemaToOpenAPI(itemMap)
				} else {
					newArr[i] = item
				}
			}
			result[k] = newArr
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
	role := ""
	var toolCalls []OpenAIToolCall
	toolCallCounter := 0

	if len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if r, ok := content["role"].(string); ok {
					if r == "model" {
						role = "assistant"
					} else {
						role = r
					}
				}
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					// Iterate through all parts to find text and functionCall content
					// Gemini 3 Pro models may provide thoughtSignature in one part and functionCall in another
					var textParts []string
					var currentThoughtSignature string

					// First pass: extract any thought signature for the whole candidate
					for _, p := range parts {
						if part, ok := p.(map[string]interface{}); ok {
							if sig, ok := part["thoughtSignature"].(string); ok && sig != "" {
								currentThoughtSignature = sig
							} else if sig, ok := part["thought_signature"].(string); ok && sig != "" {
								currentThoughtSignature = sig
							}
						}
					}

					for _, p := range parts {
						if part, ok := p.(map[string]interface{}); ok {
							// Extract text
							if t, ok := part["text"].(string); ok && t != "" {
								textParts = append(textParts, t)
							}
							// Extract functionCall
							if fc, ok := part["functionCall"].(map[string]interface{}); ok {
								name, _ := fc["name"].(string)
								args, _ := fc["args"].(map[string]interface{})
								argsJSON, _ := json.Marshal(args)

								toolCallCounter++
								currIdx := toolCallCounter - 1

								// ID Smuggling: Embed signature into ID if present
								toolCallID := "call_" + time.Now().Format("20060102150405") + "_" + string(rune('0'+toolCallCounter))
								if currentThoughtSignature != "" {
									toolCallID = toolCallID + ThoughtSignatureSeparator + currentThoughtSignature
								}

								toolCalls = append(toolCalls, OpenAIToolCall{
									Index: &currIdx,
									ID:    toolCallID,
									Type:  "function",
									Function: &OpenAIFunctionCall{
										Name:      name,
										Arguments: string(argsJSON),
									},
								})
							}
						}
					}
					if len(textParts) > 0 {
						text = strings.Join(textParts, "")
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
		// Extract finish_reason from the first candidate if available
		var fr *string
		if len(candidates) > 0 {
			if candidate, ok := candidates[0].(map[string]interface{}); ok {
				if frStr, ok := candidate["finishReason"].(string); ok && frStr != "" {
					normalized := strings.ToLower(frStr)
					if normalized == "stop" {
						fr = stringPtr("stop")
					} else {
						fr = stringPtr(normalized)
					}
				}
			}
		}

		// If tool calls were made, OpenAI uses "tool_calls" reason
		if len(toolCalls) > 0 {
			fr = stringPtr("tool_calls")
		}

		// Skip empty chunks that have nothing to offer
		if text == "" && len(toolCalls) == 0 && fr == nil && role == "" {
			return nil, nil
		}

		chunk := OpenAIStreamChunk{
			ID:      "chatcmpl-nexus",
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []OpenAIChoice{
				{
					Index: 0,
					Delta: &OpenAIMessage{
						Role:      role,
						Content:   text,
						ToolCalls: toolCalls,
					},
					FinishReason: fr,
				},
			},
		}
		return json.Marshal(chunk)
	}

	// Determine finish_reason based on whether model made tool calls
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
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
					Role:      "assistant",
					Content:   text,
					ToolCalls: toolCalls, // Will be nil/empty if no function calls
				},
				FinishReason: stringPtr(finishReason),
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

// ExtractGroundingMetadata extracts grounding metadata from Gemini response
// Returns the metadata if found, nil otherwise
func ExtractGroundingMetadata(geminiResp map[string]interface{}) *GeminiGroundingMetadata {
	// Get candidates
	var candidates []interface{}

	// Check nested format first
	if response, ok := geminiResp["response"].(map[string]interface{}); ok {
		if cand, ok := response["candidates"].([]interface{}); ok {
			candidates = cand
		}
	} else {
		if cand, ok := geminiResp["candidates"].([]interface{}); ok {
			candidates = cand
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Extract from first candidate
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return nil
	}

	groundingData, ok := candidate["groundingMetadata"].(map[string]interface{})
	if !ok {
		return nil
	}

	metadata := &GeminiGroundingMetadata{}

	// Extract groundingSupports
	if supports, ok := groundingData["groundingSupports"].([]interface{}); ok {
		for _, s := range supports {
			if supportMap, ok := s.(map[string]interface{}); ok {
				support := GeminiGroundingSupport{}

				// Extract segment
				if segment, ok := supportMap["segment"].(map[string]interface{}); ok {
					if startIdx, ok := segment["startIndex"].(float64); ok {
						support.Segment.StartIndex = int(startIdx)
					}
					if endIdx, ok := segment["endIndex"].(float64); ok {
						support.Segment.EndIndex = int(endIdx)
					}
				}

				// Extract groundingChunkIndices
				if indices, ok := supportMap["groundingChunkIndices"].([]interface{}); ok {
					for _, idx := range indices {
						if i, ok := idx.(float64); ok {
							support.GroundingChunkIndices = append(support.GroundingChunkIndices, int(i))
						}
					}
				}

				metadata.GroundingSupports = append(metadata.GroundingSupports, support)
			}
		}
	}

	// Extract groundingChunks
	if chunks, ok := groundingData["groundingChunks"].([]interface{}); ok {
		for _, c := range chunks {
			if chunkMap, ok := c.(map[string]interface{}); ok {
				chunk := GeminiGroundingChunk{}

				if webData, ok := chunkMap["web"].(map[string]interface{}); ok {
					chunk.Web = &struct {
						URI   string `json:"uri"`
						Title string `json:"title"`
					}{}
					if uri, ok := webData["uri"].(string); ok {
						chunk.Web.URI = uri
					}
					if title, ok := webData["title"].(string); ok {
						chunk.Web.Title = title
					}
				}

				metadata.GroundingChunks = append(metadata.GroundingChunks, chunk)
			}
		}
	}

	// Extract webSearchQueries
	if queries, ok := groundingData["webSearchQueries"].([]interface{}); ok {
		for _, q := range queries {
			if query, ok := q.(string); ok {
				metadata.WebSearchQueries = append(metadata.WebSearchQueries, query)
			}
		}
	}

	return metadata
}

// ConvertGroundingMetadataToAnnotations converts Gemini grounding metadata to OpenAI annotations
// Based on LiteLLM's _convert_grounding_metadata_to_annotations
func ConvertGroundingMetadataToAnnotations(metadata *GeminiGroundingMetadata) []OpenAIAnnotation {
	if metadata == nil {
		return nil
	}

	var annotations []OpenAIAnnotation

	// Build chunk index to URI map
	chunkToURI := make(map[int]string)
	chunkToTitle := make(map[int]string)

	for idx, chunk := range metadata.GroundingChunks {
		if chunk.Web != nil {
			chunkToURI[idx] = chunk.Web.URI
			chunkToTitle[idx] = chunk.Web.Title
		}
	}

	// Process each grounding support to create annotations
	for _, support := range metadata.GroundingSupports {
		if len(support.GroundingChunkIndices) == 0 {
			continue
		}

		startIndex := support.Segment.StartIndex
		endIndex := support.Segment.EndIndex

		// Use the first chunk's URL for the annotation
		firstChunkIdx := support.GroundingChunkIndices[0]
		if url, ok := chunkToURI[firstChunkIdx]; ok && url != "" {
			annotation := OpenAIAnnotation{
				Type: "url_citation",
				URLCitation: &OpenAIAnnotationURLCitation{
					StartIndex: startIndex,
					EndIndex:   endIndex,
					URL:        url,
					Title:      chunkToTitle[firstChunkIdx],
				},
			}
			annotations = append(annotations, annotation)
		}
	}

	return annotations
}
