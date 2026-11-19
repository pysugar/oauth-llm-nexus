package openaichat

import (
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

type chatChunk struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   *chatUsage        `json:"usage"`
	Error   *json.RawMessage  `json:"error"`
}

type chatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        chatChunkDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type chatChunkDelta struct {
	Role             string          `json:"role"`
	Content          *string         `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	Reasoning        *string         `json:"reasoning"`
	Refusal          *string         `json:"refusal"`
	ToolCalls        []chatInToolRef `json:"tool_calls"`
}

type chatUsage struct {
	PromptTokens         int `json:"prompt_tokens"`
	CompletionTokens     int `json:"completion_tokens"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type activeToolState struct {
	callID   string
	name     string
	argsBuf  strings.Builder
	started  bool
	finished bool
}

// chatDecoder translates OpenAI Chat SSE chunks into IR events, serializing parallel
// interleaved tool calls and extracting optional <think> blocks.
type chatDecoder struct {
	quirks       codec.Quirks
	sentStart    bool
	respID       string
	model        string
	finishReason string
	sawDone      bool
	usage        ir.Usage
	sentFinish   bool

	// Parallel tool call serializer: openIdx holds currently streaming tool call index,
	// pendingByIdx buffers out-of-order/concurrent tool call indices until openIdx finishes.
	tools           map[int]*activeToolState
	toolOrder       []int
	openToolIdx     int
	hasOpenTool     bool
	nextToolSlot    int
	lastNoIndexSlot int
	inThinkTag      bool
	tagTailBuf      string
}

// NewDecoder creates an OpenAI Chat SSE StreamDecoder.
func (c *Codec) NewDecoder(q codec.Quirks) codec.StreamDecoder {
	return &chatDecoder{
		quirks:          q,
		tools:           make(map[int]*activeToolState),
		openToolIdx:     -1,
		lastNoIndexSlot: -1,
	}
}

// Complete implements codec.StreamDecoder.
func (d *chatDecoder) Complete() bool { return d.sawDone || d.finishReason != "" }

func (d *chatDecoder) DecodeFrame(frame sse.Event) ([]ir.Event, bool, *ir.GatewayError) {
	if len(frame.Data) == 0 {
		return nil, false, nil
	}
	if sse.IsDone(frame.Data) {
		d.sawDone = true
		return d.Flush(), true, nil
	}

	// Quick check for mid-stream error envelope.
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(frame.Data, &probe); err == nil && len(probe.Error) > 0 && string(probe.Error) != "null" {
		return nil, true, codec.ParseUpstreamError(502, frame.Data)
	}

	var chunk chatChunk
	if err := json.Unmarshal(frame.Data, &chunk); err != nil {
		return nil, false, nil
	}

	var out []ir.Event
	if chunk.ID != "" {
		d.respID = chunk.ID
	}
	if chunk.Model != "" {
		d.model = chunk.Model
	}
	if chunk.Usage != nil {
		u := parseChatUsage(chunk.Usage)
		ir.MergeUsage(&d.usage, u)
		if !d.sentStart && (chunk.ID != "" || chunk.Model != "") {
			d.sentStart = true
			out = append(out, ir.Event{
				Kind:       ir.EventStart,
				ResponseID: d.respID,
				Model:      d.model,
				Usage:      d.usage,
			})
		} else {
			out = append(out, ir.Event{Kind: ir.EventUsage, Usage: u})
		}
	}

	if !d.sentStart && (chunk.ID != "" || chunk.Model != "" || len(chunk.Choices) > 0) {
		d.sentStart = true
		out = append(out, ir.Event{
			Kind:       ir.EventStart,
			ResponseID: d.respID,
			Model:      d.model,
		})
	}

	for _, ch := range chunk.Choices {
		if ch.Index != 0 {
			continue
		}
		delta := ch.Delta
		if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
			out = append(out, ir.Event{Kind: ir.EventThought, Text: *delta.ReasoningContent})
		} else if delta.Reasoning != nil && *delta.Reasoning != "" {
			out = append(out, ir.Event{Kind: ir.EventThought, Text: *delta.Reasoning})
		}

		if delta.Content != nil && *delta.Content != "" {
			out = append(out, d.emitContent(*delta.Content)...)
		}
		if delta.Refusal != nil && *delta.Refusal != "" {
			out = append(out, ir.Event{Kind: ir.EventText, Text: *delta.Refusal})
		}

		for _, tc := range delta.ToolCalls {
			idx := d.resolveToolIndex(tc)
			out = append(out, d.handleToolDelta(idx, tc)...)
		}

		if ch.FinishReason != nil && *ch.FinishReason != "" {
			d.finishReason = mapChatFinishReason(*ch.FinishReason)
		}
	}

	return out, false, nil
}

func (d *chatDecoder) Flush() []ir.Event {
	var out []ir.Event
	if !d.sentStart && (d.respID != "" || d.model != "") {
		d.sentStart = true
		out = append(out, ir.Event{
			Kind:       ir.EventStart,
			ResponseID: d.respID,
			Model:      d.model,
		})
	}
	if d.tagTailBuf != "" {
		kind := ir.EventText
		if d.inThinkTag {
			kind = ir.EventThought
		}
		out = append(out, ir.Event{Kind: kind, Text: d.tagTailBuf})
		d.tagTailBuf = ""
	}
	out = append(out, d.flushAllTools()...)
	if !d.sentFinish {
		d.sentFinish = true
		reason := d.finishReason
		if reason == "" {
			if len(d.toolOrder) > 0 {
				reason = ir.FinishToolCalls
			} else {
				reason = ir.FinishStop
			}
		}
		out = append(out, ir.Event{
			Kind:         ir.EventFinish,
			FinishReason: reason,
			Usage:        d.usage,
		})
	}
	return out
}

func (d *chatDecoder) resolveToolIndex(tc chatInToolRef) int {
	if tc.Index != nil {
		idx := *tc.Index
		if idx >= d.nextToolSlot {
			d.nextToolSlot = idx + 1
		}
		return idx
	}
	if tc.ID != "" {
		for _, idx := range d.toolOrder {
			if st := d.tools[idx]; st != nil && st.callID == tc.ID {
				d.lastNoIndexSlot = idx
				return idx
			}
		}
	}
	if d.lastNoIndexSlot >= 0 {
		prev := d.tools[d.lastNoIndexSlot]
		if prev != nil {
			newID := tc.ID != "" && prev.callID != "" && tc.ID != prev.callID
			newName := tc.Function != nil && tc.Function.Name != "" && prev.name != "" && tc.Function.Name != prev.name
			if !newID && !newName {
				return d.lastNoIndexSlot
			}
		}
	}
	idx := d.nextToolSlot
	d.nextToolSlot++
	d.lastNoIndexSlot = idx
	return idx
}

func (d *chatDecoder) handleToolDelta(idx int, tc chatInToolRef) []ir.Event {
	st, exists := d.tools[idx]
	if !exists {
		callID := tc.ID
		if callID == "" {
			callID = ir.NewID("call_")
		}
		st = &activeToolState{callID: callID}
		d.tools[idx] = st
		d.toolOrder = append(d.toolOrder, idx)
	}
	if tc.ID != "" {
		st.callID = tc.ID
	}
	if tc.Function != nil {
		if tc.Function.Name != "" {
			st.name = tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			st.argsBuf.WriteString(tc.Function.Arguments)
		}
	}

	var out []ir.Event
	// Wait until we have a tool name if this call isn't started yet.
	if !d.hasOpenTool {
		if st.name != "" {
			d.hasOpenTool = true
			d.openToolIdx = idx
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
		}
		return out
	}

	if d.openToolIdx == idx {
		if tc.Function != nil && tc.Function.Arguments != "" && st.started {
			// Already appended to argsBuf above; drain immediately.
			out = append(out, ir.Event{
				Kind:   ir.EventToolCallDelta,
				CallID: st.callID,
				Args:   st.argsBuf.String(),
			})
			st.argsBuf.Reset()
		}
		return out
	}

	// A different tool index received data. If the currently open tool call is balanced JSON,
	// or empty args, close it and open the next ready index cleanly.
	cur := d.tools[d.openToolIdx]
	if cur != nil && !cur.finished && (cur.argsBuf.Len() == 0) {
		// Keep cur open while buffering st; they will be flushed sequentially in order.
	}
	return out
}

func (d *chatDecoder) flushAllTools() []ir.Event {
	var out []ir.Event
	for _, idx := range d.toolOrder {
		st := d.tools[idx]
		if st == nil || st.finished {
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
		st.finished = true
		out = append(out, ir.Event{
			Kind:   ir.EventToolCallDone,
			CallID: st.callID,
			Name:   st.name,
		})
	}
	d.hasOpenTool = false
	d.openToolIdx = -1
	return out
}

var thinkOpenTags = []string{"<think>", "<thought>"}
var thinkCloseTags = []string{"</think>", "</thought>"}

func (d *chatDecoder) emitContent(delta string) []ir.Event {
	if !d.quirks.ThoughtTagParsing && d.tagTailBuf == "" && !d.inThinkTag {
		// Automatically activate <think> parsing if the stream opens with an explicit tag.
		if !strings.Contains(delta, "<think") && !strings.Contains(delta, "<thought") {
			return []ir.Event{{Kind: ir.EventText, Text: delta}}
		}
	}

	text := d.tagTailBuf + delta
	d.tagTailBuf = ""
	var out []ir.Event

	for len(text) > 0 {
		if d.inThinkTag {
			idx, tagLen := findEarliestTag(text, thinkCloseTags)
			if idx >= 0 {
				if idx > 0 {
					out = append(out, ir.Event{Kind: ir.EventThought, Text: text[:idx]})
				}
				d.inThinkTag = false
				text = text[idx+tagLen:]
				continue
			}
			// Check for partial suffix matching any closing tag.
			if split := partialTagPrefixIdx(text, thinkCloseTags); split >= 0 {
				if split > 0 {
					out = append(out, ir.Event{Kind: ir.EventThought, Text: text[:split]})
				}
				d.tagTailBuf = text[split:]
				return out
			}
			out = append(out, ir.Event{Kind: ir.EventThought, Text: text})
			return out
		}

		idx, tagLen := findEarliestTag(text, thinkOpenTags)
		if idx >= 0 {
			if idx > 0 {
				out = append(out, ir.Event{Kind: ir.EventText, Text: text[:idx]})
			}
			d.inThinkTag = true
			text = text[idx+tagLen:]
			continue
		}
		if split := partialTagPrefixIdx(text, thinkOpenTags); split >= 0 {
			if split > 0 {
				out = append(out, ir.Event{Kind: ir.EventText, Text: text[:split]})
			}
			d.tagTailBuf = text[split:]
			return out
		}
		out = append(out, ir.Event{Kind: ir.EventText, Text: text})
		return out
	}

	return out
}

func findEarliestTag(s string, tags []string) (idx int, length int) {
	bestIdx := -1
	bestLen := 0
	for _, tag := range tags {
		if i := strings.Index(s, tag); i >= 0 && (bestIdx < 0 || i < bestIdx) {
			bestIdx = i
			bestLen = len(tag)
		}
	}
	return bestIdx, bestLen
}

func partialTagPrefixIdx(s string, tags []string) int {
	lastLT := strings.LastIndexByte(s, '<')
	if lastLT < 0 {
		return -1
	}
	tail := s[lastLT:]
	for _, tag := range tags {
		if len(tail) < len(tag) && strings.HasPrefix(tag, tail) {
			return lastLT
		}
	}
	return -1
}

func parseChatUsage(u *chatUsage) ir.Usage {
	if u == nil {
		return ir.Usage{}
	}
	out := ir.Usage{
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		CacheReadTokens: u.PromptCacheHitTokens,
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > out.CacheReadTokens {
		out.CacheReadTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

func mapChatFinishReason(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "stop", "end_turn":
		return ir.FinishStop
	case "length", "max_tokens":
		return ir.FinishLength
	case "tool_calls", "function_call":
		return ir.FinishToolCalls
	case "content_filter":
		return ir.FinishContentFilter
	default:
		return r
	}
}
