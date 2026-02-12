# OpenClaw x Nexus Gemini Proxy Integration SOP

## Goal

Use OpenClaw `google` provider without exposing real Gemini/Vertex API key to OpenClaw.

Architecture:

- OpenClaw (uses `GEMINI_API_KEY` = Nexus API key)
- Nexus (`/v1beta/models/*` -> Gemini API, `/v1/publishers/google/models/*` -> Vertex AI)
- Upstream API (`generativelanguage.googleapis.com` or `aiplatform.googleapis.com`)

## Prerequisites

- Nexus version includes Gemini transparent proxy endpoints:
  - Gemini API: `GET /v1beta/models`, `POST /v1beta/models/{model}:generateContent`
  - Vertex AI: `POST /v1/publishers/google/models/{model}:generateContent`
- OpenClaw installed and running.

## Step 1: Configure Nexus (server side)

Set environment variables on the machine running Nexus (choose one or both upstreams):

```bash
# Vertex upstream (optional)
export NEXUS_VERTEX_API_KEY="YOUR_REAL_VERTEX_KEY"
# export NEXUS_VERTEX_BASE_URL="https://aiplatform.googleapis.com"
# export NEXUS_VERTEX_PROXY_TIMEOUT="5m"

# Gemini API upstream (recommended for /v1beta/models/*)
export NEXUS_GEMINI_API_KEY="YOUR_REAL_GEMINI_KEY"
# or fallback:
# export GEMINI_API_KEY="YOUR_REAL_GEMINI_KEY"
# export NEXUS_GEMINI_BASE_URL="https://generativelanguage.googleapis.com"
# export NEXUS_GEMINI_PROXY_TIMEOUT="5m"
```

Start Nexus:

```bash
cd /opt/vault/projects/2026-acp/oauth-llm-nexus
go run ./cmd/nexus
```

Expected startup logs:

- `Gemini API proxy enabled (/v1beta/models/*)`
- `Vertex AI proxy enabled (/v1/publishers/google/models/*)` (if Vertex key is set)

## Step 2: Get Nexus API key (client side secret for OpenClaw)

```bash
curl -s http://127.0.0.1:8080/api/config/apikey | jq -r .api_key
```

This key is what OpenClaw should use as `GEMINI_API_KEY`.

## Step 3: Verify Nexus endpoints directly

```bash
NEXUS_KEY="sk-xxxx"

# Gemini API transparent proxy
curl "http://127.0.0.1:8080/v1beta/models/gemini-2.5-flash:generateContent" \
  -X POST \
  -H "Authorization: Bearer ${NEXUS_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "contents":[{"role":"user","parts":[{"text":"Explain how AI works in a few words"}]}]
  }'

# Vertex transparent proxy
curl "http://127.0.0.1:8080/v1/publishers/google/models/gemini-3-flash-preview:streamGenerateContent" \
  -X POST \
  -H "Authorization: Bearer ${NEXUS_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "contents":[{"role":"user","parts":[{"text":"Explain how AI works in a few words"}]}]
  }'
```

If these work, proxy path/auth is ready.

## Step 4: Configure OpenClaw to use Nexus

### 4.1 Put client key into OpenClaw env

If OpenClaw runs as daemon, put env in:

- `~/.openclaw/.env`

Add:

```bash
GEMINI_API_KEY=sk-your-nexus-api-key
```

### 4.2 Set default model to Google provider

In `~/.openclaw/openclaw.json`:

```json5
{
  agents: {
    defaults: {
      model: { primary: "google/gemini-3-flash-preview" }
    }
  }
}
```

### 4.3 Override Google provider base URL to Nexus

In the same config:

```json5
{
  models: {
    mode: "merge",
    providers: {
      google: {
        baseUrl: "http://127.0.0.1:8080",
        apiKey: "${GEMINI_API_KEY}"
      }
    }
  }
}
```

## Step 5: Restart OpenClaw gateway

Restart using your normal run mode (daemon/manual), then validate:

```bash
openclaw status
openclaw models list
```

Run one real query in OpenClaw and confirm Nexus receives `/v1beta/models/*` traffic.

## Troubleshooting

### 401 from Nexus

- `GEMINI_API_KEY` is wrong in OpenClaw.
- It must be Nexus API key, not Vertex key.

### 404 on `/v1beta/models/...`

- `NEXUS_GEMINI_API_KEY` and `GEMINI_API_KEY` are both missing.
- Gemini API endpoint is auto-disabled when neither key exists.

### 404 on `/v1/publishers/google/models/...`

- `NEXUS_VERTEX_API_KEY` missing on Nexus process.
- Vertex endpoint is auto-disabled when this variable is empty.

### OpenClaw still calls Google directly

- Check `models.providers.google.baseUrl` loaded correctly.
- Run `openclaw doctor` to confirm config validation.
- Ensure gateway process reads latest env/config (`~/.openclaw/.env` + restart).

## Security Checklist

- Real upstream keys only exist on Nexus host (`NEXUS_VERTEX_API_KEY`, `NEXUS_GEMINI_API_KEY`/`GEMINI_API_KEY`).
- OpenClaw only holds Nexus key (`GEMINI_API_KEY` = Nexus API key).
- Do not commit any real keys in config files.
