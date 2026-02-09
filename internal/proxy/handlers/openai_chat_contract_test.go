package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatContract_StreamAppendsDoneOnEOFWithoutDoneMarker(t *testing.T) {
	rec := httptest.NewRecorder()
	stream := strings.NewReader(
		"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]}}]}}\n\n",
	)

	chunkCount, doneSent, err := streamOpenAIChatChunks(rec, rec, stream, "gemini-3-flash", "req-contract")
	if err != nil {
		t.Fatalf("unexpected scanner error: %v", err)
	}
	if !doneSent {
		t.Fatal("expected doneSent=true")
	}
	if chunkCount != 1 {
		t.Fatalf("expected 1 chunk, got %d", chunkCount)
	}

	body := rec.Body.String()
	if strings.Count(body, "data: [DONE]") != 1 {
		t.Fatalf("expected exactly one [DONE], got body=%s", body)
	}
	if !strings.Contains(body, "\"object\":\"chat.completion.chunk\"") {
		t.Fatalf("expected chat completion chunk in body, got %s", body)
	}
}

func TestOpenAIChatContract_StreamDoesNotDuplicateDoneWhenUpstreamIncludesDone(t *testing.T) {
	rec := httptest.NewRecorder()
	stream := strings.NewReader(
		"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]}}]}}\n\n" +
			"data: [DONE]\n\n",
	)

	chunkCount, doneSent, err := streamOpenAIChatChunks(rec, rec, stream, "gemini-3-flash", "req-contract")
	if err != nil {
		t.Fatalf("unexpected scanner error: %v", err)
	}
	if !doneSent {
		t.Fatal("expected doneSent=true")
	}
	if chunkCount != 1 {
		t.Fatalf("expected 1 chunk, got %d", chunkCount)
	}
	if strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
		t.Fatalf("expected one [DONE], got body=%s", rec.Body.String())
	}
}

func TestOpenAIChatContract_NonStreamErrorMappingUsesOpenAIEnvelope(t *testing.T) {
	upstreamBody := []byte(`{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`)
	mapped := buildOpenAIUpstreamErrorBody(404, upstreamBody)

	var parsed map[string]interface{}
	if err := json.Unmarshal(mapped, &parsed); err != nil {
		t.Fatalf("failed to parse mapped error: %v body=%s", err, string(mapped))
	}
	errObj, ok := parsed["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing error object: %v", parsed)
	}
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("expected invalid_request_error, got %v", errObj["type"])
	}
	if msg, ok := errObj["message"].(string); !ok || strings.TrimSpace(msg) == "" {
		t.Fatalf("missing error.message: %v", parsed)
	}
	if _, ok := errObj["code"]; !ok {
		t.Fatalf("missing error.code: %v", parsed)
	}
}

func TestOpenAIChatContract_StreamPreflightErrorMappingUsesOpenAIEnvelope(t *testing.T) {
	upstreamBody := []byte(`{"error":{"code":429,"message":"Rate limit exceeded.","status":"RESOURCE_EXHAUSTED"}}`)
	mapped := buildOpenAIUpstreamErrorBody(429, upstreamBody)

	var parsed map[string]interface{}
	if err := json.Unmarshal(mapped, &parsed); err != nil {
		t.Fatalf("failed to parse mapped error: %v body=%s", err, string(mapped))
	}
	errObj, ok := parsed["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing error object: %v", parsed)
	}
	if errObj["type"] != "rate_limit_error" {
		t.Fatalf("expected rate_limit_error, got %v", errObj["type"])
	}
}
