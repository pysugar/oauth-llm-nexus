package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codecs"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

func (s *Server) servePassthrough(
	w http.ResponseWriter,
	resp *http.Response,
	irReq *ir.Request,
	_ codec.Codec,
	targetProto ir.Protocol,
	cand router.Candidate,
	apiKey string,
) (attemptOutcome, bool, *ir.GatewayError) {
	outcome := attemptOutcome{
		ProviderID:    cand.Provider.ID,
		UpstreamModel: cand.UpstreamModel,
		AccountLabel:  maskKeyLabel(apiKey),
		Status:        resp.StatusCode,
	}

	if !irReq.Stream {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodyBytes))
		if err != nil {
			return outcome, false, ir.NewError(502, "upstream_read_failed", err.Error())
		}
		if ge := probeJSONError(resp.StatusCode, body); ge != nil {
			return outcome, false, ge
		}

		setProvenanceHeaders(w, cand, "passthrough")
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)

		outcome.ResponseBody = string(body)
		u := extractJSONUsage(body)
		outcome.InputTokens = u.InputTokens
		outcome.OutputTokens = u.OutputTokens
		outcome.CachedTokens = u.CacheReadTokens
		outcome.ReasoningTokens = u.ReasoningTokens
		if irReq.Source == ir.ProtoOpenAIResp && s.RespStore != nil {
			if respID, parts := extractResponsesJSONTurn(body); respID != "" {
				s.RespStore.Save(respID, irReq.Messages, parts)
			}
		}
		return outcome, true, nil
	}

	// Streaming passthrough: inspect first SSE frame before committing headers to allow clean failover.
	br := bufio.NewReaderSize(resp.Body, 64*1024)
	firstFrameBytes, firstData, isEOF, readErr := readFirstSSEFrameBytes(br)
	if readErr != nil && len(firstFrameBytes) == 0 {
		return outcome, false, ir.NewError(502, "upstream_stream_failed", readErr.Error())
	}
	if ge := probeSSEDataError(firstData); ge != nil {
		return outcome, false, ge
	}
	if len(firstData) == 0 && len(firstFrameBytes) > 0 {
		if ge := probeSSEDataError(firstFrameBytes); ge != nil {
			return outcome, false, ge
		}
	}

	setProvenanceHeaders(w, cand, "passthrough")
	sse.SetHeaders(w)
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	var capture bytes.Buffer
	const maxCaptureBytes = 512 * 1024
	const maxTailBytes = 128 * 1024
	var tail []byte
	var totalBytes int

	recordChunk := func(chunk []byte) {
		if len(chunk) == 0 {
			return
		}
		totalBytes += len(chunk)
		if capture.Len() < maxCaptureBytes {
			remaining := maxCaptureBytes - capture.Len()
			if len(chunk) > remaining {
				capture.Write(chunk[:remaining])
			} else {
				capture.Write(chunk)
			}
		}
		// Keep a bounded rolling tail so streams larger than maxCaptureBytes still capture final SSE usage chunks.
		if totalBytes > maxCaptureBytes || len(tail) > 0 {
			tail = append(tail, chunk...)
			if len(tail) > maxTailBytes*2 {
				tail = append([]byte(nil), tail[len(tail)-maxTailBytes:]...)
			}
		}
	}

	if len(firstFrameBytes) > 0 {
		_, _ = w.Write(firstFrameBytes)
		if flusher != nil {
			flusher.Flush()
		}
		recordChunk(firstFrameBytes)
	}

	if !isEOF {
		buf := make([]byte, 32*1024)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				_, _ = w.Write(chunk)
				if flusher != nil {
					flusher.Flush()
				}
				recordChunk(chunk)
			}
			if err != nil {
				break
			}
		}
	}

	outcome.ResponseBody = capture.String()
	usageBytes := capture.Bytes()
	if totalBytes > maxCaptureBytes && len(tail) > 0 {
		usageBytes = tail
	}
	u := extractSSEUsage(targetProto, usageBytes, cand.Provider.Quirks)
	outcome.InputTokens = u.InputTokens
	outcome.OutputTokens = u.OutputTokens
	outcome.CachedTokens = u.CacheReadTokens
	outcome.ReasoningTokens = u.ReasoningTokens
	if irReq.Source == ir.ProtoOpenAIResp && s.RespStore != nil {
		if respID, parts := extractResponsesSSETurn(usageBytes); respID != "" {
			s.RespStore.Save(respID, irReq.Messages, parts)
		}
	}
	return outcome, true, nil
}

func readFirstSSEFrameBytes(br *bufio.Reader) (rawFrame []byte, firstData []byte, eof bool, err error) {
	var rawBuf bytes.Buffer
	var dataLines [][]byte
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			rawBuf.Write(line)
			trimmed := bytes.TrimRight(line, "\r\n")
			if len(trimmed) == 0 {
				if len(dataLines) > 0 || rawBuf.Len() > 0 {
					return rawBuf.Bytes(), bytes.Join(dataLines, []byte{'\n'}), false, nil
				}
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("data:")) {
				val := bytes.TrimPrefix(trimmed, []byte("data:"))
				val = bytes.TrimPrefix(val, []byte(" "))
				cp := make([]byte, len(val))
				copy(cp, val)
				dataLines = append(dataLines, cp)
			}
		}
		if rerr != nil {
			isEOF := rerr == io.EOF
			if isEOF {
				return rawBuf.Bytes(), bytes.Join(dataLines, []byte{'\n'}), true, nil
			}
			return rawBuf.Bytes(), bytes.Join(dataLines, []byte{'\n'}), false, rerr
		}
	}
}

func probeJSONError(status int, body []byte) *ir.GatewayError {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &arr) == nil && len(arr) > 0 {
			return probeJSONError(status, arr[0])
		}
		return nil
	}
	var probe struct {
		Type   string          `json:"type"`
		Error  json.RawMessage `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal([]byte(trimmed), &probe) == nil {
		hasErr := len(probe.Error) > 0 && string(probe.Error) != "null"
		hasDetail := len(probe.Detail) > 0 && string(probe.Detail) != "null"
		if hasErr || hasDetail || probe.Type == "error" {
			if status >= 200 && status < 300 {
				status = 502
			}
			return codec.ParseUpstreamError(status, []byte(trimmed))
		}
	}
	return nil
}

func probeSSEDataError(data []byte) *ir.GatewayError {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "[DONE]" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil
	}
	return probeJSONError(502, []byte(trimmed))
}

func extractJSONUsage(body []byte) ir.Usage {
	var probe struct {
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			OutputTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		UsageMetadata *struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ir.Usage{}
	}
	if probe.Usage != nil {
		inTok := probe.Usage.PromptTokens
		if inTok == 0 {
			inTok = probe.Usage.InputTokens
		}
		outTok := probe.Usage.CompletionTokens
		if outTok == 0 {
			outTok = probe.Usage.OutputTokens
		}
		cached := probe.Usage.CacheReadTokens
		if cached == 0 && probe.Usage.PromptTokensDetails != nil {
			cached = probe.Usage.PromptTokensDetails.CachedTokens
		}
		if cached == 0 && probe.Usage.InputTokensDetails != nil {
			cached = probe.Usage.InputTokensDetails.CachedTokens
		}
		reasoning := 0
		if probe.Usage.CompletionTokensDetails != nil {
			reasoning = probe.Usage.CompletionTokensDetails.ReasoningTokens
		}
		if reasoning == 0 && probe.Usage.OutputTokensDetails != nil {
			reasoning = probe.Usage.OutputTokensDetails.ReasoningTokens
		}
		return ir.Usage{
			InputTokens:     inTok,
			OutputTokens:    outTok,
			CacheReadTokens: cached,
			ReasoningTokens: reasoning,
		}
	}
	if probe.UsageMetadata != nil {
		return ir.Usage{
			InputTokens:     probe.UsageMetadata.PromptTokenCount,
			OutputTokens:    probe.UsageMetadata.CandidatesTokenCount + probe.UsageMetadata.ThoughtsTokenCount,
			CacheReadTokens: probe.UsageMetadata.CachedContentTokenCount,
			ReasoningTokens: probe.UsageMetadata.ThoughtsTokenCount,
		}
	}
	return ir.Usage{}
}

func extractResponsesJSONTurn(body []byte) (string, []ir.Part) {
	var resp struct {
		ID     string `json:"id"`
		Output []struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &resp) != nil || resp.ID == "" {
		return "", nil
	}
	var parts []ir.Part
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Text != "" {
					parts = append(parts, ir.Part{Kind: ir.KindText, Text: c.Text})
				}
			}
		case "function_call":
			parts = append(parts, ir.Part{
				Kind:   ir.KindToolCall,
				CallID: item.CallID,
				Name:   item.Name,
				Args:   item.Arguments,
			})
		}
	}
	return resp.ID, parts
}

func extractResponsesSSETurn(raw []byte) (string, []ir.Part) {
	c, ok := codecs.For(ir.ProtoOpenAIResp)
	if !ok {
		return "", nil
	}
	dec := c.NewDecoder(codec.Quirks{})
	col := ir.NewCollector()
	sr := sse.NewReader(bytes.NewReader(raw))
	for {
		ev, err := sr.Next()
		if err != nil {
			break
		}
		events, done, ge := dec.DecodeFrame(ev)
		if ge != nil {
			break
		}
		for _, item := range events {
			col.Add(item)
		}
		if done {
			break
		}
	}
	for _, item := range dec.Flush() {
		col.Add(item)
	}
	res := col.Build()
	return res.ID, res.Content
}

func extractSSEUsage(proto ir.Protocol, raw []byte, q codec.Quirks) ir.Usage {
	c, ok := codecs.For(proto)
	if !ok {
		return ir.Usage{}
	}
	dec := c.NewDecoder(q)
	col := ir.NewCollector()
	sr := sse.NewReader(bytes.NewReader(raw))
	for {
		ev, err := sr.Next()
		if err != nil {
			break
		}
		events, done, ge := dec.DecodeFrame(ev)
		if ge != nil {
			break
		}
		for _, item := range events {
			col.Add(item)
		}
		if done {
			break
		}
	}
	for _, item := range dec.Flush() {
		col.Add(item)
	}
	return col.Build().Usage
}
