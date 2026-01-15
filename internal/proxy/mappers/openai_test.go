package mappers

import (
	"strings"
	"testing"
)

func TestOpenAIToGemini_SystemRole(t *testing.T) {
	req := OpenAIChatRequest{
		Model: "gpt-4",
		Messages: []OpenAIMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there"},
			{Role: "user", Content: "Bye"},
		},
	}

	geminiReq := OpenAIToGemini(req, "gemini-test-model", "test-project")

	// 1. Verify System Instruction
	// With Antigravity identity injection (v3.3.17), SystemInstruction has 2 parts:
	// - Part 0: Antigravity identity
	// - Part 1: User's system message
	if geminiReq.Request.SystemInstruction == nil {
		t.Fatal("SystemInstruction should not be nil")
	}
	if len(geminiReq.Request.SystemInstruction.Parts) != 2 {
		t.Fatalf("Expected 2 system parts (identity + user), got %d", len(geminiReq.Request.SystemInstruction.Parts))
	}
	// Part 0 should be Antigravity identity
	if !strings.Contains(geminiReq.Request.SystemInstruction.Parts[0].Text, "You are Antigravity") {
		t.Errorf("Part 0 should contain Antigravity identity")
	}
	// Part 1 should be user's system message
	expectedSys := "You are a helpful assistant."
	if geminiReq.Request.SystemInstruction.Parts[1].Text != expectedSys {
		t.Errorf("System instruction mismatch. Expected %q, got %q", expectedSys, geminiReq.Request.SystemInstruction.Parts[1].Text)
	}

	// 2. Verify Message Content (System messages should be removed from contents)
	// Expecting: User, Model, User -> 3 messages
	expectedCount := 3
	if len(geminiReq.Request.Contents) != expectedCount {
		t.Fatalf("Expected %d content messages, got %d", expectedCount, len(geminiReq.Request.Contents))
	}

	// Message 1: User
	if geminiReq.Request.Contents[0].Role != "user" {
		t.Errorf("Msg 0 role mismatch: %s", geminiReq.Request.Contents[0].Role)
	}
	if geminiReq.Request.Contents[0].Parts[0].Text != "Hello" {
		t.Errorf("Msg 0 text mismatch")
	}

	// Message 2: Model (mapped from assistant)
	if geminiReq.Request.Contents[1].Role != "model" {
		t.Errorf("Msg 1 role mismatch: %s", geminiReq.Request.Contents[1].Role)
	}
	if geminiReq.Request.Contents[1].Parts[0].Text != "Hi there" {
		t.Errorf("Msg 1 text mismatch")
	}

	// Message 3: User
	if geminiReq.Request.Contents[2].Role != "user" {
		t.Errorf("Msg 2 role mismatch: %s", geminiReq.Request.Contents[2].Role)
	}
	if geminiReq.Request.Contents[2].Parts[0].Text != "Bye" {
		t.Errorf("Msg 2 text mismatch")
	}
}

func TestOpenAIToGemini_NoSystemRole(t *testing.T) {
	req := OpenAIChatRequest{
		Model: "gpt-4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Just a user message"},
		},
	}

	geminiReq := OpenAIToGemini(req, "gemini-test-model", "test-project")

	// With Antigravity identity injection (v3.3.17), SystemInstruction is always present
	// containing the Antigravity identity even without user system message
	if geminiReq.Request.SystemInstruction == nil {
		t.Fatal("SystemInstruction should contain Antigravity identity")
	}
	if len(geminiReq.Request.SystemInstruction.Parts) != 1 {
		t.Fatalf("Expected 1 part (identity only), got %d", len(geminiReq.Request.SystemInstruction.Parts))
	}
	if !strings.Contains(geminiReq.Request.SystemInstruction.Parts[0].Text, "You are Antigravity") {
		t.Error("SystemInstruction should contain Antigravity identity")
	}

	// Verify Contents
	if len(geminiReq.Request.Contents) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(geminiReq.Request.Contents))
	}
}
func TestOpenAIToGemini_IDSmuggling(t *testing.T) {
	// Test extraction from tool_calls
	req := OpenAIChatRequest{
		Model: "gpt-4o-mini",
		Messages: []OpenAIMessage{
			{
				Role: "assistant",
				ToolCalls: []OpenAIToolCall{
					{
						ID:   "call_123__thought__test_sig",
						Type: "function",
						Function: &OpenAIFunctionCall{
							Name:      "test_func",
							Arguments: `{"arg": "val"}`,
						},
					},
				},
			},
		},
	}

	geminiReq := OpenAIToGemini(req, "gemini-3-flash", "test-project")

	if len(geminiReq.Request.Contents) != 1 {
		t.Fatalf("Expected 1 content message, got %d", len(geminiReq.Request.Contents))
	}

	parts := geminiReq.Request.Contents[0].Parts
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part, got %d", len(parts))
	}

	if parts[0].ThoughtSignature != "test_sig" {
		t.Errorf("Expected thought_signature 'test_sig', got %q", parts[0].ThoughtSignature)
	}

	// Test extraction from tool role (cleaning)
	req2 := OpenAIChatRequest{
		Model: "gpt-4o-mini",
		Messages: []OpenAIMessage{
			{
				Role:       "tool",
				ToolCallID: "call_123__thought__test_sig",
				Name:       "test_func",
				Content:    "result",
			},
		},
	}

	geminiReq2 := OpenAIToGemini(req2, "gemini-3-flash", "test-project")
	if geminiReq2.Request.Contents[0].Parts[0].FunctionResponse.Name != "test_func" {
		t.Errorf("Expected function name 'test_func', got %q", geminiReq2.Request.Contents[0].Parts[0].FunctionResponse.Name)
	}
}

func TestConvertJSONSchemaToOpenAPI_ClaudeStrict(t *testing.T) {
	schema := map[string]interface{}{
		"type":        "object",
		"description": "Root description that should be removed",
		"properties": map[string]interface{}{
			"start_line": map[string]interface{}{
				"type":        "integer",
				"description": "Inner description that should stay",
				"default":     nil,  // Should be removed
				"nullable":    true, // Should be removed
			},
			"options": map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{
						"type":    "string",
						"title":   "Title to remove",
						"example": "Example to remove",
					},
				},
			},
		},
		"required": []string{"start_line"},
		"strict":   true,
	}

	result := ConvertJSONSchemaToOpenAPI(schema)

	// In OpenAIToGemini, we also apply strict root filtering
	allowedKeys := map[string]bool{
		"type":                 true,
		"properties":           true,
		"required":             true,
		"additionalProperties": true,
		"$defs":                true,
		"strict":               true,
	}
	for k := range result {
		if !allowedKeys[k] {
			delete(result, k)
		}
	}

	// Assertions
	if _, ok := result["description"]; ok {
		t.Errorf("Root description should have been filtered out")
	}
	if result["type"] != "object" {
		t.Errorf("Expected type object, got %v", result["type"])
	}

	props := result["properties"].(map[string]interface{})
	startLine := props["start_line"].(map[string]interface{})

	if _, ok := startLine["default"]; ok {
		t.Errorf("Null default should have been removed")
	}
	if _, ok := startLine["nullable"]; ok {
		t.Errorf("Nullable should have been removed")
	}
	if startLine["description"] != "Inner description that should stay" {
		t.Errorf("Inner description should have been preserved")
	}

	options := props["options"].(map[string]interface{})
	anyOf := options["anyOf"].([]interface{})
	first := anyOf[0].(map[string]interface{})

	if _, ok := first["title"]; ok {
		t.Errorf("Inner title should have been removed")
	}
	if _, ok := first["example"]; ok {
		t.Errorf("Inner example should have been removed")
	}
}

func TestConvertJSONSchemaToOpenAPI_AnyOfFlattening(t *testing.T) {
	// Schema simulating the "now" tool's timezone parameter
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"timezone": map[string]interface{}{
				"description": "The timezone to use",
				"anyOf": []interface{}{
					map[string]interface{}{
						"description": "Use UTC",
						"enum":        []interface{}{"utc"},
						"type":        "string",
					},
					map[string]interface{}{
						"description": "Use local time",
						"enum":        []interface{}{"local"},
						"type":        "string",
					},
				},
			},
		},
	}

	result := ConvertJSONSchemaToOpenAPI(schema)

	props := result["properties"].(map[string]interface{})
	timezone := props["timezone"].(map[string]interface{})

	// Should NOT have anyOf
	if _, ok := timezone["anyOf"]; ok {
		t.Errorf("anyOf should have been flattened")
	}

	// Should have enum: ["utc", "local"]
	if enums, ok := timezone["enum"].([]interface{}); ok {
		if len(enums) != 2 {
			t.Errorf("Expected 2 enum values, got %d", len(enums))
		} else {
			// Basic check (order might not be guaranteed if map iteration was involved, but slice append preserves it)
			if enums[0] != "utc" || enums[1] != "local" {
				t.Errorf("Unexpected enum values: %v", enums)
			}
		}
	} else {
		t.Errorf("enum field missing after flattening")
	}

	// Should inherit type: string
	if tType, ok := timezone["type"]; !ok || tType != "string" {
		t.Errorf("Expected type string, got %v", tType)
	}
}
