package mappers

import (
	"strings"
	"testing"
)

func TestGenerateToolUseID(t *testing.T) {
	tests := []struct {
		funcName string
	}{
		{"get_weather"},
		{"search_files"},
		{"complex_function_name"},
		{"a"},
	}

	for _, tt := range tests {
		t.Run(tt.funcName, func(t *testing.T) {
			id := GenerateToolUseID(tt.funcName)

			// Verify format: {funcName}-{random8hex}
			if !strings.HasPrefix(id, tt.funcName+"-") {
				t.Errorf("GenerateToolUseID(%q) = %q, want prefix %q", tt.funcName, id, tt.funcName+"-")
			}

			// Verify suffix length (8 hex chars)
			parts := strings.Split(id, "-")
			suffix := parts[len(parts)-1]
			if len(suffix) != 8 {
				t.Errorf("GenerateToolUseID(%q) suffix length = %d, want 8", tt.funcName, len(suffix))
			}
		})
	}
}

func TestExtractFunctionName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"get_weather-a1b2c3d4", "get_weather"},
		{"search_files-12345678", "search_files"},
		{"complex-name-with-dashes-abcd1234", "complex-name-with-dashes"},
		{"func-name-87654321", "func-name"},
		// Legacy format fallback
		{"toolu_123", "toolu_123"},
		{"some_random_id", "some_random_id"},
		// Edge cases
		{"short-ab12", "short-ab12"},             // suffix too short
		{"name-12345678901", "name-12345678901"}, // suffix too long
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ExtractFunctionName(tt.input)
			if result != tt.expected {
				t.Errorf("ExtractFunctionName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateAndExtractRoundTrip(t *testing.T) {
	// Test that we can generate an ID and extract the function name from it
	funcNames := []string{"get_weather", "search", "complex_function_name", "a"}

	for _, name := range funcNames {
		t.Run(name, func(t *testing.T) {
			id := GenerateToolUseID(name)
			extracted := ExtractFunctionName(id)
			if extracted != name {
				t.Errorf("RoundTrip failed: GenerateToolUseID(%q) = %q, ExtractFunctionName() = %q", name, id, extracted)
			}
		})
	}
}
