package anthropic

import (
	"encoding/json"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

type activeAnthBlock int

const (
	blockNone activeAnthBlock = iota
	blockThinking
	blockText
	blockTool
)

type anthEncoder struct {
	id           string
	model        string
	started      bool
	finished     bool
	blockIdx     int
	activeBlock  activeAnthBlock
	activeCallID string
	activeTool   string
	sawToolUse   bool
	usage        ir.Usage
	lastPing     time.Time
}

// NewEncoder creates an Anthropic Messages SSE StreamEncoder.
func (c *Codec) NewEncoder(model string) codec.StreamEncoder {
	return &anthEncoder{
		id:    ir.NewID("msg_"),
		model: model,
	}
}

func (e *anthEncoder) ensureStarted(w *sse.Writer, u ir.Usage) error {
	if e.started {
		return nil
	}
	e.started = true
	e.lastPing = time.Now()
	msg := map[string]any{
		"id":            e.id,
		"type":          "message",
		"role":          "assistant",
		"model":         e.model,
		"content":       []any{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":                u.UncachedInputTokens(),
			"output_tokens":               0,
			"cache_read_input_tokens":     u.CacheReadTokens,
			"cache_creation_input_tokens": u.CacheWriteTokens,
		},
	}
	if err := w.WriteJSON("message_start", map[string]any{
		"type":    "message_start",
		"message": msg,
	}); err != nil {
		return err
	}
	return w.WriteJSON("ping", map[string]any{"type": "ping"})
}

func (e *anthEncoder) closeBlock(w *sse.Writer) error {
	if e.activeBlock == blockNone {
		return nil
	}
	idx := e.blockIdx
	e.blockIdx++
	e.activeBlock = blockNone
	return w.WriteJSON("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": idx,
	})
}

func (e *anthEncoder) maybePing(w *sse.Writer) error {
	if time.Since(e.lastPing) < 15*time.Second {
		return nil
	}
	e.lastPing = time.Now()
	return w.WriteJSON("ping", map[string]any{"type": "ping"})
}

func (e *anthEncoder) Encode(ev ir.Event, w *sse.Writer) error {
	switch ev.Kind {
	case ir.EventStart:
		if ev.ResponseID != "" {
			e.id = ev.ResponseID
		}
		if ev.Model != "" {
			e.model = ev.Model
		}
		ir.MergeUsage(&e.usage, ev.Usage)
		return e.ensureStarted(w, e.usage)

	case ir.EventUsage:
		ir.MergeUsage(&e.usage, ev.Usage)
		return nil

	case ir.EventThought:
		if ev.Text == "" && ev.Signature == "" {
			return nil
		}
		if err := e.ensureStarted(w, ir.Usage{}); err != nil {
			return err
		}
		if e.activeBlock != blockThinking {
			if err := e.closeBlock(w); err != nil {
				return err
			}
			e.activeBlock = blockThinking
			if err := w.WriteJSON("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": e.blockIdx,
				"content_block": map[string]any{
					"type":     "thinking",
					"thinking": "",
				},
			}); err != nil {
				return err
			}
		}
		if ev.Text != "" {
			if err := w.WriteJSON("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": e.blockIdx,
				"delta": map[string]any{
					"type":     "thinking_delta",
					"thinking": ev.Text,
				},
			}); err != nil {
				return err
			}
		}
		if ev.Signature != "" {
			return w.WriteJSON("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": e.blockIdx,
				"delta": map[string]any{
					"type":      "signature_delta",
					"signature": ev.Signature,
				},
			})
		}
		return e.maybePing(w)

	case ir.EventThoughtSignature:
		if ev.Signature == "" || e.activeBlock != blockThinking {
			return nil
		}
		return w.WriteJSON("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.blockIdx,
			"delta": map[string]any{
				"type":      "signature_delta",
				"signature": ev.Signature,
			},
		})

	case ir.EventText:
		if ev.Text == "" {
			return nil
		}
		if err := e.ensureStarted(w, ir.Usage{}); err != nil {
			return err
		}
		if e.activeBlock != blockText {
			if err := e.closeBlock(w); err != nil {
				return err
			}
			e.activeBlock = blockText
			if err := w.WriteJSON("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": e.blockIdx,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			}); err != nil {
				return err
			}
		}
		if err := w.WriteJSON("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.blockIdx,
			"delta": map[string]any{
				"type": "text_delta",
				"text": ev.Text,
			},
		}); err != nil {
			return err
		}
		return e.maybePing(w)

	case ir.EventToolCallStart:
		if err := e.ensureStarted(w, ir.Usage{}); err != nil {
			return err
		}
		if err := e.closeBlock(w); err != nil {
			return err
		}
		e.activeBlock = blockTool
		e.sawToolUse = true
		e.activeCallID = ir.SanitizeToolID(ev.CallID)
		e.activeTool = ev.Name
		if err := w.WriteJSON("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": e.blockIdx,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    e.activeCallID,
				"name":  e.activeTool,
				"input": map[string]any{},
			},
		}); err != nil {
			return err
		}
		if ev.Args != "" {
			return w.WriteJSON("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": e.blockIdx,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": ev.Args,
				},
			})
		}
		return nil

	case ir.EventToolCallDelta:
		if ev.Args == "" {
			return nil
		}
		if e.activeBlock != blockTool {
			return e.Encode(ir.Event{
				Kind:   ir.EventToolCallStart,
				CallID: ev.CallID,
				Name:   ev.Name,
				Args:   ev.Args,
			}, w)
		}
		return w.WriteJSON("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.blockIdx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": ev.Args,
			},
		})

	case ir.EventToolCallDone:
		if e.activeBlock == blockTool {
			return e.closeBlock(w)
		}
		return nil

	case ir.EventFinish:
		ir.MergeUsage(&e.usage, ev.Usage)
		return e.finishWith(w, ev.FinishReason, e.usage)

	case ir.EventError:
		if ev.Err == nil {
			return nil
		}
		return w.WriteEvent("error", encodeAnthError(ev.Err))
	}
	return nil
}

func (e *anthEncoder) Finish(w *sse.Writer) error {
	if e.finished {
		return nil
	}
	reason := ir.FinishStop
	if e.sawToolUse {
		reason = ir.FinishToolCalls
	}
	return e.finishWith(w, reason, e.usage)
}

func (e *anthEncoder) finishWith(w *sse.Writer, reason string, u ir.Usage) error {
	if e.finished {
		return nil
	}
	e.finished = true
	if err := e.ensureStarted(w, u); err != nil {
		return err
	}
	if err := e.closeBlock(w); err != nil {
		return err
	}

	stopReason := toAnthStopReason(reason, e.sawToolUse)
	if err := w.WriteJSON("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"input_tokens":                u.UncachedInputTokens(),
			"output_tokens":               u.OutputTokens,
			"cache_read_input_tokens":     u.CacheReadTokens,
			"cache_creation_input_tokens": u.CacheWriteTokens,
		},
	}); err != nil {
		return err
	}
	return w.WriteJSON("message_stop", map[string]any{"type": "message_stop"})
}

// EncodeResult renders an aggregated IR Result into a non-streaming Anthropic Messages JSON response.
func (c *Codec) EncodeResult(res *ir.Result) ([]byte, *ir.GatewayError) {
	if res == nil {
		return nil, ir.NewError(500, "empty_result", "nil result")
	}
	id := res.ID
	if id == "" {
		id = ir.NewID("msg_")
	}

	var content []any
	hasToolUse := false
	for _, p := range res.Content {
		switch p.Kind {
		case ir.KindThought:
			block := map[string]any{
				"type":     "thinking",
				"thinking": p.Text,
			}
			if p.Signature != "" {
				block["signature"] = p.Signature
			}
			content = append(content, block)
		case ir.KindText, ir.KindRefusal, "":
			content = append(content, map[string]any{
				"type": "text",
				"text": p.Text,
			})
		case ir.KindToolCall:
			hasToolUse = true
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    ir.SanitizeToolID(p.CallID),
				"name":  p.Name,
				"input": ir.ParseArgs(p.Args),
			})
		}
	}
	if len(content) == 0 {
		content = []any{map[string]any{"type": "text", "text": ""}}
	}

	resp := map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         res.Model,
		"content":       content,
		"stop_reason":   toAnthStopReason(res.FinishReason, hasToolUse),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":                res.Usage.UncachedInputTokens(),
			"output_tokens":               res.Usage.OutputTokens,
			"cache_read_input_tokens":     res.Usage.CacheReadTokens,
			"cache_creation_input_tokens": res.Usage.CacheWriteTokens,
		},
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, ir.NewError(500, "marshal_error", err.Error())
	}
	return raw, nil
}

// EncodeError renders a GatewayError in Anthropic's JSON error envelope.
func (c *Codec) EncodeError(err *ir.GatewayError) []byte {
	return encodeAnthError(err)
}

func encodeAnthError(err *ir.GatewayError) []byte {
	if err == nil {
		err = ir.NewError(500, "api_error", "unknown error")
	}
	errType := err.Type
	if errType == "" {
		errType = "api_error"
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": err.Message,
		},
	})
	return raw
}

func toAnthStopReason(reason string, hasToolUse bool) string {
	if hasToolUse && (reason == ir.FinishStop || reason == "") {
		return "tool_use"
	}
	if !hasToolUse && reason == ir.FinishToolCalls {
		return "end_turn"
	}
	switch reason {
	case ir.FinishToolCalls:
		return "tool_use"
	case ir.FinishLength:
		return "max_tokens"
	case ir.FinishContentFilter:
		return "refusal"
	case ir.FinishStop:
		return "end_turn"
	default:
		if hasToolUse {
			return "tool_use"
		}
		return "end_turn"
	}
}
