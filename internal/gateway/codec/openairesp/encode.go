package openairesp

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

type activeOutputKind int

const (
	outNone activeOutputKind = iota
	outThought
	outMessage
	outTool
)

type respEncoder struct {
	id          string
	model       string
	created     int64
	seq         int
	started     bool
	finished    bool
	outIdx      int
	activeKind  activeOutputKind
	activeID    string
	activeCall  string
	activeName  string
	textBuf     strings.Builder
	thoughtBuf  strings.Builder
	argsBuf     strings.Builder
	usage       ir.Usage
	outputItems []map[string]any
}

// NewEncoder creates an OpenAI Responses SSE StreamEncoder.
func (c *Codec) NewEncoder(model string) codec.StreamEncoder {
	return &respEncoder{
		id:      ir.NewID("resp_"),
		model:   model,
		created: time.Now().Unix(),
	}
}

// ResponseID returns the response ID emitted in response.created and response.completed events.
func (e *respEncoder) ResponseID() string {
	return e.id
}

func (e *respEncoder) nextSeq() int {
	s := e.seq
	e.seq++
	return s
}

func (e *respEncoder) emit(w *sse.Writer, evType string, payload map[string]any) error {
	payload["type"] = evType
	payload["sequence_number"] = e.nextSeq()
	return w.WriteJSON(evType, payload)
}

func (e *respEncoder) ensureStarted(w *sse.Writer) error {
	if e.started {
		return nil
	}
	e.started = true
	base := map[string]any{
		"id":         e.id,
		"object":     "response",
		"created_at": e.created,
		"model":      e.model,
		"status":     "in_progress",
		"output":     []any{},
	}
	if err := e.emit(w, "response.created", map[string]any{"response": base}); err != nil {
		return err
	}
	return e.emit(w, "response.in_progress", map[string]any{"response": base})
}

func (e *respEncoder) closeActive(w *sse.Writer) error {
	switch e.activeKind {
	case outThought:
		full := e.thoughtBuf.String()
		e.thoughtBuf.Reset()
		if err := e.emit(w, "response.reasoning_summary_text.done", map[string]any{
			"item_id":       e.activeID,
			"output_index":  e.outIdx,
			"summary_index": 0,
			"text":          full,
		}); err != nil {
			return err
		}
		part := map[string]any{"type": "summary_text", "text": full}
		if err := e.emit(w, "response.reasoning_summary_part.done", map[string]any{
			"item_id":       e.activeID,
			"output_index":  e.outIdx,
			"summary_index": 0,
			"part":          part,
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id":      e.activeID,
			"type":    "reasoning",
			"status":  "completed",
			"summary": []map[string]any{part},
		}
		e.outputItems = append(e.outputItems, item)
		e.outIdx++
		e.activeKind = outNone
		return e.emit(w, "response.output_item.done", map[string]any{
			"output_index": e.outIdx - 1,
			"item":         item,
		})

	case outMessage:
		full := e.textBuf.String()
		e.textBuf.Reset()
		if err := e.emit(w, "response.output_text.done", map[string]any{
			"item_id":       e.activeID,
			"output_index":  e.outIdx,
			"content_index": 0,
			"text":          full,
		}); err != nil {
			return err
		}
		part := map[string]any{"type": "output_text", "text": full, "annotations": []any{}}
		if err := e.emit(w, "response.content_part.done", map[string]any{
			"item_id":       e.activeID,
			"output_index":  e.outIdx,
			"content_index": 0,
			"part":          part,
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id":      e.activeID,
			"type":    "message",
			"role":    "assistant",
			"status":  "completed",
			"content": []map[string]any{part},
		}
		e.outputItems = append(e.outputItems, item)
		e.outIdx++
		e.activeKind = outNone
		return e.emit(w, "response.output_item.done", map[string]any{
			"output_index": e.outIdx - 1,
			"item":         item,
		})

	case outTool:
		args := ir.ArgsOrEmpty(e.argsBuf.String())
		e.argsBuf.Reset()
		if err := e.emit(w, "response.function_call_arguments.done", map[string]any{
			"item_id":      e.activeID,
			"output_index": e.outIdx,
			"arguments":    args,
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id":        e.activeID,
			"type":      "function_call",
			"call_id":   e.activeCall,
			"name":      e.activeName,
			"arguments": args,
			"status":    "completed",
		}
		e.outputItems = append(e.outputItems, item)
		e.outIdx++
		e.activeKind = outNone
		return e.emit(w, "response.output_item.done", map[string]any{
			"output_index": e.outIdx - 1,
			"item":         item,
		})
	}
	return nil
}

func (e *respEncoder) openThought(w *sse.Writer) error {
	if e.activeKind == outThought {
		return nil
	}
	if err := e.closeActive(w); err != nil {
		return err
	}
	e.activeKind = outThought
	e.activeID = ir.NewID("rs_")
	item := map[string]any{
		"id":      e.activeID,
		"type":    "reasoning",
		"status":  "in_progress",
		"summary": []any{},
	}
	if err := e.emit(w, "response.output_item.added", map[string]any{
		"output_index": e.outIdx,
		"item":         item,
	}); err != nil {
		return err
	}
	return e.emit(w, "response.reasoning_summary_part.added", map[string]any{
		"item_id":       e.activeID,
		"output_index":  e.outIdx,
		"summary_index": 0,
		"part":          map[string]any{"type": "summary_text", "text": ""},
	})
}

func (e *respEncoder) openMessage(w *sse.Writer) error {
	if e.activeKind == outMessage {
		return nil
	}
	if err := e.closeActive(w); err != nil {
		return err
	}
	e.activeKind = outMessage
	e.activeID = ir.NewID("msg_")
	item := map[string]any{
		"id":      e.activeID,
		"type":    "message",
		"role":    "assistant",
		"status":  "in_progress",
		"content": []any{},
	}
	if err := e.emit(w, "response.output_item.added", map[string]any{
		"output_index": e.outIdx,
		"item":         item,
	}); err != nil {
		return err
	}
	return e.emit(w, "response.content_part.added", map[string]any{
		"item_id":       e.activeID,
		"output_index":  e.outIdx,
		"content_index": 0,
		"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

func (e *respEncoder) Encode(ev ir.Event, w *sse.Writer) error {
	switch ev.Kind {
	case ir.EventStart:
		if ev.ResponseID != "" {
			e.id = ev.ResponseID
		}
		if ev.Model != "" {
			e.model = ev.Model
		}
		ir.MergeUsage(&e.usage, ev.Usage)
		return e.ensureStarted(w)

	case ir.EventUsage:
		ir.MergeUsage(&e.usage, ev.Usage)
		return nil

	case ir.EventThought:
		if ev.Text == "" {
			return nil
		}
		if err := e.ensureStarted(w); err != nil {
			return err
		}
		if err := e.openThought(w); err != nil {
			return err
		}
		e.thoughtBuf.WriteString(ev.Text)
		return e.emit(w, "response.reasoning_summary_text.delta", map[string]any{
			"item_id":       e.activeID,
			"output_index":  e.outIdx,
			"summary_index": 0,
			"delta":         ev.Text,
		})

	case ir.EventText:
		if ev.Text == "" {
			return nil
		}
		if err := e.ensureStarted(w); err != nil {
			return err
		}
		if err := e.openMessage(w); err != nil {
			return err
		}
		e.textBuf.WriteString(ev.Text)
		return e.emit(w, "response.output_text.delta", map[string]any{
			"item_id":       e.activeID,
			"output_index":  e.outIdx,
			"content_index": 0,
			"delta":         ev.Text,
		})

	case ir.EventToolCallStart:
		if err := e.ensureStarted(w); err != nil {
			return err
		}
		if err := e.closeActive(w); err != nil {
			return err
		}
		e.activeKind = outTool
		e.activeID = ir.NewID("fc_")
		e.activeCall = ev.CallID
		if e.activeCall == "" {
			e.activeCall = ir.NewID("call_")
		}
		e.activeName = ev.Name
		item := map[string]any{
			"id":        e.activeID,
			"type":      "function_call",
			"call_id":   e.activeCall,
			"name":      e.activeName,
			"arguments": "",
			"status":    "in_progress",
		}
		if err := e.emit(w, "response.output_item.added", map[string]any{
			"output_index": e.outIdx,
			"item":         item,
		}); err != nil {
			return err
		}
		if ev.Args != "" {
			e.argsBuf.WriteString(ev.Args)
			return e.emit(w, "response.function_call_arguments.delta", map[string]any{
				"item_id":      e.activeID,
				"output_index": e.outIdx,
				"delta":        ev.Args,
			})
		}
		return nil

	case ir.EventToolCallDelta:
		if ev.Args == "" {
			return nil
		}
		if e.activeKind != outTool {
			return e.Encode(ir.Event{
				Kind:   ir.EventToolCallStart,
				CallID: ev.CallID,
				Name:   ev.Name,
				Args:   ev.Args,
			}, w)
		}
		e.argsBuf.WriteString(ev.Args)
		return e.emit(w, "response.function_call_arguments.delta", map[string]any{
			"item_id":      e.activeID,
			"output_index": e.outIdx,
			"delta":        ev.Args,
		})

	case ir.EventToolCallDone:
		if e.activeKind == outTool {
			return e.closeActive(w)
		}
		return nil

	case ir.EventFinish:
		ir.MergeUsage(&e.usage, ev.Usage)
		return e.finishWith(w, ev.FinishReason, e.usage)

	case ir.EventError:
		if ev.Err == nil {
			return nil
		}
		return e.emit(w, "error", map[string]any{
			"error": map[string]any{
				"type":    ev.Err.Type,
				"code":    ev.Err.Code,
				"message": ev.Err.Message,
			},
		})
	}
	return nil
}

func (e *respEncoder) Finish(w *sse.Writer) error {
	if e.finished {
		return nil
	}
	return e.finishWith(w, ir.FinishStop, e.usage)
}

func (e *respEncoder) finishWith(w *sse.Writer, reason string, u ir.Usage) error {
	if e.finished {
		return nil
	}
	e.finished = true
	if err := e.ensureStarted(w); err != nil {
		return err
	}
	if err := e.closeActive(w); err != nil {
		return err
	}

	status := "completed"
	evName := "response.completed"
	var incompleteDetails map[string]any
	if reason == ir.FinishLength {
		status = "incomplete"
		evName = "response.incomplete"
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	} else if reason == ir.FinishContentFilter {
		status = "incomplete"
		evName = "response.incomplete"
		incompleteDetails = map[string]any{"reason": "content_filter"}
	}

	respObj := map[string]any{
		"id":         e.id,
		"object":     "response",
		"created_at": e.created,
		"model":      e.model,
		"status":     status,
		"output":     e.outputItems,
		"usage":      encodeRespUsage(u),
	}
	if incompleteDetails != nil {
		respObj["incomplete_details"] = incompleteDetails
	}
	return e.emit(w, evName, map[string]any{"response": respObj})
}

// EncodeResult renders an aggregated IR Result into a non-streaming OpenAI Responses JSON payload.
func (c *Codec) EncodeResult(res *ir.Result) ([]byte, *ir.GatewayError) {
	if res == nil {
		return nil, ir.NewError(500, "empty_result", "nil result")
	}
	id := res.ID
	if id == "" {
		id = ir.NewID("resp_")
	}

	var output []map[string]any
	var textBuf strings.Builder
	var thoughtBuf strings.Builder

	flushText := func() {
		if textBuf.Len() == 0 {
			return
		}
		output = append(output, map[string]any{
			"id":     ir.NewID("msg_"),
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        textBuf.String(),
				"annotations": []any{},
			}},
		})
		textBuf.Reset()
	}

	flushThought := func() {
		if thoughtBuf.Len() == 0 {
			return
		}
		output = append(output, map[string]any{
			"id":     ir.NewID("rs_"),
			"type":   "reasoning",
			"status": "completed",
			"summary": []map[string]any{{
				"type": "summary_text",
				"text": thoughtBuf.String(),
			}},
		})
		thoughtBuf.Reset()
	}

	for _, p := range res.Content {
		switch p.Kind {
		case ir.KindThought:
			flushText()
			thoughtBuf.WriteString(p.Text)
		case ir.KindText, ir.KindRefusal, "":
			flushThought()
			textBuf.WriteString(p.Text)
		case ir.KindToolCall:
			flushThought()
			flushText()
			cid := p.CallID
			if cid == "" {
				cid = ir.NewID("call_")
			}
			output = append(output, map[string]any{
				"id":        ir.NewID("fc_"),
				"type":      "function_call",
				"call_id":   cid,
				"name":      p.Name,
				"arguments": ir.ArgsOrEmpty(p.Args),
				"status":    "completed",
			})
		}
	}
	flushThought()
	flushText()

	status := "completed"
	var incompleteDetails map[string]any
	if res.FinishReason == ir.FinishLength {
		status = "incomplete"
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	} else if res.FinishReason == ir.FinishContentFilter {
		status = "incomplete"
		incompleteDetails = map[string]any{"reason": "content_filter"}
	}

	resp := map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      res.Model,
		"status":     status,
		"output":     output,
		"usage":      encodeRespUsage(res.Usage),
	}
	if incompleteDetails != nil {
		resp["incomplete_details"] = incompleteDetails
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, ir.NewError(500, "marshal_error", err.Error())
	}
	return raw, nil
}

// EncodeError renders a GatewayError in OpenAI's JSON error envelope.
func (c *Codec) EncodeError(err *ir.GatewayError) []byte {
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

func encodeRespUsage(u ir.Usage) map[string]any {
	out := map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
		"total_tokens":  u.TotalTokens(),
	}
	if u.CacheReadTokens > 0 {
		out["input_tokens_details"] = map[string]any{
			"cached_tokens": u.CacheReadTokens,
		}
	}
	if u.ReasoningTokens > 0 {
		out["output_tokens_details"] = map[string]any{
			"reasoning_tokens": u.ReasoningTokens,
		}
	}
	return out
}
