# Multi-Backend API Aggregation Gateway Design

## 1. Overview

`oauth-llm-nexus` now provides a Magpie-inspired 4x7 multi-protocol API aggregation gateway in `internal/gateway/` alongside existing legacy OAuth (`google`, `codex`, `vertex`) proxy flows. Any client speaking one of the **4 supported frontend protocols** can invoke models hosted on any of the **7 built-in API backends** (or custom YAML-configured backends) and switch backends at runtime without restarting clients.

```mermaid
flowchart LR
    C1["OpenAI Chat<br/>/v1/chat/completions"] --> GW
    C2["OpenAI Responses<br/>/v1/responses"] --> GW
    C3["Anthropic Messages<br/>/anthropic/v1/messages, /v1/messages"] --> GW
    C4["Google Gemini<br/>/v1beta/models/*"] --> GW

    GW["Gateway Server<br/>internal/gateway/server"] --> RT["Router + Policy + Cooldown<br/>internal/gateway/router"]
    RT -->|"same protocol"| PT["Passthrough Path"]
    RT -->|"cross-protocol"| IR["IR + Streaming Translator<br/>internal/gateway/ir + codecs"]
    RT -->|"legacy OAuth only"| LEG["Legacy Handlers<br/>(google / codex / vertex)"]

    PT --> KR["Provider KeyRing & Presets<br/>internal/gateway/provider"]
    IR --> KR

    KR --> B1["openai"]
    KR --> B2["openrouter"]
    KR --> B3["nvidia"]
    KR --> B4["amd (Token Factory)"]
    KR --> B5["ollama"]
    KR --> B6["deepseek"]
    KR --> B7["gemini"]
```

## 2. Unified Frontend Protocols (`internal/gateway/codec*`)

| Frontend Protocol | Codec Package | Endpoints | Notes |
| :--- | :--- | :--- | :--- |
| **OpenAI Chat Completions** | `internal/gateway/codec/openaichat` | `POST /v1/chat/completions`, `GET /v1/models` | Supports `messages`, `tools`, `tool_calls`, `reasoning_content`, `<think>` tag extraction (`ThoughtTagParsing`), and `usage` |
| **OpenAI Responses API** | `internal/gateway/codec/openairesp` | `POST /v1/responses` | Translates `input`, `instructions`, `function_call`/`function_call_output`, `reasoning.effort`; stateful `previous_response_id` passes through on OpenAI and bridges across translated backends via bounded TTL LRU `ResponseStore` (`internal/gateway/ir/respstore.go`) |
| **Anthropic Messages API** | `internal/gateway/codec/anthropic` | `POST /anthropic/v1/messages`, `POST /v1/messages`, `GET /anthropic/v1/models` | Supports `system`, `thinking` blocks (with signature cache `internal/gateway/ir/sigstore.go`), `tool_use`/`tool_result`, cache control usage |
| **Google Gemini API** | `internal/gateway/codec/gemini` | `GET /v1beta/models`, `GET /v1beta/models/{model}`, `POST /v1beta/models/{model}:generateContent`, `POST /v1beta/models/{model}:streamGenerateContent`, `POST /v1beta/openai/chat/completions` | Translates `contents`, `systemInstruction`, `functionDeclarations`/`functionCall`/`functionResponse`, `thinkingConfig` (`thinkingLevel` for Gemini 3 & 4), thought signatures; non-generation actions (`countTokens`, embeddings) forward through `KeyRing` with client `?key=` stripped before falling back to legacy |

### Unified Embeddings (`POST /v1/embeddings`, `POST /v1beta/openai/embeddings`)

OpenAI-compatible embeddings endpoint (`internal/gateway/server/embeddings.go`, `internal/gateway/router/embeddings.go`) sharing the same routing, key rotation, cooldown and pre-response failover as chat:

- **Resolution**: `X-Nexus-Provider` / `<provider>/<model>` prefix / DB `ModelRoute` aliases (e.g. `pkm-embed`) first; otherwise per-provider *embedding catalogs* (models detected by `IsEmbeddingModel` or Gemini `embedContent` support, kept apart from chat catalogs so they never pollute `/v1/models`). Vendor-namespaced IDs such as `nvidia/llama-3.2-nv-embedqa-1b-v1` are kept verbatim when the embedding catalog advertises them.
- **OpenAI-compatible backends** (`openai`, `nvidia`, `ollama`, `openrouter`, ...): passthrough to `{openai_chat}/embeddings` with the model rewritten.
- **Gemini backend**: translated to `:batchEmbedContents` (chunked at 100 inputs), `dimensions` → `outputDimensionality`, `encoding_format: base64` honored (little-endian float32, matching the OpenAI SDK default).
- **Retrieval hint**: optional `input_type` (alias `task_type`): `query` | `document`/`passage` | `similarity` | `classification` | `clustering`. Mapped to Gemini `taskType` (`RETRIEVAL_QUERY`/`RETRIEVAL_DOCUMENT`/...), forwarded as `query`/`passage` to backends with the `embedding_input_type` quirk (NVIDIA preset), stripped for strict OpenAI-compatible APIs.
- **Caveat**: vectors from different embedding models are not interchangeable. Only put the *same* model (served by different backends) into one failover alias, and pin the alias (`pinned`) once an index is built with it.

### Translation Strategy (`ir.Request` + Stream-First)

1. **Same-Protocol Passthrough**: When the chosen backend natively supports the incoming client protocol (`Provider.TargetFor(clientProto)` returns `passthrough == true`), only the model field (`RewriteModelJSON`) and auth/base headers are rewritten while preserving vendor extensions verbatim (including OpenAI Responses `previous_response_id`).
2. **Cross-Protocol Translation**: Incoming requests parse into canonical IR (`internal/gateway/ir`), normalize reasoning effort (`internal/gateway/ir/effort.go`) and cached thinking signatures (`SigStore`), encode into the upstream codec (always opening an SSE stream upstream via `internal/gateway/sse`), decode upstream chunks back into `ir.StreamEvent`, and either re-encode as client SSE events or aggregate into a complete non-streaming response via `ir.Collector`.

## 3. Supported Backends & Configuration (`config/providers.yaml`)

7 built-in presets are defined in `internal/gateway/provider/presets.go` and loaded by `internal/gateway/provider/config.go` (`config/providers.yaml`, plus fallback compatibility with `config/openai_compat_providers.yaml`):

| ID | Preset | Default Base URL(s) | Native Protocols | Auth Env Vars | Notes |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `openai` | OpenAI | `https://api.openai.com/v1` | `openai-chat`, `openai-responses` | `NEXUS_OPENAI_API_KEY` / `NEXUS_OPENAI_API_KEYS` | `MaxCompletionTokens` + stream usage |
| `openrouter` | OpenRouter | Chat: `https://openrouter.ai/api/v1`, Anthropic: `https://openrouter.ai/api` | `openai-chat`, `anthropic` | `NEXUS_OPENROUTER_API_KEY` / `NEXUS_OPENROUTER_API_KEYS` | `ThoughtTagParsing` quirk |
| `nvidia` | NVIDIA NIM | `https://integrate.api.nvidia.com/v1` | `openai-chat` | `NEXUS_NVIDIA_API_KEY` / `NEXUS_NVIDIA_API_KEYS` / `NVIDIA_API_KEY` | `StreamFirst`, `OmitStrictTools`, `ThoughtTagParsing` |
| `amd` | AMD Radeon Token Factory | Chat: `https://developer.amd.com.cn/radeon/api/v1`, Anthropic: `https://developer.amd.com.cn/radeon/api` | `openai-chat`, `anthropic` | `NEXUS_AMD_API_KEY` / `NEXUS_AMD_API_KEYS` / `AMD_API_KEY` | Nested `detail.error` parsing, `OmitStrictTools`, `ThoughtTagParsing` |
| `ollama` | Ollama Local | Chat: `http://localhost:11434/v1`, Anthropic: `http://localhost:11434` | `openai-chat`, `anthropic` | None (`no_key: true`) | 300s default timeout; override URL via `NEXUS_OLLAMA_BASE_URL` or `OLLAMA_BASE_URL` |
| `deepseek` | DeepSeek API | Chat: `https://api.deepseek.com/v1`, Anthropic: `https://api.deepseek.com/anthropic` | `openai-chat`, `anthropic` | `NEXUS_DEEPSEEK_API_KEY` / `NEXUS_DEEPSEEK_API_KEYS` / `DEEPSEEK_API_KEY` | `EchoReasoningContent`, `OmitReasoningEffort` |
| `gemini` | Google Gemini API | `https://generativelanguage.googleapis.com/v1beta` | `gemini` | `NEXUS_GEMINI_API_KEY` / `NEXUS_GEMINI_API_KEYS` / `GEMINI_API_KEY` | `x-goog-api-key` header |

Additional per-provider env overrides:
- `NEXUS_<ID>_ENABLED=true|false`
- `NEXUS_<ID>_BASE_URL` (or standard `<ID>_BASE_URL`, automatically normalizing `/v1` for OpenAI Chat and root URL for Anthropic)
- `NEXUS_<ID>_ANTHROPIC_BASE_URL`, `NEXUS_<ID>_RESPONSES_BASE_URL`, `NEXUS_<ID>_GEMINI_BASE_URL`
- `NEXUS_<ID>_API_KEY`, `NEXUS_<ID>_API_KEYS`, `<ID>_API_KEYS` (e.g. `GEMINI_API_KEYS`), or standard `<ID>_API_KEY` (`OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `NVIDIA_API_KEY`, `AMD_API_KEY`, `DEEPSEEK_API_KEY`, `GEMINI_API_KEY`)
- `NEXUS_<ID>_KEY_STRATEGY=round-robin|sticky` (`gemini` preset defaults to `round-robin`)
- `NEXUS_<ID>_INCLUDE_NON_CHAT=true|false`
- `NEXUS_<ID>_TIMEOUT` (e.g. `120s`)
- `NEXUS_PROVIDERS_FILE` (override path to `providers.yaml`)
- `NEXUS_CORS_ORIGINS` (opt-in CORS for inference APIs `/v1/*`, `/v1beta/*`, `/anthropic/*`; comma-separated origins, `scheme://host:*` port wildcards and `*` supported, e.g. `http://localhost:*,tauri://localhost`). Admin `/api/*` and `/ui` stay same-origin.

## 4. Routing Precedence, Failover, Key Rotation & Cooldown

### Resolution Order (`internal/gateway/router/router.go`)

1. **`X-Nexus-Provider` request header**: Forces execution against the named gateway provider (or legacy OAuth handler) with failover disabled (`Plan.Forced = true`).
2. **`<provider>/<model>` prefix (`shouldTreatAsProviderPrefix`)**: When the prefix before the first `/` matches a configured gateway or legacy provider ID, checks whether the full unstripped `<prefix>/<rest>` string is an explicit DB `ModelRoute` or a vendor-namespaced catalog model ID (such as `nvidia/nemotron-3.5-super-vl-120b-a12b` on `nvidia`, `nvidia/nemotron-3.5-super-vl:free` on `openrouter`, or `google/gemma-4-31b-it` on `nvidia`/`openrouter`). Only genuine provider prefixes (such as `amd/Gemma-4-31B-IT` or `openrouter/nvidia/nemotron-3.5-super-vl:free`) are stripped and pinned.
3. **Database `ModelRoute` + `ModelPolicy`**: Looks up active `ModelRoute` entries ordered by `(priority ASC, id ASC)` plus `ModelPolicy.Strategy` (`"failover"` default, or `"pinned"`).
   - `pinned`: Attempts only the primary route candidate (no automatic failover to secondary providers).
   - `failover`: Orders healthy non-cooled candidates first, placing candidates in cooldown after healthy ones so traffic fails over immediately when keys/rates degrade.
4. **Catalog Discovery (`HasExactModel` + `CanonicalModelID` Fallback)**: If no explicit DB `ModelRoute` exists (or only inactive legacy OAuth routes exist in failover mode), checks `internal/gateway/provider/catalog.go` (pre-warmed in background at startup, singleflight-deduplicated per provider) first for exact model ID matches, and when none match verbatim, falls back to canonical matches via `CanonicalModelID` (e.g., `deepseek-r1` matching `deepseek-reasoner`, `DeepSeek-R1`, `deepseek/deepseek-r1:free`).
   - **OpenRouter Free-Only Filtering**: By default (`free_only: true` in `openrouter` preset & `config/providers.yaml`), OpenRouter catalog discovery retains only zero-cost text models (e.g. `openrouter/free` and models with `0` prompt/completion/request/reasoning pricing and `"text"` output modality). Toggle at runtime via `PATCH /api/gateway/providers/openrouter`, `/ui`, or `NEXUS_OPENROUTER_FREE_ONLY=false` (explicit `ModelRoute` entries and `openrouter/<model>` prefixes continue to work regardless).
5. **Legacy OAuth Fallback**: If no gateway candidate applies and active legacy OAuth accounts exist (`Router.IsLegacyAvailable`), delegates to `google`, `codex`, or `vertex`.

### Key Rotation & Failure Handling

- **`KeyRing` (`internal/gateway/provider/keys.go`)**: Supports both `sticky` and `round-robin` (`key_strategy`, default `round-robin` for multi-key `gemini`, configurable via `NEXUS_<ID>_KEY_STRATEGY` and runtime PATCH), ingesting deduplicated keys across `NEXUS_<ID>_API_KEYS`, `<ID>_API_KEYS` (e.g., `GEMINI_API_KEYS`), `NEXUS_<ID>_API_KEY`, and `<ID>_API_KEY`. Tracks per-key health/telemetry (`StatusSnapshot`) and cooldown windows so exhausted/overloaded/invalid keys (`401`/`402`/`403`/`429`/`503`/`529` and Google `400 API_KEY_INVALID`) immediately relay to healthy siblings. Inspects `Retry-After` / `x-ratelimit-reset-*` and proactively rotates (`MarkExhaustedAndRotate`) when `x-ratelimit-remaining-requests: 0` is returned alongside HTTP 200.
- **`Cooldown` (`internal/gateway/router/cooldown.go`) & `ClassifyGatewayError` (`internal/gateway/router/classify.go`)**: Parses explicit reset headers (`ParseRateLimitHeaders`) to apply exact backoff windows (`MarkFailureWithRetry`) and classifies non-client errors (`402`, `503`/`529` short overload cooldown, `410` without backend poisoning, `408`/`5xx`) into cooldown only after all healthy keys fail before stream commitment. Client cancellations (`499`) never trigger backend cooldown, and pre-commit JSON error probing catches HTTP-200 non-SSE error payloads across both passthrough and translated streams.

### Stateful History, Model Guards & Always-On Telemetry

- **`CanonicalModelID` (`internal/gateway/provider/normalize.go`)**: Normalizes vendor namespaces (`deepseek/`, `deepseek-ai/`, `google/`, `meta-llama/`, etc.), routing suffixes (`:free`, `:latest`), casing, and known aliases (`deepseek-reasoner` → `deepseek-r1`) so `/api/gateway/models/matrix` and fallback catalog routing map one logical model cleanly across all supporting backends.
- **`ResponseStore` (`internal/gateway/ir/respstore.go`)**: Bounded TTL LRU (`2048` entries, `2h` TTL) caching OpenAI Responses API conversation turns so multi-turn `previous_response_id` flows work seamlessly when routed or failed over to non-OpenAI translated backends (`deepseek`, `amd`, `openrouter`, `ollama`, `gemini`).
- **Model Capability Filtering & Quirks**: `IsChatCapableModel` excludes non-chat models (e.g., `Clef`, `d3-flash`, `tts-*`, `*-embed*`, `nemoretriever`) from catalog discovery by default unless `include_non_chat: true` (`NEXUS_<ID>_INCLUDE_NON_CHAT=true`); `IsStrictReasoningModel` strips unsupported `temperature`/`top_p` sampling parameters on reasoning models (`o1`/`o3`/`o4`/`gpt-5*`) across both passthrough and translated paths; Gemini `buildThinkingConfig` emits `thinkingLevel` for both Gemini 3 and Gemini 4 (`carbon` / `hydrogen` / `argon`) models.
- **Always-On Metrics (`internal/gateway/server/metrics.go`)**: Accumulates per-provider and per-model requests, errors, average + EWMA latency, and tokens (`input`, `output`, `cached`, `reasoning`) even when full payload logging is disabled, extracting trailing usage chunks from streaming passthrough responses (`>512KB` safe 128KB rolling tail capture).

### Response Provenance Headers

Every gateway response includes diagnostic headers identifying the resolved execution path:
- `X-Nexus-Provider`: Backend provider ID selected (e.g., `openrouter`, `nvidia`, `amd`)
- `X-Nexus-Upstream-Model`: Exact target model ID sent upstream
- `X-Nexus-Route`: `"passthrough"`, `"translated:<target_protocol>"`, or `"legacy-oauth"`
- `X-Nexus-Route-Source`: `"header"`, `"prefix"`, `"db"`, `"catalog"`, or `"legacy-default"`

## 5. Runtime Switching Admin API & Dashboard

The `/api/gateway/*` endpoints (protected by `NEXUS_ADMIN_PASSWORD` when set) expose live state and instant runtime backend switching without client restarts:

| Method & Path | Purpose |
| :--- | :--- |
| `GET /api/gateway/providers` | List all gateway providers, per-key `key_statuses`, `key_strategy`, protocol capabilities, active cooldowns, live `stats`, and catalog counts |
| `PATCH /api/gateway/providers/{id}` | Mutate runtime provider settings (`enabled`, `free_only`, `include_non_chat`, `prefer`, `key_strategy`), add/remove API keys (`add_api_key`, `remove_api_key`), clear cooldowns, and refresh catalog. Changes are persisted to SQLite (`configs.gateway_provider_overrides`) and replayed at startup, taking precedence over `providers.yaml`/env; UI-added keys are stored verbatim in the local DB, removed env keys only as SHA-256 fingerprints |
| `DELETE /api/gateway/providers/{id}/overrides` | Forget persisted runtime overrides for `{id}` (config/env defaults apply after restart) |
| `POST /api/gateway/providers/{id}/refresh` | Refresh live upstream model catalog for provider `{id}` |
| `GET /api/gateway/stats` / `POST /api/gateway/stats/reset` | Read or reset lightweight always-on per-provider & per-model request/latency/token counters |
| `GET /api/gateway/models` | List unified models with active backend, exact + canonical candidates, and `failover`/`pinned` policy |
| `GET /api/gateway/models/matrix` | Inverted model-to-backends matrix grouped by `CanonicalModelID` (filterable by `?q=` and `?multi_only=true`) |
| `GET /api/gateway/models/{client}` | Inspect all supporting candidates (DB routes + canonical catalog matches) for a single model |
| `POST /api/gateway/select` / `POST /api/gateway/models/{client}/select` | Promote/upsert candidate backend to `priority = 1` at runtime (and optionally clear its cooldown) |
| `POST /api/gateway/models/{client}/failover` | Materialize all discovered supporting backends for `{client}` into priority-ordered failover DB routes |
| `PUT /api/gateway/policy` / `PUT /api/gateway/models/{client}/policy` | Toggle per-model strategy between `"failover"` and `"pinned"` |
| `POST /api/gateway/cooldown/clear` | Clear cooldowns (provider-level and per-key) for one provider or all providers |
| `POST /api/gateway/test` | Execute a live protocol compatibility probe (`openai-chat`, `openai-responses`, `anthropic`, `gemini`, supporting `stream` & `tools`) with optional pinned `provider` |

The standalone Magpie-inspired console at **`GET /ui`** (`internal/ui/`) consumes these endpoints alongside `/api/model-routes`, `/api/config/apikey`, and `/api/request-logs` to provide:
- Split-pane **Providers & Catalog** inspection with per-key health pills, `round-robin` / `sticky` key relay switch, live `Enabled` / `Free Only` / `Include Non-Chat` switches, preferred protocol selector, inline `+ Add` / `×` key management, real-time telemetry tiles, and inline model probing
- **Routing & Model Matrix** with dedicated `Model Matrix` tab listing all supporting backends per canonical model (`TargetModel`, native protocols, EWMA latency), one-click primary switching, and `⚡ Sync Failover Group`
- **4-Protocol Compatibility Tester** (`OpenAI Chat Completions`, `OpenAI Responses`, `Anthropic Messages`, and `Google Gemini`) across `Sync JSON`, `Stream SSE`, and `Tool Call` modes with full wire request/response inspection
- **Connect & Activity** with copyable `Shell` / `curl` / `Python SDK` snippets (including `@google/genai` & Gemini CLI) and expandable recent request logs

The legacy Dashboard at `/` (`internal/proxy/handlers/dashboard.go`) remains intact for existing OAuth account workflows.
