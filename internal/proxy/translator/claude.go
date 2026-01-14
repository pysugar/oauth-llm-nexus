// Package translator provides Claude-specific format conversion utilities.
// This file handles the conversion between Gemini's functionCall/functionResponse format
// and Claude's tool_use/tool_result format.
package translator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
)

// generateToolUseID generates a unique tool use ID for Claude format.
// Format: "toolu_{random_hex}" to match Claude's expected format.
func generateToolUseID() string {
	bytes := make([]byte, 12)
	rand.Read(bytes)
	return "toolu_" + hex.EncodeToString(bytes)
}

// EnsureFunctionCallIDs ensures all functionCall parts have an id field.
// Claude requires each tool_use to have a unique id that is later referenced
// by tool_result via tool_use_id. This function adds missing ids to functionCall parts.
//
// Parameters:
//   - request: The Gemini request object (the inner "request" field of the payload)
func EnsureFunctionCallIDs(request map[string]interface{}) {
	contents, ok := request["contents"].([]interface{})
	if !ok {
		return
	}

	for _, content := range contents {
		contentMap, ok := content.(map[string]interface{})
		if !ok {
			continue
		}

		parts, ok := contentMap["parts"].([]interface{})
		if !ok {
			continue
		}

		for _, part := range parts {
			partMap, ok := part.(map[string]interface{})
			if !ok {
				continue
			}

			// Check if this part has a functionCall
			if functionCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
				// Ensure it has an id field
				if _, hasID := functionCall["id"]; !hasID {
					name, _ := functionCall["name"].(string)
					functionCall["id"] = fmt.Sprintf("%s-%s", name, generateToolUseID())
					log.Printf("🔧 [translator] Added id to functionCall: %s", functionCall["id"])
				}
			}
		}
	}
}

// MapFunctionResponseToToolResult maps functionResponse parts to include proper tool_use_id.
// Claude's tool_result requires a tool_use_id field that references the original tool_use id.
// In Gemini format, functionResponse uses "name" to identify the function, but Claude needs
// the actual tool_use_id from the corresponding functionCall.
//
// This function scans the contents to build a map of function names to their tool_use_ids,
// then updates any functionResponse parts to use the correct format.
//
// Parameters:
//   - request: The Gemini request object (the inner "request" field of the payload)
func MapFunctionResponseToToolResult(request map[string]interface{}) {
	contents, ok := request["contents"].([]interface{})
	if !ok {
		return
	}

	// First pass: collect functionCall name -> id mappings
	funcNameToID := make(map[string]string)
	for _, content := range contents {
		contentMap, ok := content.(map[string]interface{})
		if !ok {
			continue
		}

		parts, ok := contentMap["parts"].([]interface{})
		if !ok {
			continue
		}

		for _, part := range parts {
			partMap, ok := part.(map[string]interface{})
			if !ok {
				continue
			}

			if functionCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
				name, _ := functionCall["name"].(string)
				id, _ := functionCall["id"].(string)
				if name != "" && id != "" {
					funcNameToID[name] = id
					// Also store the full id as a key (in case name is used as id)
					funcNameToID[id] = id
				}
			}
		}
	}

	// Second pass: update functionResponse parts
	for _, content := range contents {
		contentMap, ok := content.(map[string]interface{})
		if !ok {
			continue
		}

		parts, ok := contentMap["parts"].([]interface{})
		if !ok {
			continue
		}

		for _, part := range parts {
			partMap, ok := part.(map[string]interface{})
			if !ok {
				continue
			}

			if functionResponse, ok := partMap["functionResponse"].(map[string]interface{}); ok {
				name, _ := functionResponse["name"].(string)

				// Try to find the corresponding tool_use_id
				var toolUseID string
				if id, found := funcNameToID[name]; found {
					toolUseID = id
				} else {
					// If no matching functionCall found, try to extract from name
					// (some clients use the id as the name)
					parts := strings.SplitN(name, "-", 2)
					if len(parts) > 1 && strings.HasPrefix(parts[1], "toolu_") {
						toolUseID = name
					} else {
						// Generate a new id as fallback
						toolUseID = fmt.Sprintf("%s-%s", name, generateToolUseID())
						log.Printf("⚠️ [translator] No matching functionCall found for %s, generated new id: %s", name, toolUseID)
					}
				}

				// Add the id field to functionResponse (Claude will use this internally)
				functionResponse["id"] = toolUseID
				log.Printf("🔧 [translator] Mapped functionResponse %s -> id: %s", name, toolUseID)
			}
		}
	}
}

// PrepareRequestForClaude prepares a Gemini format request for sending to a Claude model.
// This is the main entry point for request translation.
//
// Parameters:
//   - payload: The full request payload (including project, model, request, etc.)
func PrepareRequestForClaude(payload map[string]interface{}) {
	request, ok := payload["request"].(map[string]interface{})
	if !ok {
		return
	}

	// 1. Ensure all functionCall parts have IDs
	EnsureFunctionCallIDs(request)

	// 2. Map functionResponse parts to include proper tool_use_id
	MapFunctionResponseToToolResult(request)

	// 3. Convert Gemini tools format (googleSearch) to Claude format (web_search_20250305)
	ConvertToolsForClaude(request)
}

// ConvertToolsForClaude converts Gemini tools format for Claude models.
// IMPORTANT: Cloud Code API uses Gemini format internally. When targeting Claude models,
// googleSearch is NOT supported because Cloud Code API doesn't translate it for Claude.
// This function removes googleSearch tools for Claude models and logs a warning.
//
// Parameters:
//   - request: The Gemini request object (the inner "request" field of the payload)
func ConvertToolsForClaude(request map[string]interface{}) {
	tools, ok := request["tools"].([]interface{})
	if !ok {
		return
	}

	var filteredTools []interface{}
	hasRemoval := false

	for _, tool := range tools {
		// Handle string format tools (e.g., ["googleSearch"])
		if toolStr, ok := tool.(string); ok {
			if toolStr == "googleSearch" || toolStr == "google_search" {
				log.Printf("⚠️ [translator] Removing googleSearch tool for Claude - not supported via Cloud Code API")
				hasRemoval = true
				continue
			}
			// Keep other string tools
			filteredTools = append(filteredTools, tool)
			continue
		}

		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			filteredTools = append(filteredTools, tool)
			continue
		}

		// Check for googleSearch tool (Gemini format) - remove for Claude
		if _, hasGoogleSearch := toolMap["googleSearch"]; hasGoogleSearch {
			log.Printf("⚠️ [translator] Removing googleSearch tool for Claude - not supported via Cloud Code API")
			hasRemoval = true
			continue
		}

		// Check for googleSearchRetrieval tool - remove for Claude
		if _, hasGoogleSearchRetrieval := toolMap["googleSearchRetrieval"]; hasGoogleSearchRetrieval {
			log.Printf("⚠️ [translator] Removing googleSearchRetrieval tool for Claude - not supported via Cloud Code API")
			hasRemoval = true
			continue
		}

		// Keep other tools as-is (functionDeclarations, etc.)
		filteredTools = append(filteredTools, toolMap)
	}

	if hasRemoval {
		if len(filteredTools) == 0 {
			// Remove empty tools array
			delete(request, "tools")
			log.Printf("🔧 [translator] Removed empty tools array after filtering")
		} else {
			request["tools"] = filteredTools
		}
	}
}
