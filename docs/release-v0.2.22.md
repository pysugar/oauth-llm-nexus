# Release Draft: v0.2.22

## Type

Patch release.

## Highlights

- Split transparent proxy paths for Vertex and Gemini API:
  - Vertex: `/v1/publishers/google/models/{model}:{action}`
  - Gemini API: `/v1beta/models/{model}:{action}` and `/v1beta/models*`
- Add prefix-driven provider policy on model routes:
  - `gpt*` -> `codex/google`
  - `gemini*` -> `google/gemini/vertex`
  - `claude*` and unknown -> `google`
- Add protocol-aware route validation with strict `422` for invalid provider/protocol combinations.
- Extract reusable key-based transparent proxy core (`internal/upstream/keyproxy`) and reuse in Vertex and Gemini providers.
- Complete Vertex naming migration:
  - `GeminiCompat*` -> `VertexAIStudio*`
- Fix monitor consistency for invalid routes:
  - Anthropic invalid route logs now use `provider=invalid`.
- Fix `/api/test?endpoint=genai_generate` monitor fields to record real routed provider/mapped model.
- Lock provider enum to: `codex`, `google`, `vertex`, `gemini`.

## API / Behavior Notes

- Existing public API paths remain stable for OpenAI/Anthropic/GenAI compatibility handlers.
- Transparent proxy behavior:
  - Client keys are stripped.
  - Server-side key injection is enforced per upstream.
- Product semantic remains unchanged:
  - No active model route still falls back to `google`.
  - Manual account refresh can reactivate disabled accounts.

## Verification

Go tests:

```bash
cd /opt/vault/projects/2026-acp/oauth-llm-nexus
GOCACHE=/tmp/go-build-cache go test ./...
```

Transparent proxy contract tests (without Google antigravity suite):

```bash
cd /opt/vault/projects/2026-acp
NEXUS_BASE_URL=http://127.0.0.1:8080 \
NEXUS_API_KEY=$(sqlite3 /opt/vault/projects/2026-acp/oauth-llm-nexus/nexus.db "select value from configs where key='api_key';") \
UV_CACHE_DIR=/tmp/uv-cache \
uv run -- python -m pytest nexus_test/tests/test_gemini_proxy_mode.py nexus_test/tests/test_vertex_proxy_mode.py -q -rs -m "contract or stream_contract or capability_skip"
```

## Release Steps

1. Merge feature branch into `main`.
2. Create and push tag:

```bash
git tag -a v0.2.22 -m "v0.2.22: prefix provider policy and vertex/gemini transparent proxy hardening"
git push origin main
git push origin v0.2.22
```

3. Create GitHub release using this draft.
