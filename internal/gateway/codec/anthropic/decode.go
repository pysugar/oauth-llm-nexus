package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

type anthStreamEnvelope struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID    string     `json:"id"`
		Model string     `json:"model"`
		Usage *anthUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
		Input     json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthBlockState struct {
	kind       string
	callID     string
	name       string
	argsSent   bool
	thoughtBuf strings.Builder
	signature  string
}

type anthDecoder struct {
	sentStart    bool
	sentFinish   bool
	respID       string
	model        string
	finishReason string
	usage        ir.Usage
	blocks       map[int]*anthBlockState
}

// NewDecoder creates an Anthropic Messages SSE StreamDecoder.
func (c *Codec) NewDecoder(_ codec.Quirks) codec.StreamDecoder {
	return &anthDecoder{
		blocks: make(map[int]*anthBlockState),
	}
}

func (d *anthDecoder) DecodeFrame(frame sse.Event) ([]ir.Event, bool, *ir.GatewayError) {
	if len(frame.Data) == 0 {
		return nil, false, nil
	}

	var env anthStreamEnvelope
	if err := json.Unmarshal(frame.Data, &env); err != nil {
		return nil, false, nil
	}
	evType := env.Type
	if evType == "" {
		evType = frame.Type
	}

	var out []ir.Event
	switch evType {
	case "ping":
		return nil, false, nil

	case "message_start":
		if env.Message != nil {
			d.respID = env.Message.ID
			d.model = env.Message.Model
			if env.Message.Usage != nil {
				ir.MergeUsage(&d.usage, parseAnthUsage(env.Message.Usage))
			}
		}
		if !d.sentStart {
			d.sentStart = true
			out = append(out, ir.Event{
				Kind:       ir.EventStart,
				ResponseID: d.respID,
				Model:      d.model,
				Usage:      d.usage,
			})
		}

	case "content_block_start":
		if env.ContentBlock == nil {
			break
		}
		cb := env.ContentBlock
		st := &anthBlockState{kind: cb.Type}
		switch cb.Type {
		case "thinking":
			if cb.Signature != "" {
				st.signature = cb.Signature
			}
			if cb.Thinking != "" || cb.Signature != "" {
				st.thoughtBuf.WriteString(cb.Thinking)
				out = append(out, ir.Event{Kind: ir.EventThought, Text: cb.Thinking, Signature: cb.Signature})
			}
		case "redacted_thinking":
			if cb.Data != "" {
				st.signature = cb.Data
				out = append(out, ir.Event{Kind: ir.EventThoughtSignature, Signature: cb.Data})
			}
		case "text":
			if cb.Text != "" {
				out = append(out, ir.Event{Kind: ir.EventText, Text: cb.Text})
			}
		case "tool_use", "server_tool_use":
			callID := ir.UnsanitizeToolID(cb.ID)
			if callID == "" {
				callID = ir.NewID("call_")
			}
			st.callID = callID
			st.name = cb.Name
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallStart,
				CallID: callID,
				Name:   cb.Name,
			})
			// Some Anthropic relays populate non-empty input directly inside content_block_start.
			trimmedInput := strings.TrimSpace(string(cb.Input))
			if trimmedInput != "" && trimmedInput != "{}" && trimmedInput != "null" {
				st.argsSent = true
				out = append(out, ir.Event{
					Kind:   ir.EventToolCallDelta,
					CallID: callID,
					Args:   trimmedInput,
				})
			}
		}
		d.blocks[env.Index] = st

	case "content_block_delta":
		if env.Delta == nil {
			break
		}
		st := d.blocks[env.Index]
		switch env.Delta.Type {
		case "text_delta":
			if env.Delta.Text != "" {
				out = append(out, ir.Event{Kind: ir.EventText, Text: env.Delta.Text})
			}
		case "thinking_delta":
			if env.Delta.Thinking != "" {
				if st != nil {
					st.thoughtBuf.WriteString(env.Delta.Thinking)
				}
				out = append(out, ir.Event{Kind: ir.EventThought, Text: env.Delta.Thinking})
			}
		case "signature_delta":
			if env.Delta.Signature != "" {
				if st != nil {
					st.signature += env.Delta.Signature
				}
				out = append(out, ir.Event{Kind: ir.EventThoughtSignature, Signature: env.Delta.Signature})
			}
		case "input_json_delta":
			if env.Delta.PartialJSON != "" && st != nil {
				st.argsSent = true
				out = append(out, ir.Event{
					Kind:   ir.EventToolCallDelta,
					CallID: st.callID,
					Args:   env.Delta.PartialJSON,
				})
			}
		}

	case "content_block_stop":
		st := d.blocks[env.Index]
		if st == nil {
			break
		}
		if st.kind == "thinking" && st.signature != "" && st.thoughtBuf.Len() > 0 {
			ir.DefaultSigStore.SaveThought(st.thoughtBuf.String(), st.signature)
		}
		if st.kind == "tool_use" || st.kind == "server_tool_use" {
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallDone,
				CallID: st.callID,
				Name:   st.name,
			})
		}
		delete(d.blocks, env.Index)

	case "message_delta":
		if env.Delta != nil && env.Delta.StopReason != "" {
			d.finishReason = mapAnthStopReason(env.Delta.StopReason)
		}
		if env.Usage != nil {
			u := parseAnthUsage(env.Usage)
			ir.MergeUsage(&d.usage, u)
			out = append(out, ir.Event{Kind: ir.EventUsage, Usage: d.usage})
		}

	case "message_stop":
		out = append(out, d.Flush()...)
		return out, true, nil

	case "error":
		msg := "Anthropic stream error"
		errType := "api_error"
		if env.Error != nil {
			if env.Error.Message != "" {
				msg = env.Error.Message
			}
			if env.Error.Type != "" {
				errType = env.Error.Type
			}
		}
		ge := ir.NewError(502, errType, msg)
		ge.Type = errType
		return nil, true, ge
	}

	return out, false, nil
}

func (d *anthDecoder) Flush() []ir.Event {
	var out []ir.Event
	for idx, st := range d.blocks {
		if st.kind == "tool_use" || st.kind == "server_tool_use" {
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallDone,
				CallID: st.callID,
				Name:   st.name,
			})
		}
		delete(d.blocks, idx)
	}
	if !d.sentFinish {
		d.sentFinish = true
		reason := d.finishReason
		if reason == "" {
			reason = ir.FinishStop
		}
		out = append(out, ir.Event{
			Kind:         ir.EventFinish,
			FinishReason: reason,
			Usage:        d.usage,
		})
	}
	return out
}

func parseAnthUsage(u *anthUsage) ir.Usage {
	if u == nil {
		return ir.Usage{}
	}
	return ir.Usage{
		InputTokens:      u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

func mapAnthStopReason(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "end_turn", "stop_sequence", "pause_turn":
		return ir.FinishStop
	case "max_tokens", "model_context_window_exceeded":
		return ir.FinishLength
	case "tool_use":
		return ir.FinishToolCalls
	case "refusal":
		return ir.FinishContentFilter
	default:
		return ir.FinishStop
	}
}
