package gemini

import (
	"encoding/json"
	"strings"
)

var snakeToCamelTop = map[string]string{
	"system_instruction":         "systemInstruction",
	"generation_config":          "generationConfig",
	"tool_config":                "toolConfig",
	"safety_settings":            "safetySettings",
	"cached_content":             "cachedContent",
	"function_calling_config":    "functionCallingConfig",
	"allowed_function_names":     "allowedFunctionNames",
	"function_declarations":      "functionDeclarations",
	"parameters_json_schema":     "parametersJsonSchema",
	"inline_data":                "inlineData",
	"file_data":                  "fileData",
	"mime_type":                  "mimeType",
	"file_uri":                   "fileUri",
	"function_call":              "functionCall",
	"function_response":          "functionResponse",
	"thought_signature":          "thoughtSignature",
	"max_output_tokens":          "maxOutputTokens",
	"stop_sequences":             "stopSequences",
	"top_p":                      "topP",
	"top_k":                      "topK",
	"presence_penalty":           "presencePenalty",
	"frequency_penalty":          "frequencyPenalty",
	"response_mime_type":         "responseMimeType",
	"response_schema":            "responseSchema",
	"response_json_schema":       "responseJsonSchema",
	"thinking_config":            "thinkingConfig",
	"thinking_budget":            "thinkingBudget",
	"thinking_level":             "thinkingLevel",
	"include_thoughts":           "includeThoughts",
	"usage_metadata":             "usageMetadata",
	"finish_reason":              "finishReason",
	"response_id":                "responseId",
	"model_version":              "modelVersion",
	"prompt_token_count":         "promptTokenCount",
	"candidates_token_count":     "candidatesTokenCount",
	"thoughts_token_count":       "thoughtsTokenCount",
	"cached_content_token_count": "cachedContentTokenCount",
	"total_token_count":          "totalTokenCount",
}

// NormalizeKeysCamel recursively converts known Gemini snake_case payload keys to camelCase
// while leaving user-defined function parameter schemas and tool argument objects untouched.
func NormalizeKeysCamel(raw []byte) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	norm := normalizeVal(v, "")
	out, err := json.Marshal(norm)
	if err != nil {
		return raw
	}
	return out
}

func normalizeVal(v any, parentKey string) any {
	// Do not mutate user-provided JSON schemas or tool argument payloads.
	switch parentKey {
	case "parameters", "parametersJsonSchema", "responseSchema", "responseJsonSchema", "args", "response":
		return v
	}

	switch typed := v.(type) {
	case map[string]any:
		res := make(map[string]any, len(typed))
		for k, val := range typed {
			nk := k
			if mapped, ok := snakeToCamelTop[k]; ok {
				nk = mapped
			}
			res[nk] = normalizeVal(val, nk)
		}
		return res
	case []any:
		res := make([]any, len(typed))
		for i, item := range typed {
			res[i] = normalizeVal(item, parentKey)
		}
		return res
	default:
		return v
	}
}

var unsupportedSchemaKeys = map[string]bool{
	"$schema":              true,
	"$id":                  true,
	"$anchor":              true,
	"$comment":             true,
	"additionalProperties": true,
	"propertyNames":        true,
	"patternProperties":    true,
	"dependencies":         true,
	"dependentSchemas":     true,
	"dependentRequired":    true,
	"exclusiveMinimum":     true,
	"exclusiveMaximum":     true,
	"multipleOf":           true,
	"const":                true,
	"default":              true,
	"examples":             true,
	"readOnly":             true,
	"writeOnly":            true,
	"deprecated":           true,
}

// CleanSchemaForGemini strips JSON Schema keywords rejected by Gemini's v1beta Schema validator
// and flattens nullable union types such as ["string", "null"].
func CleanSchemaForGemini(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	defs := collectSchemaDefs(root)
	cleaned := cleanSchemaNode(root, defs, 0)
	if m, ok := cleaned.(map[string]any); ok {
		if t, _ := m["type"].(string); strings.EqualFold(t, "object") {
			if _, hasProps := m["properties"]; !hasProps {
				m["properties"] = map[string]any{}
			}
		}
	}
	out, err := json.Marshal(cleaned)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return out
}

func collectSchemaDefs(root any) map[string]any {
	defs := make(map[string]any)
	m, ok := root.(map[string]any)
	if !ok {
		return defs
	}
	for _, key := range []string{"$defs", "definitions"} {
		if sub, ok := m[key].(map[string]any); ok {
			for k, v := range sub {
				defs[k] = v
			}
		}
	}
	return defs
}

func cleanSchemaNode(node any, defs map[string]any, depth int) any {
	if depth > 16 {
		return map[string]any{"type": "string"}
	}
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}

	// Inline local $ref if present.
	if ref, ok := m["$ref"].(string); ok && ref != "" {
		if idx := strings.LastIndexByte(ref, '/'); idx >= 0 {
			name := ref[idx+1:]
			if target, found := defs[name]; found {
				return cleanSchemaNode(target, defs, depth+1)
			}
		}
		return map[string]any{"type": "string"}
	}

	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == "$defs" || k == "definitions" || unsupportedSchemaKeys[k] {
			continue
		}
		switch k {
		case "type":
			switch t := v.(type) {
			case string:
				out["type"] = strings.ToLower(t)
			case []any:
				var chosen string
				nullable := false
				for _, item := range t {
					s, _ := item.(string)
					if strings.EqualFold(s, "null") {
						nullable = true
					} else if chosen == "" && s != "" {
						chosen = strings.ToLower(s)
					}
				}
				if chosen == "" {
					chosen = "string"
				}
				out["type"] = chosen
				if nullable {
					out["nullable"] = true
				}
			}
		case "anyOf", "oneOf":
			if arr, ok := v.([]any); ok {
				var nonNull []any
				nullable := false
				for _, item := range arr {
					if im, ok := item.(map[string]any); ok && im["type"] == "null" {
						nullable = true
						continue
					}
					nonNull = append(nonNull, item)
				}
				if len(nonNull) == 1 {
					merged := cleanSchemaNode(nonNull[0], defs, depth+1)
					if mm, ok := merged.(map[string]any); ok {
						for mk, mv := range mm {
							out[mk] = mv
						}
						if nullable {
							out["nullable"] = true
						}
					}
				} else if len(nonNull) > 1 {
					cleanedArr := make([]any, 0, len(nonNull))
					for _, item := range nonNull {
						cleanedArr = append(cleanedArr, cleanSchemaNode(item, defs, depth+1))
					}
					out["anyOf"] = cleanedArr
				}
			}
		case "properties":
			if props, ok := v.(map[string]any); ok {
				cleanProps := make(map[string]any, len(props))
				for pk, pv := range props {
					cleanProps[pk] = cleanSchemaNode(pv, defs, depth+1)
				}
				out["properties"] = cleanProps
			}
		case "items":
			if im, ok := v.(map[string]any); ok {
				out["items"] = cleanSchemaNode(im, defs, depth+1)
			} else {
				// items: true / tuple-style items have no Gemini form.
				out["items"] = map[string]any{"type": "string"}
			}
		case "allOf":
			if arr, ok := v.([]any); ok {
				for _, item := range arr {
					mm, _ := cleanSchemaNode(item, defs, depth+1).(map[string]any)
					mergeSchemaInto(out, mm)
				}
			}
		case "enum":
			if arr, ok := v.([]any); ok {
				vals := make([]any, 0, len(arr))
				for _, e := range arr {
					switch ev := e.(type) {
					case string:
						vals = append(vals, ev)
					case nil:
					default:
						b, _ := json.Marshal(ev)
						vals = append(vals, string(b))
					}
				}
				if len(vals) > 0 {
					out["enum"] = vals
				}
			}
		default:
			if geminiSchemaKeys[k] {
				out[k] = v
			}
		}
	}
	finishSchemaNode(out)
	return out
}

// geminiSchemaKeys are the OpenAPI-subset keywords Gemini's function schemas accept; everything
// else (not, if/then, x-*, patternProperties, ...) is a 400 there.
var geminiSchemaKeys = map[string]bool{
	"format": true, "title": true, "description": true, "nullable": true, "required": true,
	"minItems": true, "maxItems": true, "minimum": true, "maximum": true, "minLength": true,
	"maxLength": true, "pattern": true, "default": true, "propertyOrdering": true,
	"minProperties": true, "maxProperties": true, "example": true,
}

func mergeSchemaInto(dst, src map[string]any) {
	for k, v := range src {
		switch k {
		case "properties":
			props, _ := dst["properties"].(map[string]any)
			if props == nil {
				props = map[string]any{}
			}
			if sp, ok := v.(map[string]any); ok {
				for pk, pv := range sp {
					props[pk] = pv
				}
			}
			dst["properties"] = props
		case "required":
			have, _ := dst["required"].([]any)
			if sr, ok := v.([]any); ok {
				dst["required"] = append(have, sr...)
			}
		default:
			if _, exists := dst[k]; !exists {
				dst[k] = v
			}
		}
	}
}

// finishSchemaNode infers a missing type, gives arrays items, keeps formats Gemini accepts for the
// type, stringifies enum types, and drops required names that have no property.
func finishSchemaNode(out map[string]any) {
	if _, ok := out["type"]; !ok {
		switch {
		case out["properties"] != nil:
			out["type"] = "object"
		case out["items"] != nil:
			out["type"] = "array"
		case out["anyOf"] == nil:
			out["type"] = "string"
		}
	}
	if out["type"] == "array" && out["items"] == nil {
		out["items"] = map[string]any{"type": "string"}
	}
	if out["enum"] != nil {
		out["type"] = "string"
	}
	if f, ok := out["format"].(string); ok {
		allowed := map[string]bool{"enum": true, "date-time": true, "int32": true, "int64": true, "float": true, "double": true}
		if !allowed[f] {
			delete(out, "format")
		}
	}
	if req, ok := out["required"].([]any); ok {
		props, _ := out["properties"].(map[string]any)
		seen := map[string]bool{}
		kept := make([]any, 0, len(req))
		for _, r := range req {
			name, _ := r.(string)
			if name == "" || seen[name] || props == nil || props[name] == nil {
				continue
			}
			seen[name] = true
			kept = append(kept, name)
		}
		if len(kept) > 0 {
			out["required"] = kept
		} else {
			delete(out, "required")
		}
	}
}
