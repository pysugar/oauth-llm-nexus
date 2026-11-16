package openaichat

import (
	"encoding/json"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

type chatEncoder struct {
	id        string
	model     string
	created   int64
	sentRole  bool
	sentDone  bool
	usage     ir.Usage
	toolIndex map[string]int
	nextTool  int
}

// NewEncoder creates an OpenAI Chat Completions SSE StreamEncoder.
func (c *Codec) NewEncoder(model string) codec.StreamEncoder {
	return &chatEncoder{
		id:        ir.NewID("chatcmpl-"),
		model:     model,
		created:   time.Now().Unix(),
		toolIndex: make(map[string]int),
	}
}

func (e *chatEncoder) Encode(ev ir.Event, w *sse.Writer) error {
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
		if ev.Text == "" {
			return nil
		}
		delta := map[string]any{"reasoning_content": ev.Text}
		if !e.sentRole {
			delta["role"] = "assistant"
			e.sentRole = true
		}
		return e.writeChunk(w, delta, nil, nil)

	case ir.EventText:
		if ev.Text == "" {
			return nil
		}
		delta := map[string]any{"content": ev.Text}
		if !e.sentRole {
			delta["role"] = "assistant"
			e.sentRole = true
		}
		return e.writeChunk(w, delta, nil, nil)

	case ir.EventToolCallStart:
		idx, ok := e.toolIndex[ev.CallID]
		if !ok {
			idx = e.nextTool
			e.nextTool++
			e.toolIndex[ev.CallID] = idx
		}
		delta := map[string]any{
			"tool_calls": []map[string]any{{
				"index": idx,
				"id":    ev.CallID,
				"type":  "function",
				"function": map[string]any{
					"name":      ev.Name,
					"arguments": ev.Args,
				},
			}},
		}
		if !e.sentRole {
			delta["role"] = "assistant"
			e.sentRole = true
		}
		return e.writeChunk(w, delta, nil, nil)

	case ir.EventToolCallDelta:
		if ev.Args == "" {
			return nil
		}
		idx, ok := e.toolIndex[ev.CallID]
		if !ok {
			idx = e.nextTool
			e.nextTool++
			e.toolIndex[ev.CallID] = idx
		}
		delta := map[string]any{
			"tool_calls": []map[string]any{{
				"index": idx,
				"function": map[string]any{
					"arguments": ev.Args,
				},
			}},
		}
		return e.writeChunk(w, delta, nil, nil)

	case ir.EventFinish:
		ir.MergeUsage(&e.usage, ev.Usage)
		return e.finishWith(w, ev.FinishReason)

	case ir.EventError:
		if ev.Err == nil {
			return nil
		}
		return w.WriteEvent("", e.errorBytes(ev.Err))
	}
	return nil
}

func (e *chatEncoder) Finish(w *sse.Writer) error {
	if e.sentDone {
		return nil
	}
	return e.finishWith(w, "")
}

func (e *chatEncoder) finishWith(w *sse.Writer, finishReason string) error {
	if e.sentDone {
		return nil
	}
	e.sentDone = true
	reason := toChatFinishReason(finishReason)
	if reason == "" {
		if e.nextTool > 0 {
			reason = "tool_calls"
		} else {
			reason = "stop"
		}
	}
	if err := e.writeChunk(w, map[string]any{}, &reason, nil); err != nil {
		return err
	}
	if e.usage.TotalTokens() > 0 || e.usage.InputTokens > 0 || e.usage.OutputTokens > 0 {
		usageObj := encodeChatUsage(e.usage)
		if err := w.WriteJSON("", map[string]any{
			"id":      e.id,
			"object":  "chat.completion.chunk",
			"created": e.created,
			"model":   e.model,
			"choices": []any{},
			"usage":   usageObj,
		}); err != nil {
			return err
		}
	}
	return w.WriteDone()
}

func (e *chatEncoder) writeChunk(w *sse.Writer, delta map[string]any, finishReason *string, usage map[string]any) error {
	payload := map[string]any{
		"id":      e.id,
		"object":  "chat.completion.chunk",
		"created": e.created,
		"model":   e.model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
	if usage != nil {
		payload["usage"] = usage
	}
	return w.WriteJSON("", payload)
}

func (e *chatEncoder) errorBytes(err *ir.GatewayError) []byte {
	return encodeChatError(err)
}

// EncodeResult renders an aggregated IR Result into a non-streaming OpenAI Chat Completions JSON response.
func (c *Codec) EncodeResult(res *ir.Result) ([]byte, *ir.GatewayError) {
	if res == nil {
		return nil, ir.NewError(500, "empty_result", "nil result")
	}
	id := res.ID
	if id == "" {
		id = ir.NewID("chatcmpl-")
	}

	var text string
	var thoughts string
	var toolCalls []outChatToolRef

	for _, p := range res.Content {
		switch p.Kind {
		case ir.KindThought:
			thoughts += p.Text
		case ir.KindText, ir.KindRefusal, "":
			text += p.Text
		case ir.KindToolCall:
			callID := p.CallID
			if callID == "" {
				callID = ir.NewID("call_")
			}
			toolCalls = append(toolCalls, outChatToolRef{
				ID:   callID,
				Type: "function",
				Function: outChatFuncCall{
					Name:      p.Name,
					Arguments: ir.ArgsOrEmpty(p.Args),
				},
			})
		}
	}

	msg := map[string]any{
		"role": "assistant",
	}
	if text != "" || len(toolCalls) == 0 {
		msg["content"] = text
	} else {
		msg["content"] = nil
	}
	if thoughts != "" {
		msg["reasoning_content"] = thoughts
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	finishReason := toChatFinishReason(res.FinishReason)
	if finishReason == "" {
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}

	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   res.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": encodeChatUsage(res.Usage),
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, ir.NewError(500, "marshal_error", err.Error())
	}
	return raw, nil
}

// EncodeError renders a GatewayError in OpenAI's JSON error envelope.
func (c *Codec) EncodeError(err *ir.GatewayError) []byte {
	return encodeChatError(err)
}

func encodeChatError(err *ir.GatewayError) []byte {
	if err == nil {
		err = ir.NewError(500, "internal_error", "unknown error")
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": err.Message,
			"type":    err.Type,
			"code":    err.Code,
		},
	})
	return body
}

func encodeChatUsage(u ir.Usage) map[string]any {
	out := map[string]any{
		"prompt_tokens":     u.InputTokens,
		"completion_tokens": u.OutputTokens,
		"total_tokens":      u.TotalTokens(),
	}
	if u.CacheReadTokens > 0 {
		out["prompt_tokens_details"] = map[string]any{
			"cached_tokens": u.CacheReadTokens,
		}
	}
	if u.ReasoningTokens > 0 {
		out["completion_tokens_details"] = map[string]any{
			"reasoning_tokens": u.ReasoningTokens,
		}
	}
	return out
}

func toChatFinishReason(r string) string {
	switch r {
	case ir.FinishStop:
		return "stop"
	case ir.FinishLength:
		return "length"
	case ir.FinishToolCalls:
		return "tool_calls"
	case ir.FinishContentFilter:
		return "content_filter"
	default:
		return r
	}
}
