// Package ir defines the protocol-neutral intermediate representation for the LLM API aggregation gateway.
package ir

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Protocol identifies one of the four supported frontend/backend wire formats.
type Protocol string

const (
	ProtoOpenAIChat Protocol = "openai-chat"
	ProtoOpenAIResp Protocol = "openai-responses"
	ProtoAnthropic  Protocol = "anthropic"
	ProtoGemini     Protocol = "gemini"
)

// Role identifies the speaker role of a conversation message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// PartKind identifies the content kind of a Part inside a message or response.
type PartKind string

const (
	KindText       PartKind = "text"
	KindThought    PartKind = "thought"
	KindImage      PartKind = "image"
	KindFile       PartKind = "file"
	KindToolCall   PartKind = "tool_call"
	KindToolResult PartKind = "tool_result"
	KindRefusal    PartKind = "refusal"
)

// Finish reason constants normalized across protocols.
const (
	FinishStop          = "stop"
	FinishLength        = "length"
	FinishToolCalls     = "tool_calls"
	FinishContentFilter = "content_filter"
	FinishError         = "error"
)

// Part represents a single content element inside a message or model output.
type Part struct {
	Kind             PartKind
	Text             string
	Signature        string
	URL              string
	Mime             string
	Data             []byte
	Filename         string
	Detail           string
	CallID           string
	Name             string
	Args             string
	ThoughtSignature string
	Result           string
	IsError          bool
	ResultParts      []Part
}

// Message represents a single turn in the conversation history.
type Message struct {
	Role  Role
	Name  string
	Parts []Part
}

// Tool declares a callable function tool in JSON Schema form.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Strict      bool
}

// ToolChoice specifies how the model should select among declared tools.
type ToolChoice struct {
	Mode string // "", "auto", "none", "required", "tool"
	Name string // Tool name when Mode == "tool"
}

// Reasoning configures thinking/reasoning effort or token budget.
type Reasoning struct {
	Effort string // "", "none", "minimal", "low", "medium", "high", "xhigh", "max"
	Budget int    // >0 explicit token budget, -1 dynamic, 0 unset
	Raw    bool   // true when the client explicitly supplied reasoning/thinking controls
}

// Format configures structured JSON or schema-constrained output.
type Format struct {
	Type        string // "", "text", "json_object", "json_schema"
	Name        string
	Description string
	Schema      json.RawMessage
	Strict      *bool
}

// Request is the protocol-neutral representation of an inference request.
type Request struct {
	Source             Protocol
	Model              string
	System             []Part
	Messages           []Message
	Tools              []Tool
	ToolChoice         ToolChoice
	ParallelToolCalls  *bool
	Stream             bool
	MaxTokens          int
	Temperature        *float64
	TopP               *float64
	TopK               *int
	Seed               *int64
	Stop               []string
	PresencePenalty    *float64
	FrequencyPenalty   *float64
	Reasoning          Reasoning
	Format             *Format
	IncludeUsage       *bool
	User               string
	Store              *bool
	PreviousResponseID string
	// UsageInjected is gateway-internal: the attempt added stream_options.include_usage the client
	// did not ask for, so the usage-only chunk is read for accounting but not forwarded.
	UsageInjected bool
	// SkipRespStore is gateway-internal: the request went unmasked to a provider that skips
	// privacy masking, so its turn must not enter the shared ResponseStore (a later turn could
	// replay it, unmasked, to another backend).
	SkipRespStore bool
}

// SystemText joins all KindText system parts with double newlines.
func (r *Request) SystemText() string {
	var parts []string
	for _, p := range r.System {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// EventKind distinguishes streaming IR deltas and lifecycle markers.
type EventKind int

const (
	EventStart EventKind = iota
	EventText
	EventThought
	EventThoughtSignature
	EventToolCallStart
	EventToolCallDelta
	EventToolCallDone
	EventUsage
	EventFinish
	EventError
)

// Usage tracks token counts across protocols.
// InputTokens stores total input tokens (including cached tokens).
// OutputTokens stores total output tokens (including reasoning tokens).
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
}

// UncachedInputTokens returns InputTokens minus cached read/write tokens, floored at 0.
func (u Usage) UncachedInputTokens() int {
	v := u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens
	if v < 0 {
		return 0
	}
	return v
}

// NonReasoningOutputTokens returns OutputTokens minus ReasoningTokens, floored at 0.
func (u Usage) NonReasoningOutputTokens() int {
	v := u.OutputTokens - u.ReasoningTokens
	if v < 0 {
		return 0
	}
	return v
}

// TotalTokens returns InputTokens + OutputTokens.
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens
}

// GatewayError represents a structured HTTP/API error from an upstream or codec.
type GatewayError struct {
	HTTPStatus int
	Type       string
	Code       string
	Message    string
}

func (e *GatewayError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return fmt.Sprintf("gateway error (%d %s): %s", e.HTTPStatus, e.Code, e.Message)
	}
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("gateway error (%d): %s", e.HTTPStatus, e.Message)
	}
	return e.Message
}

// NewError creates a GatewayError with the given HTTP status, code, and message.
func NewError(status int, code, message string) *GatewayError {
	errType := "invalid_request_error"
	if status >= 500 {
		errType = "server_error"
	} else if status == 401 || status == 403 {
		errType = "authentication_error"
	} else if status == 429 {
		errType = "rate_limit_error"
	} else if status == 404 {
		errType = "not_found_error"
	}
	return &GatewayError{
		HTTPStatus: status,
		Type:       errType,
		Code:       code,
		Message:    message,
	}
}

// Event is a single incremental output emitted by a upstream StreamDecoder.
type Event struct {
	Kind             EventKind
	ResponseID       string
	Model            string
	Text             string
	Signature        string
	CallID           string
	Name             string
	Args             string
	ThoughtSignature string
	FinishReason     string
	Usage            Usage
	Err              *GatewayError
}

// Result is the collected non-streaming output aggregated from Events.
type Result struct {
	ID           string
	Model        string
	Content      []Part
	FinishReason string
	Usage        Usage
}

// NewID generates a random hex identifier with the given prefix.
func NewID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// TextOf extracts concatenated plain text from a slice of parts.
func TextOf(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == KindText || p.Kind == KindRefusal || p.Kind == "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ArgsOrEmpty ensures tool arguments are a valid JSON object string, defaulting to "{}".
func ArgsOrEmpty(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	return trimmed
}

// ParseArgs decodes JSON tool arguments into a map, falling back to an empty map on blank/invalid JSON.
func ParseArgs(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// MergeUsage combines delta/final usage counters while keeping non-zero prior estimates.
func MergeUsage(dst *Usage, src Usage) {
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheReadTokens > 0 {
		dst.CacheReadTokens = src.CacheReadTokens
	}
	if src.CacheWriteTokens > 0 {
		dst.CacheWriteTokens = src.CacheWriteTokens
	}
	if src.ReasoningTokens > 0 {
		dst.ReasoningTokens = src.ReasoningTokens
	}
}

const geminiSigSep = "__gsig_"

// EncodeGeminiCallSig embeds a thought signature suffix into a synthetic call ID when needed
// so clients running through OpenAI/Anthropic codecs preserve it across turns.
func EncodeGeminiCallSig(callID, sig string) string {
	if sig == "" || strings.Contains(callID, geminiSigSep) {
		return callID
	}
	if callID == "" {
		callID = NewID("call_")
	}
	return callID + geminiSigSep + base64.RawURLEncoding.EncodeToString([]byte(sig))
}

// DecodeGeminiCallSig extracts the base call ID and optional embedded thought signature.
func DecodeGeminiCallSig(id string) (callID string, sig string) {
	idx := strings.Index(id, geminiSigSep)
	if idx < 0 {
		return id, ""
	}
	rawSig, err := base64.RawURLEncoding.DecodeString(id[idx+len(geminiSigSep):])
	if err != nil {
		return id[:idx], ""
	}
	return id[:idx], string(rawSig)
}

// SanitizeToolID encodes arbitrary call IDs into Anthropic-safe [A-Za-z0-9_-] tokens.
func SanitizeToolID(id string) string {
	if id == "" {
		return NewID("toolu_")
	}
	clean := true
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			clean = false
			break
		}
	}
	if clean {
		return id
	}
	return "mp_" + base64.RawURLEncoding.EncodeToString([]byte(id))
}

// UnsanitizeToolID decodes tool IDs previously produced by SanitizeToolID.
func UnsanitizeToolID(id string) string {
	if !strings.HasPrefix(id, "mp_") {
		return id
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "mp_"))
	if err != nil || len(raw) == 0 {
		return id
	}
	return string(raw)
}
