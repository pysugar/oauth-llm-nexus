package server

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codecs"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

func (s *Server) serveTranslated(
	w http.ResponseWriter,
	resp *http.Response,
	irReq *ir.Request,
	clientCodec codec.Codec,
	targetProto ir.Protocol,
	cand router.Candidate,
	apiKey string,
	routeLabel string,
) (attemptOutcome, bool, *ir.GatewayError) {
	outcome := attemptOutcome{
		ProviderID:    cand.Provider.ID,
		UpstreamModel: cand.UpstreamModel,
		AccountLabel:  maskKeyLabel(apiKey),
		Status:        resp.StatusCode,
	}
	if routeLabel == "" {
		routeLabel = "translated:" + string(targetProto)
	}

	targetCodec, ok := codecs.For(targetProto)
	if !ok {
		return outcome, false, ir.NewError(500, "missing_codec", "missing target codec "+string(targetProto))
	}

	br := bufio.NewReaderSize(resp.Body, 64*1024)
	if errProbe := probeNonSSEJSONError(resp.StatusCode, resp.Header.Get("Content-Type"), br); errProbe != nil {
		return outcome, false, errProbe
	}

	dec := targetCodec.NewDecoder(cand.Provider.Quirks)
	if irReq.Source == ir.ProtoAnthropic && targetProto != ir.ProtoAnthropic {
		dec = sigScrubDecoder{dec}
	}
	pump := startPump(sse.NewReader(br))
	defer pump.Close()
	cs, _ := w.(*clientStream)
	rd := &streamReader{
		ctx:     respContext(resp),
		pump:    pump,
		dec:     dec,
		cs:      cs,
		idle:    cand.Provider.Timeout,
		ftt:     cand.Provider.FirstTokenTimeout,
		policy:  s.streamPolicy(),
		started: time.Now(),
	}
	col := ir.NewCollector()

	// Non-streaming client: collect the complete translated stream before committing HTTP 200
	// so a broken, truncated or empty stream fails over cleanly.
	if !irReq.Stream {
		for !rd.done {
			f, err := rd.next(!rd.content, time.Time{})
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return outcome, false, toGatewayError(err)
			}
			for _, ev := range f.events {
				col.Add(ev)
			}
		}
		if ge := rd.drainEnd(col.Add); ge != nil {
			return outcome, false, ge
		}
		if ge := col.Err(); ge != nil {
			return outcome, false, ge
		}
		outcome.TTFT = rd.ttft

		res := col.Build()
		if res.Model == "" {
			res.Model = irReq.Model
		}
		if irReq.Source == ir.ProtoOpenAIResp && res.ID == "" {
			res.ID = ir.NewID("resp_")
		}
		outBytes, ge := clientCodec.EncodeResult(res)
		if ge != nil {
			return outcome, false, ge
		}

		setProvenanceHeaders(w, cand, routeLabel)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(outBytes)

		outcome.InputTokens = res.Usage.InputTokens
		outcome.OutputTokens = res.Usage.OutputTokens
		outcome.CachedTokens = res.Usage.CacheReadTokens
		outcome.CacheWriteTokens = res.Usage.CacheWriteTokens
		outcome.ReasoningTokens = res.Usage.ReasoningTokens
		outcome.ResponseBody = string(outBytes)
		if irReq.Source == ir.ProtoOpenAIResp && s.RespStore != nil && !irReq.SkipRespStore && res.ID != "" {
			s.RespStore.SaveBy(res.ID, cand.Provider.ID, irReq.Messages, res.Content)
		}
		return outcome, true, nil
	}

	// Streaming client: hold until first content (keepalives flow meanwhile), then commit.
	frames, finished, ge := rd.hold()
	if ge != nil {
		return outcome, false, ge
	}

	setProvenanceHeaders(w, cand, routeLabel)
	sse.SetHeaders(w)
	w.WriteHeader(http.StatusOK)
	if cs != nil {
		cs.committed = true
	}
	outcome.TTFT = rd.ttft

	sw := sse.NewWriter(w)
	enc := clientCodec.NewEncoder(irReq.Model)
	clientGone := false
	emitEvents := func(evs []ir.Event) {
		for _, ev := range evs {
			col.Add(ev)
			if clientGone {
				continue
			}
			if err := enc.Encode(ev, sw); err != nil {
				clientGone = true
			}
		}
	}
	for _, f := range frames {
		emitEvents(f.events)
	}

	var streamErr *ir.GatewayError
	if !finished {
		for !rd.done && !clientGone {
			f, err := rd.next(false, time.Time{})
			if err != nil {
				if !errors.Is(err, io.EOF) {
					streamErr = toGatewayError(err)
				}
				break
			}
			emitEvents(f.events)
		}
	}
	if streamErr == nil && !clientGone {
		streamErr = rd.drainEnd(func(ev ir.Event) { emitEvents([]ir.Event{ev}) })
	}
	if streamErr != nil {
		// Post-commit failure: report it in-stream instead of fabricating a finished reply.
		outcome.StreamErr = streamErr.Code
		if !clientGone {
			_ = enc.Encode(ir.Event{Kind: ir.EventError, Err: streamErr}, sw)
		}
	} else if !clientGone {
		_ = enc.Finish(sw)
	}

	res := col.Build()
	outcome.InputTokens = res.Usage.InputTokens
	outcome.OutputTokens = res.Usage.OutputTokens
	outcome.CachedTokens = res.Usage.CacheReadTokens
	outcome.CacheWriteTokens = res.Usage.CacheWriteTokens
	outcome.ReasoningTokens = res.Usage.ReasoningTokens
	if irReq.Source == ir.ProtoOpenAIResp && s.RespStore != nil && !irReq.SkipRespStore && streamErr == nil {
		respID := res.ID
		if idProvider, ok := enc.(interface{ ResponseID() string }); ok && idProvider.ResponseID() != "" {
			respID = idProvider.ResponseID()
		}
		if respID != "" {
			s.RespStore.SaveBy(respID, cand.Provider.ID, irReq.Messages, res.Content)
		}
	}
	return outcome, true, nil
}

// probeNonSSEJSONError catches upstream relays that return HTTP 200 with a raw JSON error payload
// (no "data:" SSE framing) before streaming begins.
func probeNonSSEJSONError(status int, contentType string, br *bufio.Reader) *ir.GatewayError {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return nil
	}
	peeked, _ := br.Peek(256)
	trimmed := bytes.TrimSpace(peeked)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(br, 1024*1024))
	if ge := probeJSONError(status, body); ge != nil {
		return ge
	}
	return ir.NewError(502, "unexpected_json_response", "upstream returned non-SSE JSON payload without events")
}

// sigScrubDecoder drops non-Claude thinking signatures before they reach an Anthropic client:
// a client like Claude Code would send them back to a real Claude later and get a 400. Gemini
// signatures still round-trip through the SigStore by tool call id.
type sigScrubDecoder struct{ codec.StreamDecoder }

func (d sigScrubDecoder) DecodeFrame(f sse.Event) ([]ir.Event, bool, *ir.GatewayError) {
	evs, done, ge := d.StreamDecoder.DecodeFrame(f)
	return scrubSignatures(evs), done, ge
}

func (d sigScrubDecoder) Flush() []ir.Event { return scrubSignatures(d.StreamDecoder.Flush()) }

func scrubSignatures(evs []ir.Event) []ir.Event {
	out := evs[:0]
	for _, ev := range evs {
		if ev.Kind == ir.EventThoughtSignature {
			continue
		}
		if ev.Kind == ir.EventThought {
			ev.Signature = ""
		}
		out = append(out, ev)
	}
	return out
}
