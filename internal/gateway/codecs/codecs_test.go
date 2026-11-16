package codecs

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

func TestCrossProtocolTranslationMatrix(t *testing.T) {
	chatCodec := MustFor(ir.ProtoOpenAIChat)
	respCodec := MustFor(ir.ProtoOpenAIResp)
	anthCodec := MustFor(ir.ProtoAnthropic)
	gemCodec := MustFor(ir.ProtoGemini)

	// Client sends Anthropic Messages request -> translated to Gemini -> Gemini SSE -> Anthropic non-stream Result.
	anthReq := `{
		"model": "gemini-2.5-flash",
		"system": "Answer precisely.",
		"max_tokens": 512,
		"messages": [{"role": "user", "content": "What is 2+2?"}]
	}`
	req, ge := anthCodec.ParseRequest("", []byte(anthReq))
	if ge != nil {
		t.Fatalf("ParseRequest failed: %v", ge)
	}
	gemBody, ge := gemCodec.BuildRequest(req, "gemini-2.5-flash", true, codec.Quirks{})
	if ge != nil {
		t.Fatalf("BuildRequest to Gemini failed: %v", ge)
	}
	if !strings.Contains(string(gemBody), "Answer precisely.") {
		t.Fatalf("expected system instruction in Gemini payload: %s", string(gemBody))
	}

	// Simulate Gemini SSE output -> decode -> encode to Anthropic Result AND Responses Result AND Chat Result.
	gemSSE := "data: {\"responseId\":\"g1\",\"modelVersion\":\"gemini-2.5-flash\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"4\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\",\"content\":{\"role\":\"model\",\"parts\":[]}}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":1}}\n\n"

	dec := gemCodec.NewDecoder(codec.Quirks{})
	col := ir.NewCollector()
	sr := sse.NewReader(strings.NewReader(gemSSE))
	for {
		f, err := sr.Next()
		if err != nil {
			break
		}
		evs, _, gerr := dec.DecodeFrame(f)
		if gerr != nil {
			t.Fatalf("decode error: %v", gerr)
		}
		for _, e := range evs {
			col.Add(e)
		}
	}
	for _, e := range dec.Flush() {
		col.Add(e)
	}
	res := col.Build()

	for _, target := range []codec.Codec{chatCodec, respCodec, anthCodec, gemCodec} {
		out, gerr := target.EncodeResult(res)
		if gerr != nil {
			t.Fatalf("EncodeResult(%s) failed: %v", target.Protocol(), gerr)
		}
		if !strings.Contains(string(out), `"4"`) {
			t.Fatalf("EncodeResult(%s) missing text '4': %s", target.Protocol(), string(out))
		}
	}

	// Verify all 4 stream encoders accumulate EventUsage and emit token counts on Finish().
	for _, target := range []codec.Codec{chatCodec, respCodec, anthCodec, gemCodec} {
		rec := httptest.NewRecorder()
		sw := sse.NewWriter(rec)
		enc := target.NewEncoder("m-test")
		_ = enc.Encode(ir.Event{Kind: ir.EventStart, Model: "m-test", Usage: ir.Usage{InputTokens: 11}}, sw)
		_ = enc.Encode(ir.Event{Kind: ir.EventText, Text: "ok"}, sw)
		_ = enc.Encode(ir.Event{Kind: ir.EventUsage, Usage: ir.Usage{InputTokens: 11, OutputTokens: 7}}, sw)
		_ = enc.Finish(sw)
		body := rec.Body.String()
		if !strings.Contains(body, `"11"`) && !strings.Contains(body, `:11`) {
			t.Fatalf("encoder(%s) did not preserve input token usage on Finish(): %s", target.Protocol(), body)
		}
		if !strings.Contains(body, `:7`) {
			t.Fatalf("encoder(%s) did not preserve output token usage on Finish(): %s", target.Protocol(), body)
		}
	}
}
