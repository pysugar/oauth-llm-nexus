package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type gatewayTestRequest struct {
	Protocol string `json:"protocol"`
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Stream   bool   `json:"stream,omitempty"`
	Tools    bool   `json:"tools,omitempty"`
}

type gatewayTestResponse struct {
	OK                bool   `json:"ok"`
	Protocol          string `json:"protocol"`
	Path              string `json:"path"`
	Model             string `json:"model"`
	RequestedProvider string `json:"requested_provider,omitempty"`
	Provider          string `json:"provider,omitempty"`
	UpstreamModel     string `json:"upstream_model,omitempty"`
	Route             string `json:"route,omitempty"`
	RouteSource       string `json:"route_source,omitempty"`
	StatusCode        int    `json:"status_code"`
	DurationMS        int64  `json:"duration_ms"`
	Summary           string `json:"summary"`
	Text              string `json:"text,omitempty"`
	FinishReason      string `json:"finish_reason,omitempty"`
	Usage             any    `json:"usage,omitempty"`
	RequestPayload    string `json:"request_payload,omitempty"`
	Snippet           string `json:"snippet,omitempty"`
	Error             string `json:"error,omitempty"`
}

// HandleGatewayTest serves POST /api/gateway/test and executes an internal request
// through the live gateway handlers across OpenAI Chat, OpenAI Responses, Anthropic Messages,
// or Gemini protocols so callers and the Dashboard can verify compatibility end-to-end.
func (s *Server) HandleGatewayTest(w http.ResponseWriter, r *http.Request) {
	var req gatewayTestRequest
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body"})
			return
		}
	}

	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		req.Model = strings.TrimSpace(r.URL.Query().Get("model"))
	}
	if req.Protocol == "" {
		req.Protocol = strings.TrimSpace(r.URL.Query().Get("protocol"))
	}
	if req.Provider == "" {
		req.Provider = strings.TrimSpace(r.URL.Query().Get("provider"))
	}
	if req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model is required"})
		return
	}

	proto, err := normalizeTestProtocol(req.Protocol)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		if req.Tools {
			prompt = "Check the weather in Tokyo using the get_weather tool."
		} else {
			prompt = "Reply briefly with OK."
		}
	}

	path, payload, err := buildTestPayload(proto, req.Model, prompt, req.Stream, req.Tools)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	subReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload)).WithContext(ctx)
	subReq.Header.Set("Content-Type", "application/json")
	if proto == ir.ProtoAnthropic {
		subReq.Header.Set("anthropic-version", "2023-06-01")
	}
	if req.Provider != "" {
		subReq.Header.Set("X-Nexus-Provider", req.Provider)
	}

	rec := httptest.NewRecorder()
	start := time.Now()
	switch proto {
	case ir.ProtoOpenAIChat:
		s.HandleOpenAIChat(rec, subReq)
	case ir.ProtoOpenAIResp:
		s.HandleOpenAIResponses(rec, subReq)
	case ir.ProtoAnthropic:
		s.HandleAnthropicMessages(rec, subReq)
	case ir.ProtoGemini:
		s.HandleGeminiModels(rec, subReq)
	}
	durMS := time.Since(start).Milliseconds()

	rawBody := rec.Body.Bytes()
	res := gatewayTestResponse{
		OK:                rec.Code >= 200 && rec.Code < 300,
		Protocol:          string(proto),
		Path:              path,
		Model:             req.Model,
		RequestedProvider: req.Provider,
		Provider:          rec.Header().Get("X-Nexus-Provider"),
		UpstreamModel:     rec.Header().Get("X-Nexus-Upstream-Model"),
		Route:             rec.Header().Get("X-Nexus-Route"),
		RouteSource:       rec.Header().Get("X-Nexus-Route-Source"),
		StatusCode:        rec.Code,
		DurationMS:        durMS,
		RequestPayload:    truncateText(string(payload), 2048),
		Snippet:           truncateText(strings.TrimSpace(string(rawBody)), 4096),
	}

	extractGatewayTestDetails(proto, rawBody, &res)
	if res.OK {
		if res.Provider != "" {
			res.Summary = "HTTP " + itoa(res.StatusCode) + " via " + res.Provider
		} else {
			res.Summary = "HTTP " + itoa(res.StatusCode)
		}
	} else {
		if res.Error != "" {
			res.Summary = "HTTP " + itoa(res.StatusCode) + ": " + res.Error
		} else {
			res.Summary = "HTTP " + itoa(res.StatusCode)
		}
	}

	writeJSON(w, http.StatusOK, res)
}

func normalizeTestProtocol(raw string) (ir.Protocol, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "openai-chat", "openai_chat", "chat", "openai":
		return ir.ProtoOpenAIChat, nil
	case "openai-responses", "openai_responses", "responses":
		return ir.ProtoOpenAIResp, nil
	case "anthropic", "anthropic-messages", "anthropic_messages", "messages", "claude":
		return ir.ProtoAnthropic, nil
	case "gemini", "gemini-api", "gemini_api", "genai":
		return ir.ProtoGemini, nil
	default:
		return "", ir.NewError(400, "unsupported_protocol", "unsupported protocol: "+raw)
	}
}

func buildTestPayload(proto ir.Protocol, model, prompt string, stream, withTools bool) (string, []byte, error) {
	toolParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string", "description": "City name"},
		},
		"required": []string{"city"},
	}

	switch proto {
	case ir.ProtoOpenAIChat:
		body := map[string]any{
			"model":      model,
			"messages":   []map[string]any{{"role": "user", "content": prompt}},
			"max_tokens": 256,
			"stream":     stream,
		}
		if withTools {
			body["tools"] = []map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name":        "get_weather",
					"description": "Get weather for a city",
					"parameters":  toolParams,
				},
			}}
		}
		b, err := json.Marshal(body)
		return "/v1/chat/completions", b, err

	case ir.ProtoOpenAIResp:
		body := map[string]any{
			"model":             model,
			"input":             prompt,
			"max_output_tokens": 256,
			"stream":            stream,
		}
		if withTools {
			body["tools"] = []map[string]any{{
				"type":        "function",
				"name":        "get_weather",
				"description": "Get weather for a city",
				"parameters":  toolParams,
			}}
		}
		b, err := json.Marshal(body)
		return "/v1/responses", b, err

	case ir.ProtoAnthropic:
		body := map[string]any{
			"model":      model,
			"max_tokens": 256,
			"messages":   []map[string]any{{"role": "user", "content": prompt}},
			"stream":     stream,
		}
		if withTools {
			body["tools"] = []map[string]any{{
				"name":         "get_weather",
				"description":  "Get weather for a city",
				"input_schema": toolParams,
			}}
		}
		b, err := json.Marshal(body)
		return "/anthropic/v1/messages", b, err

	case ir.ProtoGemini:
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		body := map[string]any{
			"contents": []map[string]any{
				{"role": "user", "parts": []map[string]any{{"text": prompt}}},
			},
			"generationConfig": map[string]any{"maxOutputTokens": 256},
		}
		if withTools {
			body["tools"] = []map[string]any{{
				"functionDeclarations": []map[string]any{{
					"name":        "get_weather",
					"description": "Get weather for a city",
					"parameters":  toolParams,
				}},
			}}
		}
		b, err := json.Marshal(body)
		return "/v1beta/models/" + model + ":" + action, b, err

	default:
		return "", nil, ir.NewError(400, "unsupported_protocol", "unsupported protocol")
	}
}

func extractGatewayTestDetails(proto ir.Protocol, rawBody []byte, res *gatewayTestResponse) {
	var m map[string]any
	if json.Unmarshal(rawBody, &m) != nil || m == nil {
		if res.OK {
			if sseText := extractSSEText(rawBody); sseText != "" {
				res.Text = truncateText(sseText, 240)
				return
			}
		}
		if !res.OK && res.Error == "" {
			res.Error = truncateText(strings.TrimSpace(string(rawBody)), 240)
		}
		return
	}

	if errObj, ok := m["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok && msg != "" {
			res.Error = truncateText(msg, 240)
		}
	}
	if res.Error == "" {
		if msg, ok := m["message"].(string); ok && !res.OK {
			res.Error = truncateText(msg, 240)
		}
	}
	if u, ok := m["usage"]; ok {
		res.Usage = u
	} else if u, ok := m["usageMetadata"]; ok {
		res.Usage = u
	}

	switch proto {
	case ir.ProtoOpenAIChat:
		if choices, ok := m["choices"].([]any); ok && len(choices) > 0 {
			if c0, ok := choices[0].(map[string]any); ok {
				if fr, ok := c0["finish_reason"].(string); ok {
					res.FinishReason = fr
				}
				if msg, ok := c0["message"].(map[string]any); ok {
					var parts []string
					if content, ok := msg["content"].(string); ok && content != "" {
						parts = append(parts, content)
					} else if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
						parts = append(parts, rc)
					}
					if tcs, ok := msg["tool_calls"].([]any); ok {
						for _, rawTC := range tcs {
							if tc, ok := rawTC.(map[string]any); ok {
								if fn, ok := tc["function"].(map[string]any); ok {
									name, _ := fn["name"].(string)
									args, _ := fn["arguments"].(string)
									parts = append(parts, "[tool_call: "+name+"("+args+")]")
								}
							}
						}
					}
					if len(parts) > 0 {
						res.Text = truncateText(strings.Join(parts, " "), 320)
					}
				}
			}
		}

	case ir.ProtoOpenAIResp:
		if st, ok := m["status"].(string); ok {
			res.FinishReason = st
		}
		if ot, ok := m["output_text"].(string); ok && ot != "" {
			res.Text = truncateText(ot, 240)
			return
		}
		if out, ok := m["output"].([]any); ok {
			var parts []string
			for _, item := range out {
				obj, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if objType, _ := obj["type"].(string); objType == "function_call" {
					name, _ := obj["name"].(string)
					args, _ := obj["arguments"].(string)
					parts = append(parts, "[tool_call: "+name+"("+args+")]")
					continue
				}
				if content, ok := obj["content"].([]any); ok {
					for _, c := range content {
						cm, ok := c.(map[string]any)
						if !ok {
							continue
						}
						if t, ok := cm["text"].(string); ok && t != "" {
							parts = append(parts, t)
						}
					}
				}
			}
			if len(parts) > 0 {
				res.Text = truncateText(strings.Join(parts, " "), 320)
			}
		}

	case ir.ProtoAnthropic:
		if sr, ok := m["stop_reason"].(string); ok {
			res.FinishReason = sr
		}
		if content, ok := m["content"].([]any); ok {
			var parts []string
			for _, item := range content {
				cm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if cmType, _ := cm["type"].(string); cmType == "tool_use" {
					name, _ := cm["name"].(string)
					inputJSON, _ := json.Marshal(cm["input"])
					parts = append(parts, "[tool_use: "+name+"("+string(inputJSON)+")]")
					continue
				}
				if t, ok := cm["text"].(string); ok && t != "" {
					parts = append(parts, t)
				} else if th, ok := cm["thinking"].(string); ok && th != "" && len(parts) == 0 {
					parts = append(parts, th)
				}
			}
			if len(parts) > 0 {
				res.Text = truncateText(strings.Join(parts, " "), 320)
			}
		}

	case ir.ProtoGemini:
		if cands, ok := m["candidates"].([]any); ok && len(cands) > 0 {
			if c0, ok := cands[0].(map[string]any); ok {
				if fr, ok := c0["finishReason"].(string); ok {
					res.FinishReason = fr
				}
				if content, ok := c0["content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok {
						var texts []string
						var thoughts []string
						for _, p := range parts {
							pm, ok := p.(map[string]any)
							if !ok {
								continue
							}
							if fc, ok := pm["functionCall"].(map[string]any); ok {
								name, _ := fc["name"].(string)
								argsJSON, _ := json.Marshal(fc["args"])
								texts = append(texts, "[functionCall: "+name+"("+string(argsJSON)+")]")
								continue
							}
							if t, ok := pm["text"].(string); ok && t != "" {
								if isThought, _ := pm["thought"].(bool); isThought {
									thoughts = append(thoughts, t)
								} else {
									texts = append(texts, t)
								}
							}
						}
						if len(texts) == 0 && len(thoughts) > 0 {
							texts = thoughts
						}
						if len(texts) > 0 {
							res.Text = truncateText(strings.Join(texts, " "), 320)
						}
					}
				}
			}
		}
	}
}

func truncateText(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func extractSSEText(raw []byte) string {
	var out strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(payload), &m) != nil {
			continue
		}
		// Chat completions SSE delta
		if choices, ok := m["choices"].([]any); ok && len(choices) > 0 {
			if c0, ok := choices[0].(map[string]any); ok {
				if delta, ok := c0["delta"].(map[string]any); ok {
					if content, ok := delta["content"].(string); ok {
						out.WriteString(content)
					}
					if tcs, ok := delta["tool_calls"].([]any); ok {
						for _, rawTC := range tcs {
							if tc, ok := rawTC.(map[string]any); ok {
								if fn, ok := tc["function"].(map[string]any); ok {
									if name, _ := fn["name"].(string); name != "" {
										out.WriteString("[tool_call: " + name + "] ")
									}
									if args, _ := fn["arguments"].(string); args != "" {
										out.WriteString(args)
									}
								}
							}
						}
					}
				}
			}
		}
		// Responses SSE delta
		if t, _ := m["type"].(string); t == "response.output_text.delta" {
			if delta, ok := m["delta"].(string); ok {
				out.WriteString(delta)
			}
		} else if t == "response.output_item.added" {
			if item, ok := m["item"].(map[string]any); ok && item["type"] == "function_call" {
				if name, _ := item["name"].(string); name != "" {
					out.WriteString("[tool_call: " + name + "] ")
				}
			}
		} else if t == "response.function_call_arguments.delta" {
			if delta, ok := m["delta"].(string); ok {
				out.WriteString(delta)
			}
		}
		// Anthropic SSE delta
		if t, _ := m["type"].(string); t == "content_block_delta" {
			if delta, ok := m["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					out.WriteString(text)
				}
				if pjson, ok := delta["partial_json"].(string); ok {
					out.WriteString(pjson)
				}
			}
		} else if t == "content_block_start" {
			if cb, ok := m["content_block"].(map[string]any); ok && cb["type"] == "tool_use" {
				if name, _ := cb["name"].(string); name != "" {
					out.WriteString("[tool_use: " + name + "] ")
				}
			}
		}
		// Gemini SSE chunk
		if cands, ok := m["candidates"].([]any); ok && len(cands) > 0 {
			if c0, ok := cands[0].(map[string]any); ok {
				if content, ok := c0["content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok {
						for _, p := range parts {
							pm, ok := p.(map[string]any)
							if !ok {
								continue
							}
							if fc, ok := pm["functionCall"].(map[string]any); ok {
								name, _ := fc["name"].(string)
								argsJSON, _ := json.Marshal(fc["args"])
								out.WriteString("[functionCall: " + name + "(" + string(argsJSON) + ")] ")
								continue
							}
							if text, ok := pm["text"].(string); ok && text != "" {
								out.WriteString(text)
							}
						}
					}
				}
			}
		}
	}
	return out.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
