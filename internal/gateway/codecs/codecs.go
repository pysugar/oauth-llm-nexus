// Package codecs registers and looks up all supported gateway protocol codecs.
package codecs

import (
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec/anthropic"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec/gemini"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec/openaichat"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec/openairesp"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

var registry = map[ir.Protocol]codec.Codec{
	ir.ProtoOpenAIChat: openaichat.New(),
	ir.ProtoOpenAIResp: openairesp.New(),
	ir.ProtoAnthropic:  anthropic.New(),
	ir.ProtoGemini:     gemini.New(),
}

// For returns the Codec implementation for protocol p.
func For(p ir.Protocol) (codec.Codec, bool) {
	c, ok := registry[p]
	return c, ok
}

// MustFor returns the Codec for protocol p or panics if unknown.
func MustFor(p ir.Protocol) codec.Codec {
	c, ok := registry[p]
	if !ok {
		panic("unknown protocol codec: " + string(p))
	}
	return c
}
