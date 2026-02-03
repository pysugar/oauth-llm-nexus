package codex

// ChatCompletionToResponses converts OpenAI Chat Completions format to Codex Responses format
func ChatCompletionToResponses(chatReq map[string]interface{}) map[string]interface{} {
	responsesReq := make(map[string]interface{})

	// Copy model
	if model, ok := chatReq["model"].(string); ok {
		responsesReq["model"] = model
	}

	// Convert messages -> instructions + input
	if messages, ok := chatReq["messages"].([]interface{}); ok {
		var instructions string
		var input []interface{}

		for _, msg := range messages {
			m, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			content := m["content"]

			if role == "system" {
				// system message -> instructions
				if text, ok := content.(string); ok {
					if instructions != "" {
						instructions += "\n"
					}
					instructions += text
				}
			} else {
				// user/assistant -> input
				// Codex requires: type=message, and content type based on role
				// user -> input_text, assistant -> output_text
				contentType := "input_text"
				if role == "assistant" {
					contentType = "output_text"
				}
				input = append(input, map[string]interface{}{
					"type":    "message",
					"role":    role,
					"content": convertContentWithType(content, contentType),
				})
			}
		}

		responsesReq["instructions"] = instructions
		responsesReq["input"] = input
	}

	// Copy compatible parameters only
	// Note: Codex API does NOT support: temperature, top_p
	if maxTokens, ok := chatReq["max_tokens"]; ok {
		responsesReq["max_output_tokens"] = maxTokens
	}
	if maxTokens, ok := chatReq["max_completion_tokens"]; ok {
		responsesReq["max_output_tokens"] = maxTokens
	}

	// Force streaming and store settings
	responsesReq["stream"] = true
	responsesReq["store"] = false

	return responsesReq
}

// convertContentWithType converts message content to Responses API format
// contentType should be "input_text" for user messages, "output_text" for assistant
func convertContentWithType(content interface{}, contentType string) []interface{} {
	switch c := content.(type) {
	case string:
		return []interface{}{
			map[string]interface{}{"type": contentType, "text": c},
		}
	case []interface{}:
		// Multimodal content
		var result []interface{}
		for _, part := range c {
			if p, ok := part.(map[string]interface{}); ok {
				partType, _ := p["type"].(string)
				switch partType {
				case "text":
					result = append(result, map[string]interface{}{
						"type": contentType,
						"text": p["text"],
					})
				case "image_url":
					result = append(result, map[string]interface{}{
						"type":      "input_image",
						"image_url": p["image_url"],
					})
				}
			}
		}
		return result
	default:
		return nil
	}
}

func copyIfExists(src, dst map[string]interface{}, key string) {
	if v, ok := src[key]; ok {
		dst[key] = v
	}
}

// SupportedCodexModels returns the list of Codex-supported models
func SupportedCodexModels() []string {
	return []string{
		"gpt-5.2-codex",
		"gpt-5.2",
		"gpt-5.1-codex-max",
		"gpt-5.1-codex-mini",
	}
}
