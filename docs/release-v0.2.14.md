# Release Draft: v0.2.14

## Type

Patch release.

## Highlights

- Dashboard now shows live support status for Codex and Gemini Vertex compatibility proxy.
- API Endpoints card now supports per-endpoint testing with fixed test models and structured test results.
- Request Monitor now covers:
  - `/v1/responses`
  - `GET /genai/v1beta/models`
  - `/v1beta/models/*`
- Request logs now include `provider` field and provider-aware search/display in monitor pages.
- Accounts UI now exposes `is_active` state with enable/disable controls.
- Promoting an account to primary now also activates it (`is_active=true`).
- `/api/test` now writes monitor logs when recording is enabled.

## API / Behavior Changes

- New admin API:
  - `GET /api/support-status`
- New admin API:
  - `POST /api/accounts/{id}/active`
- Updated admin API:
  - `GET /api/test?endpoint=<name>`
    - Supported values:
      - `openai_chat`
      - `openai_responses`
      - `anthropic_messages`
      - `genai_generate`
      - `vertex_generate`

## Data Model Changes

- `request_logs` table adds indexed column:
  - `provider` (text)

## Verification

Go tests:

```bash
GOCACHE=/tmp/go-build-cache go test ./internal/proxy/handlers -count=1
GOCACHE=/tmp/go-build-cache go test ./... -count=1
```

Python integration regression (`nexus_test`):

```bash
source /opt/homebrew/etc/oauth-llm-nexus.env
NEXUS_BASE_URL=http://127.0.0.1:8080
NEXUS_API_KEY=$(sqlite3 /opt/vault/projects/2026-acp/oauth-llm-nexus/nexus.db "select value from configs where key='api_key';")
UV_CACHE_DIR=/tmp/uv-cache uv run -- python -m pytest /opt/vault/projects/2026-acp/nexus_test/tests -q -rs
```

Observed result:

- `86 passed, 6 skipped` (expected web-search skip cases for `gemini-3-flash`).

## Release Steps

1. Merge feature branch to `main`.
2. Create and push tag:

```bash
git tag -a v0.2.14 -m "v0.2.14: dashboard and monitor observability enhancements"
git push origin main
git push origin v0.2.14
```

3. Create GitHub release using this draft.
4. Attach binaries from release workflow artifacts.
