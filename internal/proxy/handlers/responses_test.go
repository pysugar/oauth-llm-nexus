package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestParseResponsesContent_WithImageAndFile(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"input_text","text":"hello"},
		{"type":"input_image","image_url":{"url":"https://example.com/a.png"}},
		{"type":"input_file","file_id":"file_123"}
	]`)

	got := parseResponsesContent(raw)
	if !strings.Contains(got, "hello") {
		t.Fatalf("expected text content, got %q", got)
	}
	if !strings.Contains(got, "[input_image] https://example.com/a.png") {
		t.Fatalf("expected image placeholder, got %q", got)
	}
	if !strings.Contains(got, "[input_file] file_123") {
		t.Fatalf("expected file placeholder, got %q", got)
	}
}

func TestExtractResponsesUsageFromGemini(t *testing.T) {
	geminiResp := map[string]interface{}{
		"usageMetadata": map[string]interface{}{
			"promptTokenCount":     float64(12),
			"candidatesTokenCount": float64(8),
		},
	}

	usage := extractResponsesUsageFromGemini(geminiResp)
	if usage == nil {
		t.Fatal("expected usage to be extracted")
	}
	if usage.PromptTokens != 12 || usage.CompletionTokens != 8 || usage.TotalTokens != 20 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestApplyResponsesUpstreamFields_EncodesCompatFieldsIntoRequestID(t *testing.T) {
	payload := map[string]interface{}{}
	req := OpenAIResponsesRequest{
		Conversation:       "conv-1",
		PreviousResponseID: "resp-1",
	}

	compatCtx, encoded := applyResponsesUpstreamFields(payload, "req-1", req)
	if !encoded {
		t.Fatal("expected requestId to be encoded with compatibility context")
	}
	if payload["userAgent"] != "antigravity" {
		t.Fatalf("expected userAgent=antigravity, got %v", payload["userAgent"])
	}
	if payload["requestType"] != "agent" {
		t.Fatalf("expected requestType=agent, got %v", payload["requestType"])
	}
	requestID, _ := payload["requestId"].(string)
	if requestID == "" || requestID == "req-1" {
		t.Fatalf("expected encoded requestId, got %v", payload["requestId"])
	}
	decoded := decodeResponsesCompatRequestID(requestID)
	if decoded.Conversation != "conv-1" || decoded.PreviousResponseID != "resp-1" {
		t.Fatalf("unexpected decoded context: %+v", decoded)
	}
	if _, ok := payload["conversation"]; ok {
		t.Fatal("conversation should not be forwarded to Google upstream payload")
	}
	if _, ok := payload["previous_response_id"]; ok {
		t.Fatal("previous_response_id should not be forwarded to Google upstream payload")
	}
	if compatCtx.Conversation != "conv-1" || compatCtx.PreviousResponseID != "resp-1" {
		t.Fatalf("unexpected compat context: %+v", compatCtx)
	}
}

func TestApplyResponsesCompatToResponse_RestoresFields(t *testing.T) {
	resp := &OpenAIResponsesResponse{
		ID:     "resp_test",
		Object: "response",
		Status: "completed",
	}
	compatCtx := responsesCompatContext{
		Conversation:       "conv-restored",
		PreviousResponseID: "resp-prev",
	}

	applyResponsesCompatToResponse(resp, compatCtx)

	if resp.PreviousResponseID != "resp-prev" {
		t.Fatalf("expected previous_response_id restored, got %q", resp.PreviousResponseID)
	}
	if resp.Metadata == nil || resp.Metadata["conversation"] != "conv-restored" {
		t.Fatalf("expected metadata.conversation restored, got %#v", resp.Metadata)
	}
}

func TestApplyResponsesCompatToMap_RestoresFields(t *testing.T) {
	respMap := map[string]interface{}{
		"id": "resp_test",
	}
	compatCtx := responsesCompatContext{
		Conversation:       "conv-map",
		PreviousResponseID: "resp-prev-map",
	}

	applyResponsesCompatToMap(respMap, compatCtx)

	if respMap["previous_response_id"] != "resp-prev-map" {
		t.Fatalf("expected previous_response_id in map, got %#v", respMap["previous_response_id"])
	}
	meta, _ := respMap["metadata"].(map[string]interface{})
	if meta == nil || meta["conversation"] != "conv-map" {
		t.Fatalf("expected metadata.conversation in map, got %#v", respMap["metadata"])
	}
}

func TestWriteResponsesUpstreamError_NormalizesEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	upstreamBody := []byte(`{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`)

	writeResponsesUpstreamError(rec, http.StatusNotFound, upstreamBody)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected application/json content type, got %q", ct)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body, got %v (%s)", err, rec.Body.String())
	}
	errObj, _ := payload["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatalf("expected error object, got %#v", payload)
	}
	if msg, _ := errObj["message"].(string); strings.TrimSpace(msg) == "" {
		t.Fatalf("expected non-empty error.message, got %#v", errObj["message"])
	}
	if typ, _ := errObj["type"].(string); typ != "invalid_request_error" {
		t.Fatalf("expected invalid_request_error type, got %#v", errObj["type"])
	}
	if _, ok := errObj["code"]; !ok {
		t.Fatalf("expected error.code, got %#v", errObj)
	}
}

func TestWriteResponsesUpstreamError_UsesStatusFallbackWhenBodyUnreadable(t *testing.T) {
	rec := httptest.NewRecorder()

	writeResponsesUpstreamError(rec, http.StatusTooManyRequests, []byte("not-json"))

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body, got %v (%s)", err, rec.Body.String())
	}
	errObj, _ := payload["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatalf("expected error object, got %#v", payload)
	}
	if typ, _ := errObj["type"].(string); typ != "rate_limit_error" {
		t.Fatalf("expected rate_limit_error, got %#v", errObj["type"])
	}
}
