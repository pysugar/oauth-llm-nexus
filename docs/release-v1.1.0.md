# Release: v1.1.0

## Type

Minor release. Both features are off unless configured; with masking off, requests and responses pass
through byte-for-byte as in v1.0.0.

## Highlights

**Privacy masking** (`internal/gateway/redact`, `docs/design_proposals/api-aggregation-gateway.md` §6)
- Secrets and personal data (private keys, API keys, JWTs, URL passwords, secret-named assignments and auth
  headers, emails, bank cards, PRC ID numbers) are replaced by deterministic placeholders
  `{{KIND_xxxxxxxx}}` (HMAC-SHA256, key `nexus.redact.key` next to the database) before a request leaves the
  gateway, and restored in the reply: non-stream JSON, streamed text and streamed tool-call arguments
  (placeholders split across SSE events are held and completed). Prompt caches keep hitting because the same
  value always gets the same placeholder.
- Fail closed: masking on but unable to run → `503 masking_unavailable`, nothing is sent.
- Per-kind modes `mask` / `warn` / `block` / `off`, value or `@domain` allowlist, preview endpoint.
- Per-provider `unredacted` sends the original body to trusted backends (default for the `ollama` preset);
  embeddings are masked only with `mask_embeddings: true`.
- Enable with `NEXUS_MASKING=on` or `PUT /api/gateway/masking {"enabled": true}`;
  `POST /api/gateway/masking/preview {"text": …}` shows what would be sent.

**Request transforms**
- Per-provider `extra_body` (fills missing top-level fields; `"!field"` replaces) and per-model
  `system_prompts` appended to the request, in `providers.yaml` or live via
  `PATCH /api/gateway/providers/{id}` (persisted). Applied after masking.

## Upgrade Notes

- Ollama `:cloud` models are served by Ollama's cloud even though the `ollama` provider is local. Because
  the `ollama` preset defaults to `unredacted: true`, they receive unmasked text when masking is on; set
  `unredacted: false` for the `ollama` provider if you use cloud models through it.
- New file `nexus.redact.key` (0600) is created next to the database the first time masking is enabled; keep
  it with backups so placeholders stay stable across restarts.

## Verification

```bash
go vet ./...
go test -count=1 -race ./...
make build
```

Live smoke with masking on against real upstreams: streamed restore through Gemini (translated), DeepSeek
confirming it only saw a placeholder while the client got the real value, streamed Anthropic tool-call
arguments restored (DeepSeek Anthropic endpoint), preview endpoint; DeepSeek per-model system prompt
applied live via PATCH.
