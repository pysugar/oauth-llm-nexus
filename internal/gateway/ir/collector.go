package ir

import "strings"

// Collector aggregates streaming IR Events into a single Result.
type Collector struct {
	id           string
	model        string
	finishReason string
	usage        Usage
	parts        []Part
	toolIndex    map[string]int
	err          *GatewayError
}

// NewCollector returns an initialized Event aggregator.
func NewCollector() *Collector {
	return &Collector{
		toolIndex: make(map[string]int),
	}
}

// Add incorporates a single Event into the aggregated result.
func (c *Collector) Add(ev Event) {
	switch ev.Kind {
	case EventStart:
		if ev.ResponseID != "" {
			c.id = ev.ResponseID
		}
		if ev.Model != "" {
			c.model = ev.Model
		}
		MergeUsage(&c.usage, ev.Usage)

	case EventText:
		if ev.Text == "" {
			return
		}
		if n := len(c.parts); n > 0 && c.parts[n-1].Kind == KindText {
			c.parts[n-1].Text += ev.Text
		} else {
			c.parts = append(c.parts, Part{
				Kind: KindText,
				Text: ev.Text,
			})
		}

	case EventThought:
		if ev.Text == "" && ev.Signature == "" {
			return
		}
		if n := len(c.parts); n > 0 && c.parts[n-1].Kind == KindThought {
			c.parts[n-1].Text += ev.Text
			if ev.Signature != "" {
				c.parts[n-1].Signature = ev.Signature
			}
		} else {
			c.parts = append(c.parts, Part{
				Kind:      KindThought,
				Text:      ev.Text,
				Signature: ev.Signature,
			})
		}

	case EventThoughtSignature:
		if ev.Signature == "" {
			return
		}
		if n := len(c.parts); n > 0 && c.parts[n-1].Kind == KindThought {
			c.parts[n-1].Signature = ev.Signature
		} else {
			c.parts = append(c.parts, Part{
				Kind:      KindThought,
				Signature: ev.Signature,
			})
		}

	case EventToolCallStart:
		callID := ev.CallID
		if callID == "" {
			callID = NewID("call_")
		}
		if idx, exists := c.toolIndex[callID]; exists {
			if ev.Name != "" {
				c.parts[idx].Name = ev.Name
			}
			if ev.Args != "" {
				c.parts[idx].Args += ev.Args
			}
			if ev.ThoughtSignature != "" {
				c.parts[idx].ThoughtSignature = ev.ThoughtSignature
			}
			return
		}
		idx := len(c.parts)
		c.toolIndex[callID] = idx
		c.parts = append(c.parts, Part{
			Kind:             KindToolCall,
			CallID:           callID,
			Name:             ev.Name,
			Args:             ev.Args,
			ThoughtSignature: ev.ThoughtSignature,
		})

	case EventToolCallDelta:
		if ev.CallID == "" {
			// Attach to the most recent tool call when delta omits ID.
			for i := len(c.parts) - 1; i >= 0; i-- {
				if c.parts[i].Kind == KindToolCall {
					c.parts[i].Args += ev.Args
					return
				}
			}
			return
		}
		if idx, exists := c.toolIndex[ev.CallID]; exists {
			c.parts[idx].Args += ev.Args
			if ev.ThoughtSignature != "" {
				c.parts[idx].ThoughtSignature = ev.ThoughtSignature
			}
		} else {
			idx := len(c.parts)
			c.toolIndex[ev.CallID] = idx
			c.parts = append(c.parts, Part{
				Kind:             KindToolCall,
				CallID:           ev.CallID,
				Name:             ev.Name,
				Args:             ev.Args,
				ThoughtSignature: ev.ThoughtSignature,
			})
		}

	case EventToolCallDone:
		if ev.CallID == "" {
			return
		}
		if idx, exists := c.toolIndex[ev.CallID]; exists {
			if ev.Name != "" && c.parts[idx].Name == "" {
				c.parts[idx].Name = ev.Name
			}
			if ev.Args != "" {
				c.parts[idx].Args = ev.Args
			}
			if ev.ThoughtSignature != "" {
				c.parts[idx].ThoughtSignature = ev.ThoughtSignature
			}
		}

	case EventUsage:
		MergeUsage(&c.usage, ev.Usage)

	case EventFinish:
		if ev.FinishReason != "" {
			c.finishReason = ev.FinishReason
		}
		MergeUsage(&c.usage, ev.Usage)

	case EventError:
		if ev.Err != nil {
			c.err = ev.Err
		}
	}
}

// Err returns the fatal error recorded via EventError, if any.
func (c *Collector) Err() *GatewayError {
	return c.err
}

// Build finalizes and returns the aggregated Result.
func (c *Collector) Build() *Result {
	id := c.id
	if id == "" {
		id = NewID("resp_")
	}
	parts := make([]Part, 0, len(c.parts))
	hasToolCall := false
	for _, p := range c.parts {
		if p.Kind == KindToolCall {
			hasToolCall = true
			if strings.TrimSpace(p.Args) == "" {
				p.Args = "{}"
			}
		}
		parts = append(parts, p)
	}
	reason := c.finishReason
	if reason == "" {
		if hasToolCall {
			reason = FinishToolCalls
		} else {
			reason = FinishStop
		}
	}
	return &Result{
		ID:           id,
		Model:        c.model,
		Content:      parts,
		FinishReason: reason,
		Usage:        c.usage,
	}
}
