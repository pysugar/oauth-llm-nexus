package handlers

import (
	"testing"
)

func TestConvertChatCompletionToResponses_EmptyInput(t *testing.T) {
	// Empty map should not panic and return valid response
	chatResp := map[string]interface{}{}
	resp := ConvertChatCompletionToResponses(chatResp)

	if resp.ID == "" {
		t.Error("Expected non-empty response ID")
	}
	if resp.Object != "response" {
		t.Errorf("Expected object 'response', got '%s'", resp.Object)
	}
	if resp.Model != "unknown" {
		t.Errorf("Expected model 'unknown' for missing model, got '%s'", resp.Model)
	}
}

func TestConvertChatCompletionToResponses_MissingChoices(t *testing.T) {
	chatResp := map[string]interface{}{
		"model":   "test-model",
		"created": float64(1234567890),
	}
	resp := ConvertChatCompletionToResponses(chatResp)

	if resp.Model != "test-model" {
		t.Errorf("Expected model 'test-model', got '%s'", resp.Model)
	}
	// Should have nil or empty output
	if len(resp.Output) != 0 {
		t.Errorf("Expected empty output for missing choices, got %d items", len(resp.Output))
	}
}

func TestConvertChatCompletionToResponses_MissingModel(t *testing.T) {
	chatResp := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello",
				},
			},
		},
	}
	resp := ConvertChatCompletionToResponses(chatResp)

	// Should fallback to "unknown"
	if resp.Model != "unknown" {
		t.Errorf("Expected model 'unknown', got '%s'", resp.Model)
	}
	if len(resp.Output) != 1 {
		t.Errorf("Expected 1 output item, got %d", len(resp.Output))
	}
}

func TestConvertChatCompletionToResponses_MissingContent(t *testing.T) {
	chatResp := map[string]interface{}{
		"model": "test-model",
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role": "assistant",
					// No content field
				},
			},
		},
	}
	resp := ConvertChatCompletionToResponses(chatResp)

	if len(resp.Output) != 1 {
		t.Fatalf("Expected 1 output item, got %d", len(resp.Output))
	}
	// Should have empty text instead of panic
	if len(resp.Output[0].Content) != 1 {
		t.Errorf("Expected 1 content block, got %d", len(resp.Output[0].Content))
	}
	if resp.Output[0].Content[0].Text != "" {
		t.Errorf("Expected empty text for missing content, got '%s'", resp.Output[0].Content[0].Text)
	}
}

func TestConvertChatCompletionToResponses_WithUsage(t *testing.T) {
	chatResp := map[string]interface{}{
		"model": "test-model",
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Test",
				},
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	}
	resp := ConvertChatCompletionToResponses(chatResp)

	if resp.Usage == nil {
		t.Fatal("Expected usage to be set")
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("Expected prompt_tokens 10, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("Expected completion_tokens 5, got %d", resp.Usage.CompletionTokens)
	}
}
