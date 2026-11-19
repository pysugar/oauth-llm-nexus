# Release: v1.0.0

## Type

Major release. The multi-protocol API aggregation gateway becomes the primary surface: one place to manage
API backends, with a unified API layer for agents and self-built apps.

## Highlights

**Unified API layer**
- `/v1/embeddings` (+ `/v1beta/openai/embeddings`) and `/v1/rerank` (+ `/rerank`) share chat's routing, key
  rotation and failover. Gemini embeddings are translated (`batchEmbedContents`, `dimensions`, base64); a
  provider-neutral `input_type` hint maps to Gemini `taskType`, NVIDIA `passage` and Voyage `document`.
- New Voyage AI preset (embeddings + rerank only; `no_chat` providers never serve chat).
- Named client keys with provider/model allowlists; `/ui` Client Keys panel.

**Reliability (protocol correctness)**
- Streams are held until first content with protocol-correct keepalives (OpenAI comment lines without a
  blank line, Anthropic `event: ping`, nothing for Gemini). Empty, errored, truncated or slow-starting replies
  fail over; post-commit truncation is an in-stream error, never a fabricated finish.
- The 180 s client-wide timeout that silently cut long streams is replaced by a response-header timeout and
  an upstream idle bound; per-provider `first_token_timeout`.
- Tool turns normalized for every upstream (late results, interleaved text, `is_error` placeholders instead of
  fake success, orphan results kept); deterministic tool-call ids for prompt-cache stability.
- Tool-returned images no longer break OpenAI (moved to a user message) or get lost on Gemini.
- Claude: adaptive thinking for 4.6+, thinking restored by tool-call id, foreign signatures dropped.
- DeepSeek passthrough fixes (`max_tokens`, `system` role, `reasoning_content` on tool turns; Anthropic
  endpoint ordering); stream tool-index reuse, `<think>` only at reply start, finish-reason consistency,
  Gemini schema allowlist and `MALFORMED_FUNCTION_CALL`.

**Cost & observability**
- Persisted usage ledger (`usage_records`) priced from models.dev (cached next to the DB) or user overrides
  (`/api/gateway/prices`); `GET /api/gateway/usage` and a `/ui` Usage & Cost view.
- OpenAI stream usage counted without changing the client stream.
- Daily/monthly USD or token spend caps per client key (429 `gateway_key_limit` + `Retry-After`) and per
  upstream key (skipped, then failover) via `/api/gateway/caps`.
- Anthropic prompt-cache marks on translated Claude requests (optional 1 h TTL); cache reads/writes reported.

**Routing**
- Strategies `rotate`, `usage`, `fastest` beside `failover`/`pinned`; cooling backends kept as last resort.
- Cache-aware conversation affinity; context-window and vision-aware candidate ordering.
- Classifier: context overflow never fails over; 400/422 fail over only for credit/quota/unserved/region.

**Security & operations**
- Admin guard against DNS rebinding and CSRF (`NEXUS_ADMIN_HOSTS`); `/api` is `no-store`.
- Runtime provider edits persist across restarts; opt-in CORS (`NEXUS_CORS_ORIGINS`) for inference paths.
- Graceful shutdown flushes the ledger; Docker healthcheck probes `/healthz`; image ships `providers.yaml`.

## Upgrade Notes

- Accessing `/ui` or `/api` under a hostname (not `localhost` or an IP) now needs `NEXUS_ADMIN_HOSTS`.
- Cooling backends are retried last instead of being skipped entirely.
- New SQLite tables are created automatically: `client_keys`, `model_prices`, `usage_records`, `spend_caps`.
- The server makes one outbound request a day to models.dev for pricing (`NEXUS_MODELS_DEV=off` to disable).

## Verification

```bash
go vet ./...
go test -count=1 -race ./...
make build
```

Live smoke against real upstreams (OpenAI, DeepSeek Chat/Anthropic, Gemini): OpenAI- and Anthropic-SDK
streaming and tool loops (translated and passthrough), Gemini embeddings (base64 default), ledger pricing,
client-key spend cap 429, admin guard.
