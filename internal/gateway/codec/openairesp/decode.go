package openairesp

import (
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

type respStreamEnvelope struct {
	Type     string `json:"type"`
	ItemID   string `json:"item_id"`
	CallID   string `json:"call_id"`
	Delta    string `json:"delta"`
	Arguments string `json:"arguments"`
	Item     *struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response *struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Status string `json:"status"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage *respUsage `json:"usage"`
	} `json:"response"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type respToolItem struct {
	itemID   string
	callID   string
	name     string
	argsBuf  strings.Builder
	started  bool
	done     bool
}

type respDecoder struct {
	sentStart   bool
	sentFinish  bool
	respID      string
	model       string
	usage       ir.Usage
	tools       map[string]*respToolItem
	waiting     []string // ordered itemIDs for parallel function_call items
	activeItem  string
	hasToolCall bool
}

// NewDecoder returns an OpenAI Responses SSE StreamDecoder.
func (c *Codec) NewDecoder(_ codec.Quirks) codec.StreamDecoder {
	return &respDecoder{
		tools: make(map[string]*respToolItem),
	}
}

func (d *respDecoder) DecodeFrame(frame sse.Event) ([]ir.Event, bool, *ir.GatewayError) {
	if len(frame.Data) == 0 {
		return nil, false, nil
	}
	if sse.IsDone(frame.Data) {
		return d.Flush(), true, nil
	}

	var env respStreamEnvelope
	if err := json.Unmarshal(frame.Data, &env); err != nil {
		return nil, false, nil
	}
	evType := env.Type
	if evType == "" {
		evType = frame.Type
	}

	var out []ir.Event
	switch evType {
	case "response.created", "response.in_progress":
		if env.Response != nil {
			if env.Response.ID != "" {
				d.respID = env.Response.ID
			}
			if env.Response.Model != "" {
				d.model = env.Response.Model
			}
		}
		if !d.sentStart {
			d.sentStart = true
			out = append(out, ir.Event{
				Kind:       ir.EventStart,
				ResponseID: d.respID,
				Model:      d.model,
			})
		}

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if env.Delta != "" {
			out = append(out, ir.Event{Kind: ir.EventThought, Text: env.Delta})
		}

	case "response.output_text.delta":
		if !d.sentStart {
			d.sentStart = true
			out = append(out, ir.Event{Kind: ir.EventStart, ResponseID: d.respID, Model: d.model})
		}
		if env.Delta != "" {
			out = append(out, ir.Event{Kind: ir.EventText, Text: env.Delta})
		}

	case "response.output_item.added":
		if env.Item != nil && env.Item.Type == "function_call" {
			d.hasToolCall = true
			key := env.Item.ID
			if key == "" {
				key = env.Item.CallID
			}
			callID := env.Item.CallID
			if callID == "" {
				callID = key
			}
			if callID == "" {
				callID = ir.NewID("call_")
			}
			st := &respToolItem{
				itemID: key,
				callID: callID,
				name:   env.Item.Name,
			}
			if env.Item.Arguments != "" {
				st.argsBuf.WriteString(env.Item.Arguments)
			}
			d.tools[key] = st
			if d.activeItem == "" {
				d.activeItem = key
				st.started = true
				out = append(out, ir.Event{
					Kind:   ir.EventToolCallStart,
					CallID: st.callID,
					Name:   st.name,
				})
				if st.argsBuf.Len() > 0 {
					out = append(out, ir.Event{
						Kind:   ir.EventToolCallDelta,
						CallID: st.callID,
						Args:   st.argsBuf.String(),
					})
					st.argsBuf.Reset()
				}
			} else {
				d.waiting = append(d.waiting, key)
			}
		}

	case "response.function_call_arguments.delta":
		key := env.ItemID
		if key == "" {
			key = env.CallID
		}
		st := d.tools[key]
		if st == nil {
			break
		}
		if d.activeItem == key {
			if env.Delta != "" {
				out = append(out, ir.Event{
					Kind:   ir.EventToolCallDelta,
					CallID: st.callID,
					Args:   env.Delta,
				})
			}
		} else {
			st.argsBuf.WriteString(env.Delta)
		}

	case "response.function_call_arguments.done", "response.output_item.done":
		key := env.ItemID
		var fallbackArgs string
		if env.Item != nil {
			if env.Item.Type != "" && env.Item.Type != "function_call" {
				break
			}
			if key == "" {
				key = env.Item.ID
			}
			fallbackArgs = env.Item.Arguments
		} else if evType == "response.function_call_arguments.done" {
			fallbackArgs = env.Arguments
		}
		st := d.tools[key]
		if st == nil || st.done {
			break
		}
		if st.argsBuf.Len() == 0 && !st.started && fallbackArgs != "" {
			st.argsBuf.WriteString(fallbackArgs)
		}
		st.done = true
		if d.activeItem == key {
			if st.argsBuf.Len() > 0 {
				out = append(out, ir.Event{
					Kind:   ir.EventToolCallDelta,
					CallID: st.callID,
					Args:   st.argsBuf.String(),
				})
				st.argsBuf.Reset()
			}
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallDone,
				CallID: st.callID,
				Name:   st.name,
			})
			d.activeItem = ""
			out = append(out, d.promoteWaiting()...)
		}

	case "response.completed", "response.incomplete":
		var reason string
		if env.Response != nil {
			if env.Response.Usage != nil {
				d.usage = parseRespUsage(env.Response.Usage)
			}
			if env.Response.IncompleteDetails != nil && env.Response.IncompleteDetails.Reason != "" {
				switch env.Response.IncompleteDetails.Reason {
				case "max_output_tokens":
					reason = ir.FinishLength
				case "content_filter":
					reason = ir.FinishContentFilter
				}
			}
		}
		out = append(out, d.flushPendingTools()...)
		if reason == "" {
			if d.hasToolCall {
				reason = ir.FinishToolCalls
			} else {
				reason = ir.FinishStop
			}
		}
		d.sentFinish = true
		out = append(out, ir.Event{
			Kind:         ir.EventFinish,
			FinishReason: reason,
			Usage:        d.usage,
		})
		return out, true, nil

	case "response.failed":
		msg := "response failed"
		code := "response_failed"
		if env.Response != nil && env.Response.Error != nil {
			if env.Response.Error.Message != "" {
				msg = env.Response.Error.Message
			}
			if env.Response.Error.Code != "" {
				code = env.Response.Error.Code
			}
		}
		return nil, true, ir.NewError(502, code, msg)

	case "error":
		msg := "upstream error"
		code := "upstream_error"
		if env.Error != nil {
			if env.Error.Message != "" {
				msg = env.Error.Message
			}
			if env.Error.Code != "" {
				code = env.Error.Code
			}
		}
		return nil, true, ir.NewError(502, code, msg)
	}

	return out, false, nil
}

func (d *respDecoder) Flush() []ir.Event {
	var out []ir.Event
	out = append(out, d.flushPendingTools()...)
	if !d.sentFinish {
		d.sentFinish = true
		reason := ir.FinishStop
		if d.hasToolCall {
			reason = ir.FinishToolCalls
		}
		out = append(out, ir.Event{
			Kind:         ir.EventFinish,
			FinishReason: reason,
			Usage:        d.usage,
		})
	}
	return out
}

func (d *respDecoder) promoteWaiting() []ir.Event {
	var out []ir.Event
	for len(d.waiting) > 0 {
		nextKey := d.waiting[0]
		d.waiting = d.waiting[1:]
		st := d.tools[nextKey]
		if st == nil {
			continue
		}
		if !st.started {
			st.started = true
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallStart,
				CallID: st.callID,
				Name:   st.name,
			})
		}
		if st.argsBuf.Len() > 0 {
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallDelta,
				CallID: st.callID,
				Args:   st.argsBuf.String(),
			})
			st.argsBuf.Reset()
		}
		if st.done {
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallDone,
				CallID: st.callID,
				Name:   st.name,
			})
			continue
		}
		d.activeItem = nextKey
		break
	}
	return out
}

func (d *respDecoder) flushPendingTools() []ir.Event {
	var out []ir.Event
	if d.activeItem != "" {
		if st := d.tools[d.activeItem]; st != nil {
			if st.argsBuf.Len() > 0 {
				out = append(out, ir.Event{
					Kind:   ir.EventToolCallDelta,
					CallID: st.callID,
					Args:   st.argsBuf.String(),
				})
				st.argsBuf.Reset()
			}
			if !st.done {
				st.done = true
				out = append(out, ir.Event{
					Kind:   ir.EventToolCallDone,
					CallID: st.callID,
					Name:   st.name,
				})
			}
		}
		d.activeItem = ""
	}
	for _, key := range d.waiting {
		st := d.tools[key]
		if st == nil {
			continue
		}
		if !st.started {
			st.started = true
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallStart,
				CallID: st.callID,
				Name:   st.name,
			})
		}
		if st.argsBuf.Len() > 0 {
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallDelta,
				CallID: st.callID,
				Args:   st.argsBuf.String(),
			})
			st.argsBuf.Reset()
		}
		if !st.done {
			st.done = true
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallDone,
				CallID: st.callID,
				Name:   st.name,
			})
		}
	}
	d.waiting = d.waiting[:0]
	return out
}

func parseRespUsage(u *respUsage) ir.Usage {
	if u == nil {
		return ir.Usage{}
	}
	out := ir.Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
	}
	if u.InputTokensDetails != nil {
		out.CacheReadTokens = u.InputTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return out
}
