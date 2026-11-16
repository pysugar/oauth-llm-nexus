package gemini

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

type gemEncoder struct {
	id         string
	model      string
	finished   bool
	toolID     string
	toolName   string
	toolArgs   strings.Builder
	usage      ir.Usage
	inToolCall bool
}

// NewEncoder creates a Google Gemini SSE StreamEncoder.
func (c *Codec) NewEncoder(model string) codec.StreamEncoder {
	return &gemEncoder{
		id:    ir.NewID("gem_"),
		model: model,
	}
}

func (e *gemEncoder) flushOpenTool(w *sse.Writer) error {
	if !e.inToolCall {
		return nil
	}
	e.inToolCall = false
	args := ir.ParseArgs(e.toolArgs.String())
	e.toolArgs.Reset()
	fc := map[string]any{
		"name": e.toolName,
		"args": args,
	}
	if e.toolID != "" {
		fc["id"] = e.toolID
	}
	return e.writeChunk(w, []map[string]any{{"functionCall": fc}}, "", nil)
}

func (e *gemEncoder) Encode(ev ir.Event, w *sse.Writer) error {
	switch ev.Kind {
	case ir.EventStart:
		if ev.ResponseID != "" {
			e.id = ev.ResponseID
		}
		if ev.Model != "" {
			e.model = ev.Model
		}
		ir.MergeUsage(&e.usage, ev.Usage)
		return nil

	case ir.EventUsage:
		ir.MergeUsage(&e.usage, ev.Usage)
		return nil

	case ir.EventThought:
		if ev.Text == "" && ev.Signature == "" {
			return nil
		}
		if err := e.flushOpenTool(w); err != nil {
			return err
		}
		p := map[string]any{
			"thought": true,
			"text":    ev.Text,
		}
		if ev.Signature != "" {
			p["thoughtSignature"] = ev.Signature
		}
		return e.writeChunk(w, []map[string]any{p}, "", nil)

	case ir.EventText:
		if ev.Text == "" {
			return nil
		}
		if err := e.flushOpenTool(w); err != nil {
			return err
		}
		return e.writeChunk(w, []map[string]any{{"text": ev.Text}}, "", nil)

	case ir.EventToolCallStart:
		if err := e.flushOpenTool(w); err != nil {
			return err
		}
		e.inToolCall = true
		e.toolID = ev.CallID
		e.toolName = ev.Name
		e.toolArgs.Reset()
		if ev.Args != "" {
			e.toolArgs.WriteString(ev.Args)
		}
		return nil

	case ir.EventToolCallDelta:
		if !e.inToolCall {
			e.inToolCall = true
			e.toolID = ev.CallID
			e.toolName = ev.Name
		}
		e.toolArgs.WriteString(ev.Args)
		return nil

	case ir.EventToolCallDone:
		if e.inToolCall {
			return e.flushOpenTool(w)
		}
		return nil

	case ir.EventFinish:
		e.finished = true
		ir.MergeUsage(&e.usage, ev.Usage)
		if err := e.flushOpenTool(w); err != nil {
			return err
		}
		return e.writeChunk(w, []map[string]any{}, toGeminiFinishReason(ev.FinishReason), &e.usage)

	case ir.EventError:
		if ev.Err == nil {
			return nil
		}
		return w.WriteEvent("", encodeGeminiError(ev.Err))
	}
	return nil
}

func (e *gemEncoder) Finish(w *sse.Writer) error {
	if e.finished {
		return nil
	}
	e.finished = true
	if err := e.flushOpenTool(w); err != nil {
		return err
	}
	var usagePtr *ir.Usage
	if e.usage.TotalTokens() > 0 || e.usage.InputTokens > 0 || e.usage.OutputTokens > 0 {
		usagePtr = &e.usage
	}
	return e.writeChunk(w, []map[string]any{}, "STOP", usagePtr)
}

func (e *gemEncoder) writeChunk(w *sse.Writer, parts []map[string]any, finishReason string, usage *ir.Usage) error {
	cand := map[string]any{
		"index": 0,
		"content": map[string]any{
			"role":  "model",
			"parts": parts,
		},
	}
	if finishReason != "" {
		cand["finishReason"] = finishReason
	}
	payload := map[string]any{
		"responseId":   e.id,
		"modelVersion": e.model,
		"candidates":   []map[string]any{cand},
	}
	if usage != nil {
		payload["usageMetadata"] = encodeGeminiUsage(*usage)
	}
	return w.WriteJSON("", payload)
}

// EncodeResult renders an aggregated IR Result into a non-streaming Gemini GenerateContentResponse JSON.
func (c *Codec) EncodeResult(res *ir.Result) ([]byte, *ir.GatewayError) {
	if res == nil {
		return nil, ir.NewError(500, "empty_result", "nil result")
	}
	id := res.ID
	if id == "" {
		id = ir.NewID("gem_")
	}

	var parts []map[string]any
	for _, p := range res.Content {
		switch p.Kind {
		case ir.KindThought:
			gp := map[string]any{
				"thought": true,
				"text":    p.Text,
			}
			if p.Signature != "" {
				gp["thoughtSignature"] = p.Signature
			}
			parts = append(parts, gp)
		case ir.KindText, ir.KindRefusal, "":
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case ir.KindToolCall:
			fc := map[string]any{
				"name": p.Name,
				"args": ir.ParseArgs(p.Args),
			}
			if p.CallID != "" {
				fc["id"] = p.CallID
			}
			gp := map[string]any{"functionCall": fc}
			if p.ThoughtSignature != "" {
				gp["thoughtSignature"] = p.ThoughtSignature
			}
			parts = append(parts, gp)
		}
	}
	if len(parts) == 0 {
		parts = []map[string]any{{"text": ""}}
	}

	resp := map[string]any{
		"responseId":   id,
		"modelVersion": res.Model,
		"candidates": []map[string]any{{
			"index":        0,
			"finishReason": toGeminiFinishReason(res.FinishReason),
			"content": map[string]any{
				"role":  "model",
				"parts": parts,
			},
		}},
		"usageMetadata": encodeGeminiUsage(res.Usage),
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, ir.NewError(500, "marshal_error", err.Error())
	}
	return raw, nil
}

// EncodeError renders a GatewayError in Google Gemini's JSON error envelope.
func (c *Codec) EncodeError(err *ir.GatewayError) []byte {
	return encodeGeminiError(err)
}

func encodeGeminiError(err *ir.GatewayError) []byte {
	if err == nil {
		err = ir.NewError(500, "INTERNAL", "unknown error")
	}
	statusStr := "INTERNAL"
	switch err.HTTPStatus {
	case 400:
		statusStr = "INVALID_ARGUMENT"
	case 401:
		statusStr = "UNAUTHENTICATED"
	case 403:
		statusStr = "PERMISSION_DENIED"
	case 404:
		statusStr = "NOT_FOUND"
	case 429:
		statusStr = "RESOURCE_EXHAUSTED"
	case 503:
		statusStr = "UNAVAILABLE"
	default:
		if err.HTTPStatus >= 400 && err.HTTPStatus < 500 {
			statusStr = strings.ToUpper(http.StatusText(err.HTTPStatus))
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    err.HTTPStatus,
			"message": err.Message,
			"status":  statusStr,
		},
	})
	return raw
}

func encodeGeminiUsage(u ir.Usage) map[string]any {
	out := map[string]any{
		"promptTokenCount":     u.InputTokens,
		"candidatesTokenCount": u.NonReasoningOutputTokens(),
		"totalTokenCount":      u.TotalTokens(),
	}
	if u.CacheReadTokens > 0 {
		out["cachedContentTokenCount"] = u.CacheReadTokens
	}
	if u.ReasoningTokens > 0 {
		out["thoughtsTokenCount"] = u.ReasoningTokens
	}
	return out
}

func toGeminiFinishReason(reason string) string {
	switch reason {
	case ir.FinishLength:
		return "MAX_TOKENS"
	case ir.FinishContentFilter:
		return "SAFETY"
	default:
		return "STOP"
	}
}
