package handlers

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCleanSchemaForGemini(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name: "removes $schema field",
			input: map[string]interface{}{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"type":    "object",
			},
			expected: map[string]interface{}{
				"type": "object",
			},
		},
		{
			name: "converts exclusiveMinimum to minimum",
			input: map[string]interface{}{
				"type":             "integer",
				"exclusiveMinimum": float64(0),
			},
			expected: map[string]interface{}{
				"type":    "integer",
				"minimum": float64(1),
			},
		},
		{
			name: "converts exclusiveMaximum to maximum",
			input: map[string]interface{}{
				"type":             "integer",
				"exclusiveMaximum": float64(100),
			},
			expected: map[string]interface{}{
				"type":    "integer",
				"maximum": float64(99),
			},
		},
		{
			name: "removes multiple unsupported fields",
			input: map[string]interface{}{
				"$schema":     "http://json-schema.org/draft-07/schema#",
				"$id":         "myschema",
				"$ref":        "#/definitions/foo",
				"$defs":       map[string]interface{}{},
				"definitions": map[string]interface{}{},
				"type":        "object",
			},
			expected: map[string]interface{}{
				"type": "object",
			},
		},
		{
			name: "recursively cleans nested properties",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"count": map[string]interface{}{
						"type":             "integer",
						"exclusiveMinimum": float64(0),
						"$schema":          "should-be-removed",
					},
				},
			},
			expected: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"count": map[string]interface{}{
						"type":    "integer",
						"minimum": float64(1),
					},
				},
			},
		},
		{
			name: "handles arrays in schema",
			input: map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"$schema": "should-be-removed",
					"type":    "string",
				},
			},
			expected: map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
		{
			name: "preserves required array",
			input: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"name", "age"},
			},
			expected: map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"name", "age"},
			},
		},
		{
			name: "removes allOf, anyOf, oneOf, not",
			input: map[string]interface{}{
				"allOf": []interface{}{},
				"anyOf": []interface{}{},
				"oneOf": []interface{}{},
				"not":   map[string]interface{}{},
				"type":  "object",
			},
			expected: map[string]interface{}{
				"type": "object",
			},
		},
		{
			name:     "handles nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "handles empty schema",
			input:    map[string]interface{}{},
			expected: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanSchemaForGemini(tt.input)
			if !reflect.DeepEqual(result, tt.expected) {
				resultJSON, _ := json.MarshalIndent(result, "", "  ")
				expectedJSON, _ := json.MarshalIndent(tt.expected, "", "  ")
				t.Errorf("cleanSchemaForGemini() =\n%s\nwant:\n%s", resultJSON, expectedJSON)
			}
		})
	}
}

func TestExtractTextFromGemini(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected string
	}{
		{
			name: "extracts text from valid response",
			input: map[string]interface{}{
				"candidates": []interface{}{
					map[string]interface{}{
						"content": map[string]interface{}{
							"parts": []interface{}{
								map[string]interface{}{
									"text": "Hello, world!",
								},
							},
						},
					},
				},
			},
			expected: "Hello, world!",
		},
		{
			name: "returns empty for no candidates",
			input: map[string]interface{}{
				"candidates": []interface{}{},
			},
			expected: "",
		},
		{
			name: "returns empty for no parts",
			input: map[string]interface{}{
				"candidates": []interface{}{
					map[string]interface{}{
						"content": map[string]interface{}{
							"parts": []interface{}{},
						},
					},
				},
			},
			expected: "",
		},
		{
			name: "returns empty for functionCall part",
			input: map[string]interface{}{
				"candidates": []interface{}{
					map[string]interface{}{
						"content": map[string]interface{}{
							"parts": []interface{}{
								map[string]interface{}{
									"functionCall": map[string]interface{}{
										"name": "get_weather",
										"args": map[string]interface{}{},
									},
								},
							},
						},
					},
				},
			},
			expected: "",
		},
		{
			name:     "handles nil input",
			input:    nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTextFromGemini(tt.input)
			if result != tt.expected {
				t.Errorf("extractTextFromGemini() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractFunctionCallFromGemini(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		hasCall  bool
		callName string
	}{
		{
			name: "extracts functionCall",
			input: map[string]interface{}{
				"candidates": []interface{}{
					map[string]interface{}{
						"content": map[string]interface{}{
							"parts": []interface{}{
								map[string]interface{}{
									"functionCall": map[string]interface{}{
										"name": "get_weather",
										"args": map[string]interface{}{
											"location": "Tokyo",
										},
									},
								},
							},
						},
					},
				},
			},
			hasCall:  true,
			callName: "get_weather",
		},
		{
			name: "returns nil for text-only response",
			input: map[string]interface{}{
				"candidates": []interface{}{
					map[string]interface{}{
						"content": map[string]interface{}{
							"parts": []interface{}{
								map[string]interface{}{
									"text": "Hello",
								},
							},
						},
					},
				},
			},
			hasCall: false,
		},
		{
			name: "returns nil for empty candidates",
			input: map[string]interface{}{
				"candidates": []interface{}{},
			},
			hasCall: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractFunctionCallFromGemini(tt.input)
			if tt.hasCall {
				if result == nil {
					t.Error("expected functionCall, got nil")
				} else if result["name"] != tt.callName {
					t.Errorf("expected name %q, got %q", tt.callName, result["name"])
				}
			} else {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
			}
		})
	}
}
