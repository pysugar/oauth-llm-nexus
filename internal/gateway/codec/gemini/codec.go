// Package gemini implements the Google Gemini API (/v1beta/models/*:generateContent) protocol codec.
package gemini

import "github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"

// Codec implements codec.Codec for the Google Gemini v1beta API.
type Codec struct{}

// New returns a Gemini v1beta codec instance.
func New() *Codec {
	return &Codec{}
}

// Protocol returns ir.ProtoGemini.
func (c *Codec) Protocol() ir.Protocol {
	return ir.ProtoGemini
}
