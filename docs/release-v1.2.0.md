# Release: v1.2.0

## Type

Minor release: native OpenAI Responses API passthrough for more backends, with safe multi-turn continuations.

## Highlights

- **Native Responses passthrough** for `deepseek`, `ollama`, `openrouter` and `amd` (verified live on
  2026-11-20; `nvidia` and Gemini's OpenAI-compatible API do not serve `/v1/responses` and keep translation).
- **Safe `previous_response_id`**: only `responses_stateful` providers (the `openai` preset) receive a raw
  `previous_response_id`, and only for a response they issued. Every other continuation is expanded from the
  gateway's `ResponseStore` into full input (Ollama and DeepSeek accept the id but silently drop the context;
  OpenRouter refuses it). Follows magpie's `prevKeeps` rule.
- **Complete stored history**: passthrough turns are stored with the whole conversation, fixing lost earlier turns
  after a failover from a stateful to a stateless backend.
- **Shape-refusal fallback**: a stateless backend's Responses API refusing an item type or field retries the same
  backend through Chat translation, remembered per provider/model.
- **Image detail**: `input_image.detail: "original"` is sent as `"high"` except to OpenAI and DeepSeek.

## Upgrade Notes

- Responses clients routed to DeepSeek, Ollama, OpenRouter or AMD now go native (`X-Nexus-Route: passthrough`)
  instead of `translated:openai-chat`. To keep translation for a provider, drop `openai-responses` from its
  `native` list in `providers.yaml`.
- New YAML field `responses_stateful` (default `true` only for the `openai` preset).
- `NEXUS_<ID>_BASE_URL` also moves the provider's Responses endpoint.

## Verification

```bash
go vet ./...
go test -count=1 -race ./...
make build
```

Live (`scripts/smoke/responses_smoke.py` through the gateway): DeepSeek, Ollama, OpenRouter, AMD and OpenAI —
single turn, `previous_response_id` continuation keeping context, streaming, function-call loop; NVIDIA
(translated) likewise.
