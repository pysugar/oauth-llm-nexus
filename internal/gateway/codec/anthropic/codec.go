// Package anthropic implements the Anthropic Messages API (/v1/messages) protocol codec.
package anthropic

import "github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"

// Codec implements codec.Codec for the Anthropic Messages API.
type Codec struct{}

// New returns an Anthropic Messages codec instance.
func New() *Codec {
	return &Codec{}
}

// Protocol returns ir.ProtoAnthropic.
func (c *Codec) Protocol() ir.Protocol {
	return ir.ProtoAnthropic
}
