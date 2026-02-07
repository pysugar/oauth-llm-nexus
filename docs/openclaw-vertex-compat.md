# OpenClaw Vertex Compatibility Proxy

This document describes the transparent Gemini-compatible proxy endpoints for OpenClaw.

## Overview

When `NEXUS_VERTEX_API_KEY` is set, nexus automatically enables Gemini-compatible endpoints:

- `POST /v1beta/models/{model}:generateContent`
- `POST /v1beta/models/{model}:streamGenerateContent`
- `POST /v1beta/models/{model}:countTokens`

These endpoints accept Gemini-style requests and proxy them to Vertex:

- `/v1/publishers/google/models/{model}:generateContent`
- `/v1/publishers/google/models/{model}:streamGenerateContent`
- `/v1/publishers/google/models/{model}:countTokens`

## Environment Variables

- `NEXUS_VERTEX_API_KEY` (required to enable this feature)
- `NEXUS_VERTEX_BASE_URL` (optional, default: `https://aiplatform.googleapis.com`)
- `NEXUS_VERTEX_PROXY_TIMEOUT` (optional, Go duration format, default: `5m`)

## Security Model

- Client authentication still uses the nexus API key.
- Incoming `Authorization`, `x-goog-api-key`, and query `key` are stripped before upstream forwarding.
- Upstream requests always use server-side `NEXUS_VERTEX_API_KEY`.

## Example

```bash
curl "http://127.0.0.1:8080/v1beta/models/gemini-3-flash-preview:streamGenerateContent" \
  -X POST \
  -H "Authorization: Bearer sk-your-nexus-key" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {
        "role": "user",
        "parts": [{"text": "Explain how AI works in a few words"}]
      }
    ]
  }'
```
