// Package openairesp implements the OpenAI Responses API (/v1/responses) protocol codec.
package openairesp

import "github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"

// Codec implements codec.Codec for the OpenAI Responses API.
type Codec struct{}

// New returns an OpenAI Responses codec instance.
func New() *Codec {
	return &Codec{}
}

// Protocol returns ir.ProtoOpenAIResp.
func (c *Codec) Protocol() ir.Protocol {
	return ir.ProtoOpenAIResp
}
