package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codecs"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

func (s *Server) executePlan(
	w http.ResponseWriter,
	r *http.Request,
	clientProto ir.Protocol,
	clientCodec codec.Codec,
	irReq *ir.Request,
	rawBody []byte,
	plan *router.Plan,
	_ time.Time,
) (attemptOutcome, *ir.GatewayError) {
	var lastErr *ir.GatewayError
	var lastProvider string

	for _, cand := range plan.Candidates {
		lastProvider = cand.Provider.ID

		targetProto, passthrough, ok := cand.Provider.TargetFor(clientProto)
		if !ok {
			lastErr = ir.NewError(502, "no_supported_protocol",
				"provider "+cand.Provider.ID+" has no compatible upstream endpoint")
			continue
		}

		effectiveReq := irReq
		if !passthrough && irReq.PreviousResponseID != "" {
			prevMsgs, found := s.RespStore.Lookup(irReq.PreviousResponseID)
			if !found {
				lastErr = ir.NewError(400, "unsupported_previous_response_id",
					"previous_response_id requires direct passthrough to an OpenAI Responses backend or a cached gateway session")
				if plan.Forced {
					return attemptOutcome{ProviderID: cand.Provider.ID}, lastErr
				}
				continue
			}
			combined := make([]ir.Message, 0, len(prevMsgs)+len(irReq.Messages))
			combined = append(combined, prevMsgs...)
			combined = append(combined, irReq.Messages...)
			effectiveReq = irReq.CloneWithMessages(combined)
		}

		// Translated mode always calls upstream in streaming mode to avoid duplicate non-stream decoders.
		// Passthrough providers with Quirks.StreamFirst also stream upstream when the client asks for
		// stream=false, preventing header timeouts while still honoring passthrough semantics.
		streamCollect := false
		upstreamStream := effectiveReq.Stream
		if !passthrough {
			upstreamStream = true
		} else if !effectiveReq.Stream && cand.Provider.Quirks.StreamFirst && targetProto != ir.ProtoOpenAIResp {
			upstreamStream = true
			streamCollect = true
		}

		payload, buildErr := buildUpstreamPayload(clientProto, targetProto, passthrough, streamCollect, effectiveReq, rawBody, cand)
		if buildErr != nil {
			lastErr = buildErr
			if buildErr.HTTPStatus < 500 && buildErr.HTTPStatus != 404 && buildErr.HTTPStatus != 429 {
				return attemptOutcome{ProviderID: cand.Provider.ID}, buildErr
			}
			continue
		}

		endpointURL, urlErr := cand.Provider.EndpointURL(targetProto, cand.UpstreamModel, upstreamStream)
		if urlErr != nil {
			lastErr = ir.NewError(502, "invalid_endpoint", urlErr.Error())
			continue
		}

		outcome, committed, attemptErr := s.attemptWithKeys(
			w, r, clientProto, clientCodec, targetProto, passthrough, streamCollect, effectiveReq, cand, endpointURL, payload, plan.Forced,
		)
		if attemptErr == nil {
			return outcome, nil
		}
		lastErr = attemptErr
		if committed || attemptErr.HTTPStatus == 499 {
			// Bytes have already been flushed to the client, or client closed the connection.
			return outcome, lastErrOrNil(committed, lastErr)
		}
		action, _ := router.ClassifyGatewayError(attemptErr)
		if !action.Failover || plan.Forced {
			return outcome, lastErr
		}
	}

	if lastErr == nil {
		lastErr = ir.NewError(502, "all_attempts_failed", "all backend candidates failed")
	}
	return attemptOutcome{ProviderID: lastProvider}, lastErr
}

func lastErrOrNil(committed bool, err *ir.GatewayError) *ir.GatewayError {
	if committed {
		return nil
	}
	return err
}

func (s *Server) attemptWithKeys(
	w http.ResponseWriter,
	r *http.Request,
	_ ir.Protocol,
	clientCodec codec.Codec,
	targetProto ir.Protocol,
	passthrough bool,
	streamCollect bool,
	irReq *ir.Request,
	cand router.Candidate,
	endpointURL string,
	payload []byte,
	forced bool,
) (attemptOutcome, bool, *ir.GatewayError) {
	maxKeys := 1
	if cand.Provider.Keys != nil && cand.Provider.Keys.Count() > 1 {
		maxKeys = cand.Provider.Keys.Count()
	}

	triedKeys := make(map[string]bool, maxKeys)
	apiKey := ""
	if cand.Provider.Keys != nil {
		apiKey = cand.Provider.Keys.Acquire()
	}

	var lastErr *ir.GatewayError
	for keyTry := 0; keyTry < maxKeys; keyTry++ {
		if apiKey != "" {
			triedKeys[apiKey] = true
		}

		// Provider.Timeout bounds the wait for response headers (and a non-stream body); a stream is
		// bounded by its idle gap instead, so long replies are never cut at a fixed wall time.
		reqCtx, cancel := context.WithCancel(r.Context())
		var headerTimer *time.Timer
		if cand.Provider.Timeout > 0 {
			headerTimer = time.AfterFunc(cand.Provider.Timeout, cancel)
		}

		httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, bytes.NewReader(payload))
		if err != nil {
			if cancel != nil {
				cancel()
			}
			return attemptOutcome{ProviderID: cand.Provider.ID}, false, ir.NewError(500, "request_build_failed", err.Error())
		}
		cand.Provider.ApplyHeaders(httpReq, targetProto, apiKey)

		resp, err := s.Registry.HTTPClient().Do(httpReq)
		streamsBody := upstreamStreams(passthrough, streamCollect, irReq)
		if headerTimer != nil && (err != nil || streamsBody) {
			headerTimer.Stop()
		}
		if err != nil {
			if cancel != nil {
				cancel()
			}
			if errors.Is(r.Context().Err(), context.Canceled) {
				// Never punish upstream backends when the downstream client disconnects.
				return attemptOutcome{ProviderID: cand.Provider.ID}, false, ir.NewError(499, "request_canceled", "client canceled request")
			}
			ge := ir.NewError(502, "upstream_transport_error", err.Error())
			s.Cooldown.MarkFailure(cand.Provider.ID, ge.HTTPStatus, err.Error())
			return attemptOutcome{ProviderID: cand.Provider.ID}, false, ge
		}

		// Inspect non-2xx HTTP status before streaming/committing.
		hints := router.ParseRateLimitHeaders(resp.Header, time.Now())
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
			resp.Body.Close()
			if cancel != nil {
				cancel()
			}
			ge := codec.ParseUpstreamError(resp.StatusCode, errBody)
			action, classDur := router.ClassifyGatewayError(ge)
			keyDur := hints.RetryAfter
			if keyDur <= 0 {
				keyDur = classDur
			}
			if cand.Provider.Keys != nil && apiKey != "" {
				cand.Provider.Keys.MarkFailureDetailed(apiKey, resp.StatusCode, ge.Message, keyDur)
			}
			if action.RotateKey && cand.Provider.Keys != nil && keyTry+1 < maxKeys {
				if nextKey, rotated := cand.Provider.Keys.NextHealthyExcluding(triedKeys); rotated {
					apiKey = nextKey
					lastErr = ge
					continue
				}
			}
			if action.Cooldown && (!forced || resp.StatusCode >= 500) && (cand.Provider.Keys == nil || cand.Provider.Keys.HealthyCount() == 0) {
				s.Cooldown.MarkFailureWithRetry(cand.Provider.ID, resp.StatusCode, ge.Message, hints.RetryAfter)
			}
			return attemptOutcome{ProviderID: cand.Provider.ID}, false, ge
		}

		// 2xx status: run either passthrough or stream-collected/translated pipeline with pre-commit checks.
		var (
			outcome   attemptOutcome
			committed bool
			pipeErr   *ir.GatewayError
		)
		if passthrough && !streamCollect {
			outcome, committed, pipeErr = s.servePassthrough(w, resp, irReq, clientCodec, targetProto, cand, apiKey)
		} else {
			routeLabel := "translated:" + string(targetProto)
			if passthrough {
				routeLabel = "passthrough"
			}
			outcome, committed, pipeErr = s.serveTranslated(w, resp, irReq, clientCodec, targetProto, cand, apiKey, routeLabel)
		}
		resp.Body.Close()
		if cancel != nil {
			cancel()
		}

		if pipeErr == nil {
			route := "passthrough"
			if !passthrough {
				route = "translated:" + string(targetProto)
			}
			noteAttempt(r, cand.UpstreamModel, apiKey, route, &outcome)
			if cand.Provider.Keys != nil {
				if hints.ProactivelyExhausted && cand.Provider.Keys.Count() > 1 {
					cand.Provider.Keys.MarkExhaustedAndRotate(apiKey, hints.RetryAfter)
				} else {
					cand.Provider.Keys.MarkSuccess(apiKey)
				}
			}
			s.Cooldown.RecordSuccess(cand.Provider.ID)
			return outcome, committed, nil
		}
		if committed {
			return outcome, true, pipeErr
		}

		// Pre-commit stream/payload error (e.g. HTTP 200 wrapping {"error": ...}).
		action, classDur := router.ClassifyGatewayError(pipeErr)
		if router.IsStreamQualityError(pipeErr) || pipeErr.HTTPStatus == 499 {
			return outcome, false, pipeErr
		}
		if cand.Provider.Keys != nil && apiKey != "" {
			cand.Provider.Keys.MarkFailureDetailed(apiKey, pipeErr.HTTPStatus, pipeErr.Message, classDur)
		}
		if action.RotateKey && cand.Provider.Keys != nil && keyTry+1 < maxKeys {
			if nextKey, rotated := cand.Provider.Keys.NextHealthyExcluding(triedKeys); rotated {
				apiKey = nextKey
				lastErr = pipeErr
				continue
			}
		}
		if action.Cooldown && (!forced || pipeErr.HTTPStatus >= 500) && (cand.Provider.Keys == nil || cand.Provider.Keys.HealthyCount() == 0) {
			s.Cooldown.MarkFailure(cand.Provider.ID, pipeErr.HTTPStatus, pipeErr.Message)
		}
		return outcome, false, pipeErr
	}

	return attemptOutcome{ProviderID: cand.Provider.ID}, false, lastErr
}

func buildUpstreamPayload(
	clientProto ir.Protocol,
	targetProto ir.Protocol,
	passthrough bool,
	streamCollect bool,
	irReq *ir.Request,
	rawBody []byte,
	cand router.Candidate,
) ([]byte, *ir.GatewayError) {
	if passthrough {
		if clientProto == ir.ProtoGemini {
			return rawBody, nil
		}
		outBody := rawBody
		var err error
		if cand.UpstreamModel != "" && cand.UpstreamModel != irReq.Model {
			outBody, err = codec.RewriteModelJSON(outBody, cand.UpstreamModel)
			if err != nil {
				return nil, ir.NewError(400, "invalid_json", err.Error())
			}
		}
		if streamCollect {
			includeUsage := targetProto == ir.ProtoOpenAIChat &&
				cand.Provider.Quirks.IncludeUsageInStream &&
				!cand.Provider.Quirks.OmitStreamOptions
			outBody, err = codec.RewriteStreamOptionsJSON(outBody, true, includeUsage)
			if err != nil {
				return nil, ir.NewError(400, "invalid_json", err.Error())
			}
		}
		if clientProto == ir.ProtoOpenAIChat && targetProto == ir.ProtoOpenAIChat {
			outBody, err = codec.ApplyOpenAIChatQuirksJSON(outBody, cand.Provider.Quirks)
			if err != nil {
				return nil, ir.NewError(400, "invalid_json", err.Error())
			}
			outBody, err = codec.StripReasoningSamplingJSON(outBody, cand.UpstreamModel, cand.Provider.Quirks)
			if err != nil {
				return nil, ir.NewError(400, "invalid_json", err.Error())
			}
			if codec.IsDeepSeekTarget(cand.Provider.Preset, cand.Provider.ID, cand.UpstreamModel) {
				outBody, err = codec.ApplyDeepSeekChatJSON(outBody, cand.Provider.Quirks.EchoReasoningContent)
				if err != nil {
					return nil, ir.NewError(400, "invalid_json", err.Error())
				}
			}
		} else if clientProto == ir.ProtoAnthropic && targetProto == ir.ProtoAnthropic {
			if cand.Provider.Preset == "deepseek" || cand.Provider.ID == "deepseek" {
				outBody, err = codec.ApplyDeepSeekAnthropicJSON(outBody)
			} else if strings.Contains(strings.ToLower(cand.UpstreamModel), "claude") {
				outBody, err = codec.SanitizeClaudeThinkingJSON(outBody)
			}
			if err != nil {
				return nil, ir.NewError(400, "invalid_json", err.Error())
			}
		} else if clientProto == ir.ProtoOpenAIResp && targetProto == ir.ProtoOpenAIResp {
			outBody, err = codec.StripReasoningSamplingJSON(outBody, cand.UpstreamModel, cand.Provider.Quirks)
			if err != nil {
				return nil, ir.NewError(400, "invalid_json", err.Error())
			}
		}
		return outBody, nil
	}

	targetCodec, ok := codecs.For(targetProto)
	if !ok {
		return nil, ir.NewError(500, "missing_codec", "no codec registered for "+string(targetProto))
	}
	return targetCodec.BuildRequest(irReq, cand.UpstreamModel, true, cand.Provider.Quirks)
}

func setProvenanceHeaders(w http.ResponseWriter, cand router.Candidate, routeLabel string) {
	h := w.Header()
	h.Set("X-Nexus-Provider", cand.Provider.ID)
	h.Set("X-Nexus-Upstream-Model", cand.UpstreamModel)
	h.Set("X-Nexus-Route", routeLabel)
	if cand.Source != "" {
		h.Set("X-Nexus-Route-Source", cand.Source)
	}
}

func maskKeyLabel(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "local"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// upstreamStreams reports whether the upstream reply is an SSE stream (translated attempts and
// stream-collected passthrough always stream; plain passthrough mirrors the client).
func upstreamStreams(passthrough, streamCollect bool, irReq *ir.Request) bool {
	return !passthrough || streamCollect || irReq.Stream
}
