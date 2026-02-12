# Release Draft: v0.2.23

## Type

Patch release.

## Highlights

- Add config-driven OpenAI-compatible provider registry:
  - `config/openai_compat_providers.yaml`
  - runtime catalog loader with env override support.
- Introduce a single generic OpenAI-compatible proxy handler:
  - `POST /{provider}/v1/chat/completions`
  - reused by `/v1/chat/completions` when `model_routes` resolves to an OpenAI-compatible provider.
- Add first OpenAI-compatible providers:
  - `openrouter` (all model prefixes)
  - `nvidia` (unknown-prefix models only).
- Extend provider policy to include catalog-scoped providers without hard-coding new providers.
- Add dashboard provider integration visibility and probes:
  - support status badges for OpenRouter/NVIDIA
  - `/api/test` probes: `openrouter_generate` and `nvidia_generate`.
- Expand endpoint test matrix with fixed probe models:
  - OpenRouter: `openrouter/free`
  - NVIDIA: `z-ai/glm4.7`.
- Increase dashboard horizontal container width for better long-path readability.

## API / Behavior Notes

- Existing Vertex/Gemini transparent proxy behavior remains unchanged.
- Existing OpenAI/Anthropic/GenAI/Codex paths remain backward compatible.
- New explicit OpenAI-compatible path:
  - `POST /{provider}/v1/chat/completions`
- `/v1/responses` is not enabled for OpenAI-compatible catalog providers in this release.
- Regression scope keeps excluding Google antigravity provider tests.

## Verification

Go tests:

```bash
cd /opt/vault/projects/2026-acp/oauth-llm-nexus
GOCACHE=/tmp/go-build-cache go test ./...
```

Transparent proxy contract tests:

```bash
cd /opt/vault/projects/2026-acp
uv run -- python -m pytest nexus_test/tests/test_vertex_proxy_mode.py nexus_test/tests/test_gemini_proxy_mode.py nexus_test/tests/test_openrouter_proxy_mode.py nexus_test/tests/test_nvidia_proxy_mode.py -q -rs -m "contract or stream_contract or capability_skip"
```

## Release Steps

1. Merge feature branch into `main`.
2. Create and push tag:

```bash
git tag -a v0.2.23 -m "v0.2.23: add config-driven openai-compatible providers and dashboard provider probes"
git push origin main
git push origin v0.2.23
```

3. Create GitHub release using this draft.
