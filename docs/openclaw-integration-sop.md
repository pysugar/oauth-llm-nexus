# OpenClaw x Nexus Vertex Proxy Integration SOP

## Goal

Use OpenClaw `google` provider without exposing real Vertex API key to OpenClaw.

Architecture:

- OpenClaw (uses `GEMINI_API_KEY` = Nexus API key)
- Nexus (injects server-side `NEXUS_VERTEX_API_KEY`)
- Vertex API (`aiplatform.googleapis.com`)

## Prerequisites

- Nexus version includes Gemini compatibility proxy endpoints:
  - `POST /v1beta/models/{model}:generateContent`
  - `POST /v1beta/models/{model}:streamGenerateContent`
  - `POST /v1beta/models/{model}:countTokens`
- OpenClaw installed and running.

## Step 1: Configure Nexus (server side)

Set environment variables on the machine running Nexus:

```bash
export NEXUS_VERTEX_API_KEY="YOUR_REAL_VERTEX_KEY"
# Optional:
# export NEXUS_VERTEX_BASE_URL="https://aiplatform.googleapis.com"
# export NEXUS_VERTEX_PROXY_TIMEOUT="5m"
```

Start Nexus:

```bash
cd /opt/vault/projects/2026-acp/oauth-llm-nexus
go run ./cmd/nexus
```

Expected startup log:

- `Gemini compatibility proxy enabled (/v1beta/models/*)`

## Step 2: Get Nexus API key (client side secret for OpenClaw)

```bash
curl -s http://127.0.0.1:8080/api/config/apikey | jq -r .api_key
```

This key is what OpenClaw should use as `GEMINI_API_KEY`.

## Step 3: Verify Nexus endpoints directly

```bash
NEXUS_KEY="sk-xxxx"

curl "http://127.0.0.1:8080/v1beta/models/gemini-3-flash-preview:streamGenerateContent" \
  -X POST \
  -H "Authorization: Bearer ${NEXUS_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "contents":[{"role":"user","parts":[{"text":"Explain how AI works in a few words"}]}]
  }'
```

If this works, proxy path/auth is ready.

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

- `NEXUS_VERTEX_API_KEY` missing on Nexus process.
- Endpoint is auto-disabled when this variable is empty.

### OpenClaw still calls Google directly

- Check `models.providers.google.baseUrl` loaded correctly.
- Run `openclaw doctor` to confirm config validation.
- Ensure gateway process reads latest env/config (`~/.openclaw/.env` + restart).

## Security Checklist

- Real Vertex key exists only on Nexus host (`NEXUS_VERTEX_API_KEY`).
- OpenClaw only holds Nexus key (`GEMINI_API_KEY` = Nexus API key).
- Do not commit any real keys in config files.
