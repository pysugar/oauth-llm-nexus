// Package codec defines protocol codecs bridging wire JSON/SSE formats and the gateway IR.
package codec

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

// Quirks controls provider-specific request/response adaptations during codec translation.
type Quirks struct {
	MaxCompletionTokens   bool // Use max_completion_tokens instead of max_tokens in OpenAI Chat
	EchoReasoningContent  bool // Replay reasoning_content on assistant tool-calling turns (DeepSeek)
	IncludeUsageInStream  bool // Attach stream_options.include_usage=true when building OpenAI Chat stream requests
	OmitStreamOptions     bool // Suppress stream_options entirely if the backend rejects it
	OmitParallelToolCalls bool // Suppress parallel_tool_calls if backend rejects it
	OmitStrictTools       bool // Suppress strict=true inside tool schemas
	OmitReasoningEffort   bool // Suppress reasoning_effort in body for backends that reject it
	ThoughtTagParsing     bool // Parse <think>/<thought> tags out of OpenAI Chat text deltas
	StreamFirst           bool // Use upstream SSE even for non-streaming requests to avoid header timeouts
	EmbeddingInputType    bool // Forward OpenAI embeddings input_type (query/passage) instead of stripping it (NVIDIA)
}

// StreamDecoder decodes upstream SSE frames into protocol-neutral IR events.
type StreamDecoder interface {
	DecodeFrame(frame sse.Event) (events []ir.Event, done bool, err *ir.GatewayError)
	Flush() []ir.Event
}

// StreamEncoder converts IR events into downstream SSE frames for a specific client protocol.
type StreamEncoder interface {
	Encode(ev ir.Event, w *sse.Writer) error
	Finish(w *sse.Writer) error
}

// Codec parses client requests, builds upstream payloads, decodes/encodes streams, and formats errors.
type Codec interface {
	Protocol() ir.Protocol
	ParseRequest(modelHint string, body []byte) (*ir.Request, *ir.GatewayError)
	BuildRequest(req *ir.Request, upstreamModel string, stream bool, q Quirks) ([]byte, *ir.GatewayError)
	NewDecoder(q Quirks) StreamDecoder
	NewEncoder(model string) StreamEncoder
	EncodeResult(res *ir.Result) ([]byte, *ir.GatewayError)
	EncodeError(err *ir.GatewayError) []byte
}

// RewriteModelJSON updates the top-level "model" field in raw JSON while preserving all other fields.
func RewriteModelJSON(body []byte, targetModel string) ([]byte, error) {
	if targetModel == "" {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		obj = make(map[string]json.RawMessage)
	}
	modelBytes, err := json.Marshal(targetModel)
	if err != nil {
		return nil, err
	}
	obj["model"] = json.RawMessage(modelBytes)
	return json.Marshal(obj)
}

// RewriteStreamJSON sets or updates the top-level "stream" field in a JSON object payload.
func RewriteStreamJSON(body []byte, stream bool) ([]byte, error) {
	return RewriteStreamOptionsJSON(body, stream, false)
}

// RewriteStreamOptionsJSON updates "stream" and optionally injects OpenAI Chat
// "stream_options":{"include_usage":true} when not already specified by the client.
func RewriteStreamOptionsJSON(body []byte, stream, includeUsage bool) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return body, nil
	}
	if stream {
		obj["stream"] = json.RawMessage("true")
		if includeUsage {
			if _, exists := obj["stream_options"]; !exists {
				obj["stream_options"] = json.RawMessage(`{"include_usage":true}`)
			}
		}
	} else {
		obj["stream"] = json.RawMessage("false")
	}
	return json.Marshal(obj)
}

// ApplyOpenAIChatQuirksJSON adapts raw OpenAI Chat Completions JSON bodies on passthrough
// routes so provider quirks (e.g., max_completion_tokens on GPT-5/o-series) are respected
// without full IR lossy re-encoding.
func ApplyOpenAIChatQuirksJSON(body []byte, q Quirks) ([]byte, error) {
	if !q.MaxCompletionTokens && !q.OmitStreamOptions && !q.OmitParallelToolCalls && !q.OmitReasoningEffort {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return body, nil
	}
	changed := false
	if q.MaxCompletionTokens {
		if mt, hasMT := obj["max_tokens"]; hasMT {
			if _, hasMCT := obj["max_completion_tokens"]; !hasMCT {
				obj["max_completion_tokens"] = mt
			}
			delete(obj, "max_tokens")
			changed = true
		}
	}
	if q.OmitStreamOptions {
		if _, ok := obj["stream_options"]; ok {
			delete(obj, "stream_options")
			changed = true
		}
	}
	if q.OmitParallelToolCalls {
		if _, ok := obj["parallel_tool_calls"]; ok {
			delete(obj, "parallel_tool_calls")
			changed = true
		}
	}
	if q.OmitReasoningEffort {
		if _, ok := obj["reasoning_effort"]; ok {
			delete(obj, "reasoning_effort")
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(obj)
}

// IsStrictReasoningModel reports whether modelID belongs to OpenAI's o-series / GPT-5
// family that rejects sampling parameters such as temperature and top_p.
func IsStrictReasoningModel(modelID string) bool {
	base := strings.ToLower(strings.TrimSpace(modelID))
	if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
		base = base[slash+1:]
	}
	if base == "" || strings.Contains(base, "-chat") {
		return false
	}
	for _, prefix := range []string{"o1", "o3", "o4", "gpt-5"} {
		if base == prefix || strings.HasPrefix(base, prefix+"-") || strings.HasPrefix(base, prefix+".") {
			return true
		}
	}
	return false
}

// StripReasoningSamplingJSON removes temperature and top_p from raw OpenAI payloads
// when targeting a strict reasoning model under MaxCompletionTokens-enabled providers.
func StripReasoningSamplingJSON(body []byte, model string, q Quirks) ([]byte, error) {
	if !q.MaxCompletionTokens || !IsStrictReasoningModel(model) {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return body, nil
	}
	changed := false
	for _, field := range []string{"temperature", "top_p"} {
		if _, ok := obj[field]; ok {
			delete(obj, field)
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(obj)
}

// DecodeDataURL parses an RFC 2397 data URL into MIME type and decoded bytes.
func DecodeDataURL(rawURL string) (mime string, data []byte, ok bool) {
	if !strings.HasPrefix(rawURL, "data:") {
		return "", nil, false
	}
	rest := strings.TrimPrefix(rawURL, "data:")
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", nil, false
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	if !strings.Contains(meta, ";base64") {
		return "", nil, false
	}
	mime = strings.TrimSuffix(meta, ";base64")
	if idx := strings.IndexByte(mime, ';'); idx >= 0 {
		mime = mime[:idx]
	}
	mime = strings.TrimSpace(mime)
	if mime == "" {
		mime = "application/octet-stream"
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(payload))
		if err != nil {
			return "", nil, false
		}
	}
	return mime, decoded, true
}

// EncodeDataURL encodes MIME type and bytes into a base64 data URL.
func EncodeDataURL(mime string, data []byte) string {
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// ParseUpstreamError extracts a structured GatewayError from OpenAI, Anthropic, Gemini,
// AMD Token Factory (nested detail.error), or RFC 7807 problem+json error bodies.
func ParseUpstreamError(status int, body []byte) *ir.GatewayError {
	if status == 0 {
		status = 502
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ir.NewError(status, "", fmt.Sprintf("upstream returned HTTP %d", status))
	}

	if ge := parseErrorJSON(status, []byte(trimmed), 0); ge != nil {
		return ge
	}

	if len(trimmed) > 512 {
		trimmed = trimmed[:512] + "..."
	}
	return ir.NewError(status, "", trimmed)
}

func parseErrorJSON(status int, raw []byte, depth int) *ir.GatewayError {
	if depth > 4 {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}

	// Unwrap top-level JSON array (e.g. Gemini streaming error envelope: [{"error": {...}}]).
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil && len(arr) > 0 {
			return parseErrorJSON(status, arr[0], depth+1)
		}
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}

	var envelope struct {
		Type    string          `json:"type"`
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
		Title   string          `json:"title"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil
	}

	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		var errStr string
		if json.Unmarshal(envelope.Error, &errStr) == nil && strings.TrimSpace(errStr) != "" {
			return ir.NewError(status, "", strings.TrimSpace(errStr))
		}
		var errObj struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		}
		if json.Unmarshal(envelope.Error, &errObj) == nil && errObj.Message != "" {
			code := formatAnyCode(errObj.Code)
			for _, d := range errObj.Details {
				if r := strings.TrimSpace(d.Reason); r != "" {
					code = r
					break
				}
			}
			if code == "" {
				code = errObj.Status
			}
			ge := ir.NewError(status, code, errObj.Message)
			if errObj.Type != "" {
				ge.Type = errObj.Type
			}
			return ge
		}
	}

	// AMD Token Factory and FastAPI relays often wrap OpenAI errors under {"detail": {"error": {...}}} or {"detail": "..."}.
	if len(envelope.Detail) > 0 && string(envelope.Detail) != "null" {
		var detailStr string
		if json.Unmarshal(envelope.Detail, &detailStr) == nil && strings.TrimSpace(detailStr) != "" {
			return ir.NewError(status, "", strings.TrimSpace(detailStr))
		}
		if nested := parseErrorJSON(status, envelope.Detail, depth+1); nested != nil {
			return nested
		}
	}

	if strings.TrimSpace(envelope.Message) != "" {
		return ir.NewError(status, "", strings.TrimSpace(envelope.Message))
	}
	if strings.TrimSpace(envelope.Title) != "" {
		return ir.NewError(status, "", strings.TrimSpace(envelope.Title))
	}
	return nil
}

func formatAnyCode(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case float64:
		return fmt.Sprintf("%.0f", c)
	default:
		return ""
	}
}
