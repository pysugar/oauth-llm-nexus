# Release Draft: v0.2.12

## Type

Patch release.

## Highlights

- Add Gemini-compatible Vertex transparent proxy for OpenClaw.
- Auto-enable `/v1beta/models/*` endpoints when `NEXUS_VERTEX_API_KEY` is set.
- Keep passthrough behavior (no model rewrite, no fallback).
- Add OpenClaw integration SOP and compatibility docs.
- Update README and README_CN with new compatibility endpoints and env vars.

## New Endpoints

- `POST /v1beta/models/{model}:generateContent`
- `POST /v1beta/models/{model}:streamGenerateContent`
- `POST /v1beta/models/{model}:countTokens`

## Environment Variables

- `NEXUS_VERTEX_API_KEY` (required to enable compatibility proxy)
- `NEXUS_VERTEX_BASE_URL` (default: `https://aiplatform.googleapis.com`)
- `NEXUS_VERTEX_PROXY_TIMEOUT` (default: `5m`)

## Verification

Run targeted tests:

```bash
GOCACHE=/tmp/go-build go test ./cmd/nexus ./internal/upstream/vertexkey ./internal/proxy/handlers -run 'Test(GeminiCompat|ParseGeminiCompatModelAction|Forward_.*|NewProviderFromEnv_DisabledWhenNoKey)'
```

Build check:

```bash
make build
```

## Release Steps

1. Commit changes.
2. Create and push tag:

```bash
git tag -a v0.2.12 -m "v0.2.12: add OpenClaw-compatible Vertex transparent proxy"
git push origin main
git push origin v0.2.12
```

3. Create GitHub release using this draft.
4. Attach binaries built from `release-*` Make targets.
