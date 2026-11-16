# Magpie Best Practices Evaluation & Roadmap for `oauth-llm-nexus`

> Scope: API aggregation gateway (`internal/gateway/`, `internal/ui/`, `internal/db/`, `internal/proxy/monitor/`), excluding OAuth-to-API reverse-proxy internals.

## 1. Current Baseline vs. Magpie

| Capability Area | Already Implemented in `oauth-llm-nexus` | Gap Compared to `magpie` |
| :--- | :--- | :--- |
| **Core Protocol Translation** | 4x4 IR codecs (`openai-chat`, `openai-responses`, `anthropic`, `gemini`), `collector`, `effort` mapping, Gemini `thoughtSignature` LRU (`sigstore.go`) | Stateful `previous_response_id` session bridge for non-OpenAI backends; upstream slow-thinking SSE keepalive pings |
| **Provider & Quirks** | 7 built-in presets, `providers.yaml` + `.env` loading, `StreamFirst` (`nvidia`), `ApplyOpenAIChatQuirksJSON` (`max_completion_tokens` on passthrough), OpenRouter `free_only` filtering | Model-level capability/parameter guards (non-chat model filtering on AMD, `temperature` stripping on strict reasoning models); live UI editing of provider settings/keys |
| **Routing & Switching** | Priority-ordered `ModelRoute` candidates, `failover` & `pinned` policies, vendor-prefix disambiguation, runtime switching API & `/ui` | Load distribution strategies (`round-robin`, `weighted`, latency-aware `fastest`); conditional/rule routing (vision, context length, background/fast model) |
| **Key & Rate Governance** | `KeyRing` sticky rotation + per-key cooldown (`401/402/403/429`), provider exponential cooldown | Parsing `Retry-After` / `x-ratelimit-*` headers for exact cooldown windows & proactive rotation; multi-caller gateway keys; upstream key balance/quota probes |
| **Observability & Safety** | Optional request body logger (`ProxyMonitor`), `X-Nexus-*` provenance headers, `/api/gateway/test` 3-protocol probe | Streaming passthrough token usage extraction; always-on lightweight usage/latency counters decoupled from full body logging; secret/key redaction filter before untrusted/free upstreams |
| **Web Console (`/ui`)** | Magpie-styled `/ui` with 4 views (`Providers`, `Routing`, `Tester`, `Connect`), dark/light theme, `中文/EN`, inline & 3-protocol testing | Inline provider/key editor, `free_only` runtime toggle in UI, agent-specific onboarding snippets (Claude Code / Codex / Cursor) |

---

## 2. High-Value Practices Worth Introducing (Prioritized)

### P0: High ROI, Low-to-Medium Complexity (Implemented)

1. **[Implemented] Streaming Passthrough Usage Tee & Always-On Lightweight Metrics**
   - **Problem**: `servePassthrough` in `internal/gateway/server/passthrough.go` extracts token usage for non-streaming JSON responses (`extractJSONUsage`), but streaming passthrough (`irReq.Stream == true`) forwards raw SSE bytes without extracting the final `usage` / `usageMetadata` chunk. Moreover, when full payload logging is off, no per-provider/per-model token or latency metrics are accumulated.
   - **Implementation**: Added a `128KB` rolling SSE tail buffer in `internal/gateway/server/passthrough.go` (preserving usage extraction past `512KB` streams), extended `extractJSONUsage` with `CachedTokens` and `ReasoningTokens`, and added `internal/gateway/server/metrics.go` (`GET /api/gateway/stats`, `POST /api/gateway/stats/reset`, and per-provider `stats` in `GET /api/gateway/providers`) tracking requests, errors, average & EWMA latency, and token totals even when request body logging is off.

2. **[Implemented] Header-Aware Rate Limit Windows (`Retry-After` & `x-ratelimit-*`)**
   - **Problem**: `Cooldown.MarkFailure` in `internal/gateway/router/cooldown.go` uses a fixed exponential curve (`15s`, `30s`, `60s`...) regardless of whether the upstream explicitly sent `Retry-After: 5` or `x-ratelimit-reset-requests`.
   - **Implementation**: Added `ParseRateLimitHeaders` in `internal/gateway/router/classify.go`, `MarkFailureWithRetry` in `internal/gateway/router/cooldown.go`, and `MarkFailureWithDuration` / `MarkExhaustedAndRotate` in `internal/gateway/provider/keys.go` + `attempt.go`, honoring `Retry-After` / `Retry-After-Ms` / `x-ratelimit-reset-*` and proactively rotating keys when `x-ratelimit-remaining-requests: 0` appears on HTTP `200 OK`.

3. **[Implemented] Stateful `previous_response_id` Bridge for Non-OpenAI Backends**
   - **Problem**: Clients calling `POST /v1/responses` with `previous_response_id` currently only work on native `openai` passthrough; routing or failing over to `deepseek`, `amd`, `openrouter`, `ollama`, or `gemini` returns `400 unsupported_previous_response_id`.
   - **Implementation**: Added bounded TTL LRU `ResponseStore` in `internal/gateway/ir/respstore.go` (`2048` entries, `2h` TTL), updated `openairesp.EncodeResponse` / `respEncoder.ResponseID()`, `translate.go`, `passthrough.go`, and `attempt.go` so multi-turn `previous_response_id` chains replay conversation history seamlessly onto translated backends.

4. **[Implemented] Model Capability Filtering & Model-Level Parameter Sanitization**
   - **Problem**: Some provider catalogs include non-chat endpoints (e.g., AMD `Clef` and `d3-flash` require `/v1/systemone` and return HTTP 400 on `/v1/chat/completions`), and certain reasoning models reject `temperature` / `top_p`.
   - **Implementation**: Added `IsChatCapableModel` filtering in `internal/gateway/provider/catalog.go` (plus `IncludeNonChat` / `NEXUS_<ID>_INCLUDE_NON_CHAT`) and `IsStrictReasoningModel` + `StripReasoningSamplingJSON` in `internal/gateway/codec/codec.go`, applied across `openaichat`, `openairesp`, and passthrough requests in `attempt.go`.

5. **[Implemented] Runtime Provider Configuration & `Free Only` Toggle in `/ui`**
   - **Problem**: Toggling OpenRouter `free_only`, adding/removing an API key, or editing an endpoint URL currently requires editing `.env` / `config/providers.yaml` and restarting `nexus`.
   - **Implementation**: Added thread-safe `ApplyRuntimePatch` on `provider.Provider` and `PATCH /api/gateway/providers/{id}` in `internal/gateway/server/admin.go`, wired into `/ui` (`Providers` detail pane) with live `Enabled`, `Free Only`, and `Include Non-Chat` switches, `Preferred` protocol selector, inline `+ Add` key input, and real-time stats strip (`Reset Stats`).

---

### P1: Medium Complexity, Strong Operational Value

6. **Load-Distribution Strategies (`round-robin`, `weighted`, `fastest`)**
   - Extend `ModelPolicy.Strategy` beyond `failover` and `pinned` to support:
     - `round-robin`: Distribute requests evenly across healthy candidates (ideal for pooling multiple free/rate-limited backends such as `openrouter`, `nvidia`, and `amd`).
     - `fastest`: Route to the healthy candidate with the lowest exponentially weighted moving average (EWMA) latency.

7. **Upstream Slow-Start SSE Keepalive Heartbeats**
   - When streaming requests wait >10s for the first upstream token (common on cold-start local `ollama` models or queued `nvidia` NIM / `amd` reasoning models), emit periodic protocol-appropriate keepalive frames (`: keepalive\n\n` for OpenAI/Gemini, `event: ping` for Anthropic) to prevent client/proxy read timeouts.

8. **Agent-Specific Connect Presets in `/ui` (Claude Code, Codex CLI, Cursor)**
   - Expand the `/ui` Connect view with ready-to-paste configuration blocks for **Claude Code** (`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`), **Codex CLI** (`OPENAI_BASE_URL`, `OPENAI_API_KEY`), and **Cursor / Continue**.

---

### P2: Advanced Governance & Security

9. **Multi-Caller Gateway Keys & Per-Key Usage Quotas**
   - Allow issuing multiple named Nexus client keys (`sk-nexus-...`) for different local agents or LAN devices, tracking token usage per caller key with optional daily limits and loopback-trust toggle.

10. **Outbound Secret & PII Redaction Guard (`redact`)**
    - Before forwarding prompts to external or free tier providers (e.g., OpenRouter free models), optionally scan and mask secret patterns (`sk-...`, `ghp_...`, AWS keys, private keys) with reversible placeholders that are restored in the downstream response.

11. **Rule / Purpose-Based Conditional Routing**
    - Route requests dynamically based on request shape: e.g., route requests containing images (`ir.KindImage`) only to vision-capable candidates, route long-context requests (>64k tokens) to long-window backends, or route lightweight background/title requests to fast flash models.
