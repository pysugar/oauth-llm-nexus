// Package openaichat implements the OpenAI Chat Completions (/v1/chat/completions) protocol codec.
package openaichat

import "github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"

// Codec implements codec.Codec for OpenAI Chat Completions.
type Codec struct{}

// New returns an OpenAI Chat Completions codec instance.
func New() *Codec {
	return &Codec{}
}

// Protocol returns ir.ProtoOpenAIChat.
func (c *Codec) Protocol() ir.Protocol {
	return ir.ProtoOpenAIChat
}
