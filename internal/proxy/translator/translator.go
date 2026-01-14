// Package translator provides format conversion between different LLM API formats.
// It handles the translation between Gemini (Google GenAI) format and Claude (Anthropic) format
// for tool calling, messages, and responses.
package translator

import (
	"strings"
)

// NeedsClaudeFormat returns true if the model is a Claude model that requires
// special format handling for tool calling.
func NeedsClaudeFormat(model string) bool {
	lowerModel := strings.ToLower(model)
	return strings.Contains(lowerModel, "claude")
}

// IsGeminiModel returns true if the model is a Gemini model.
func IsGeminiModel(model string) bool {
	lowerModel := strings.ToLower(model)
	return strings.Contains(lowerModel, "gemini")
}
