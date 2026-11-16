package server

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"

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
	sr := sse.NewReader(br)
	col := ir.NewCollector()

	// Non-streaming client: collect the complete translated stream before committing HTTP 200
	// so any stream error triggers clean failover.
	if !irReq.Stream {
		for {
			frame, err := sr.Next()
			if err != nil {
				if err == io.EOF {
					break
				}
				return outcome, false, ir.NewError(502, "upstream_stream_read_error", err.Error())
			}
			events, done, ge := dec.DecodeFrame(frame)
			if ge != nil {
				return outcome, false, ge
			}
			for _, ev := range events {
				col.Add(ev)
			}
			if done {
				break
			}
		}
		for _, ev := range dec.Flush() {
			col.Add(ev)
		}
		if ge := col.Err(); ge != nil {
			return outcome, false, ge
		}

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
		outcome.ReasoningTokens = res.Usage.ReasoningTokens
		outcome.ResponseBody = string(outBytes)
		if irReq.Source == ir.ProtoOpenAIResp && s.RespStore != nil && res.ID != "" {
			s.RespStore.Save(res.ID, irReq.Messages, res.Content)
		}
		return outcome, true, nil
	}

	// Streaming client: read pre-commit initial IR events to verify stream health before committing HTTP 200.
	var initialEvents []ir.Event
	streamDone := false
	for !streamDone && len(initialEvents) == 0 {
		frame, err := sr.Next()
		if err != nil {
			if err == io.EOF {
				initialEvents = append(initialEvents, dec.Flush()...)
				streamDone = true
				break
			}
			return outcome, false, ir.NewError(502, "upstream_stream_read_error", err.Error())
		}
		events, done, ge := dec.DecodeFrame(frame)
		if ge != nil {
			return outcome, false, ge
		}
		initialEvents = append(initialEvents, events...)
		if done {
			streamDone = true
		}
	}

	if len(initialEvents) == 0 {
		return outcome, false, ir.NewError(502, "empty_upstream_stream", "upstream closed stream before producing any events")
	}

	// Commit client response headers and stream buffered + subsequent events.
	setProvenanceHeaders(w, cand, routeLabel)
	sse.SetHeaders(w)
	w.WriteHeader(http.StatusOK)

	sw := sse.NewWriter(w)
	enc := clientCodec.NewEncoder(irReq.Model)

	emitEvents := func(evs []ir.Event) bool {
		for _, ev := range evs {
			col.Add(ev)
			if err := enc.Encode(ev, sw); err != nil {
				return false
			}
		}
		return true
	}

	if !emitEvents(initialEvents) {
		res := col.Build()
		outcome.InputTokens = res.Usage.InputTokens
		outcome.OutputTokens = res.Usage.OutputTokens
		return outcome, true, nil
	}

	if !streamDone {
		for {
			frame, err := sr.Next()
			if err != nil {
				break
			}
			events, done, ge := dec.DecodeFrame(frame)
			if ge != nil {
				_ = enc.Encode(ir.Event{Kind: ir.EventError, Err: ge}, sw)
				streamDone = true
				break
			}
			if !emitEvents(events) {
				streamDone = true
				break
			}
			if done {
				streamDone = true
				break
			}
		}
	}

	flushed := dec.Flush()
	_ = emitEvents(flushed)
	_ = enc.Finish(sw)

	res := col.Build()
	outcome.InputTokens = res.Usage.InputTokens
	outcome.OutputTokens = res.Usage.OutputTokens
	outcome.CachedTokens = res.Usage.CacheReadTokens
	outcome.ReasoningTokens = res.Usage.ReasoningTokens
	if irReq.Source == ir.ProtoOpenAIResp && s.RespStore != nil {
		respID := res.ID
		if idProvider, ok := enc.(interface{ ResponseID() string }); ok && idProvider.ResponseID() != "" {
			respID = idProvider.ResponseID()
		}
		if respID != "" {
			s.RespStore.Save(respID, irReq.Messages, res.Content)
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
