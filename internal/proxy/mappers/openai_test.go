package mappers

import (
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
	if geminiReq.Request.SystemInstruction == nil {
		t.Fatal("SystemInstruction should not be nil")
	}
	if len(geminiReq.Request.SystemInstruction.Parts) != 1 {
		t.Fatalf("Expected 1 system part, got %d", len(geminiReq.Request.SystemInstruction.Parts))
	}
	expectedSys := "You are a helpful assistant."
	if geminiReq.Request.SystemInstruction.Parts[0].Text != expectedSys {
		t.Errorf("System instruction mismatch. Expected %q, got %q", expectedSys, geminiReq.Request.SystemInstruction.Parts[0].Text)
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

	// Verify System Instruction is nil
	if geminiReq.Request.SystemInstruction != nil {
		t.Fatal("SystemInstruction should be nil when no system message is present")
	}

	// Verify Contents
	if len(geminiReq.Request.Contents) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(geminiReq.Request.Contents))
	}
}
