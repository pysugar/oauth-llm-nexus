package gemini

import (
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

type gemStreamResponse struct {
	ResponseID    string `json:"responseId"`
	ModelVersion  string `json:"modelVersion"`
	Candidates    []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finishReason"`
		Content      *struct {
			Role  string      `json:"role"`
			Parts []gemInPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	Error json.RawMessage `json:"error"`
}

type geminiDecoder struct {
	sentStart    bool
	sentFinish   bool
	respID       string
	model        string
	finishReason string
	hasToolCall  bool
	usage        ir.Usage
}

// NewDecoder creates a Google Gemini SSE StreamDecoder.
func (c *Codec) NewDecoder(_ codec.Quirks) codec.StreamDecoder {
	return &geminiDecoder{}
}

func (d *geminiDecoder) DecodeFrame(frame sse.Event) ([]ir.Event, bool, *ir.GatewayError) {
	if len(frame.Data) == 0 {
		return nil, false, nil
	}
	if sse.IsDone(frame.Data) {
		return d.Flush(), true, nil
	}

	normalized := NormalizeKeysCamel(frame.Data)
	var chunk gemStreamResponse
	if err := json.Unmarshal(normalized, &chunk); err != nil {
		return nil, false, nil
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		return nil, true, codec.ParseUpstreamError(502, normalized)
	}

	if chunk.ResponseID != "" {
		d.respID = chunk.ResponseID
	}
	if chunk.ModelVersion != "" {
		d.model = chunk.ModelVersion
	}

	if chunk.UsageMetadata != nil {
		um := chunk.UsageMetadata
		ir.MergeUsage(&d.usage, ir.Usage{
			InputTokens:     um.PromptTokenCount,
			OutputTokens:    um.CandidatesTokenCount + um.ThoughtsTokenCount,
			CacheReadTokens: um.CachedContentTokenCount,
			ReasoningTokens: um.ThoughtsTokenCount,
		})
	}

	var out []ir.Event
	if !d.sentStart && (d.respID != "" || d.model != "" || len(chunk.Candidates) > 0) {
		d.sentStart = true
		out = append(out, ir.Event{
			Kind:       ir.EventStart,
			ResponseID: d.respID,
			Model:      d.model,
			Usage:      d.usage,
		})
	}

	for _, cand := range chunk.Candidates {
		if cand.Index != 0 {
			continue
		}
		if cand.Content != nil {
			for _, p := range cand.Content.Parts {
				switch {
				case p.Thought:
					if p.Text != "" || p.ThoughtSignature != "" {
						out = append(out, ir.Event{
							Kind:      ir.EventThought,
							Text:      p.Text,
							Signature: p.ThoughtSignature,
						})
					}
				case p.FunctionCall != nil:
					d.hasToolCall = true
					callID := p.FunctionCall.ID
					if callID == "" {
						callID = ir.NewID("call_")
					}
					if p.ThoughtSignature != "" {
						ir.DefaultSigStore.SaveCall(callID, p.ThoughtSignature)
					}
					argsStr := "{}"
					if len(p.FunctionCall.Args) > 0 && string(p.FunctionCall.Args) != "null" {
						argsStr = string(p.FunctionCall.Args)
					}
					out = append(out,
						ir.Event{
							Kind:             ir.EventToolCallStart,
							CallID:           callID,
							Name:             p.FunctionCall.Name,
							ThoughtSignature: p.ThoughtSignature,
						},
						ir.Event{
							Kind:             ir.EventToolCallDelta,
							CallID:           callID,
							Args:             argsStr,
							ThoughtSignature: p.ThoughtSignature,
						},
						ir.Event{
							Kind:             ir.EventToolCallDone,
							CallID:           callID,
							Name:             p.FunctionCall.Name,
							Args:             argsStr,
							ThoughtSignature: p.ThoughtSignature,
						},
					)
				case p.Text != "":
					out = append(out, ir.Event{Kind: ir.EventText, Text: p.Text})
					if p.ThoughtSignature != "" {
						out = append(out, ir.Event{
							Kind:      ir.EventThoughtSignature,
							Signature: p.ThoughtSignature,
						})
					}
				case p.ThoughtSignature != "":
					out = append(out, ir.Event{
						Kind:      ir.EventThoughtSignature,
						Signature: p.ThoughtSignature,
					})
				}
			}
		}

		if cand.FinishReason != "" {
			d.finishReason = mapGeminiFinishReason(cand.FinishReason)
		}
	}

	if chunk.UsageMetadata != nil {
		out = append(out, ir.Event{Kind: ir.EventUsage, Usage: d.usage})
	}

	return out, false, nil
}

func (d *geminiDecoder) Flush() []ir.Event {
	if d.sentFinish {
		return nil
	}
	d.sentFinish = true
	reason := d.finishReason
	if d.hasToolCall && (reason == "" || reason == ir.FinishStop) {
		reason = ir.FinishToolCalls
	}
	if reason == "" {
		reason = ir.FinishStop
	}
	return []ir.Event{{
		Kind:         ir.EventFinish,
		FinishReason: reason,
		Usage:        d.usage,
	}}
}

func mapGeminiFinishReason(r string) string {
	switch strings.ToUpper(strings.TrimSpace(r)) {
	case "STOP":
		return ir.FinishStop
	case "MAX_TOKENS":
		return ir.FinishLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return ir.FinishContentFilter
	default:
		return ir.FinishStop
	}
}
