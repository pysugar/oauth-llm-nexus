# OAuth-LLM-Nexus

[![Release](https://img.shields.io/github/v/release/pysugar/oauth-llm-nexus)](https://github.com/pysugar/oauth-llm-nexus/releases)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

**OAuth-LLM-Nexus** is a powerful, lightweight proxy server that bridges standard LLM clients (OpenAI, Anthropic, Google GenAI) with Google's internal "Cloud Code" API (Gemini). It allows you to use your Google account's free tier quotas to power your favorite AI tools like Claude Code, Cursor, generic OpenAI clients, and more.

## ✨ Features

-   **Multi-Protocol Support**:
    -   **OpenAI Compatible**: `/v1/chat/completions` (Works with Cursor, Open WebUI, etc.)
    -   **Anthropic Compatible**: `/anthropic/v1/messages` (Works with Claude Code, Aider, etc.)
    -   **Google GenAI Compatible**: `/genai/v1beta/models` (Works with official Google SDKs)
-   **Smart Model Mapping**: Configurable routing from client model names to backend models via Dashboard.
-   **Account Pool Management**: Link multiple Google accounts to pool quotas and increase limits.
-   **Automatic Failover**: Automatically switches to the next available account if one hits a rate limit (429).
-   **Dashboard**: A built-in web dashboard to manage accounts, model routes, view usage, and get your API key.
-   **Secure**: API Key authentication for client access.
-   **Homebrew Support**: Easy installation via `brew tap` with service management.

📚 **Quick Start Guide**: [Claude Code Setup](docs/CLAUDE_CODE_SETUP.md) | [English README](README.md) | [中文文档](README_CN.md)

## 🖼️ Preview

<p align="center">
  <img src="docs/preview_01.png" width="600" alt="Dashboard Overview" />
</p>

<p align="center">
  <img src="docs/preview_02.png" width="600" alt="Config Inspector - Local Discovery" />
</p>

<p align="center">
  <img src="docs/preview_03.png" width="600" alt="Config Inspector - Config Reference" />
</p>

## 🚀 Installation

### Option 1: Homebrew (macOS/Linux)

```bash
# Add tap
brew tap pysugar/tap

# Install
brew install oauth-llm-nexus

# Start as service
brew services start oauth-llm-nexus
```

### Option 2: Download Binary

Download the latest release for your platform from [Releases](https://github.com/pysugar/oauth-llm-nexus/releases).

```bash
# macOS Apple Silicon
curl -LO https://github.com/pysugar/oauth-llm-nexus/releases/latest/download/nexus-darwin-arm64
chmod +x nexus-darwin-arm64
./nexus-darwin-arm64
```

### Option 3: Docker

```bash
# Pull from GitHub Container Registry
docker pull ghcr.io/pysugar/oauth-llm-nexus:latest

# Run with Docker
docker run -d \
  --name oauth-llm-nexus \
  -p 8086:8080 \
  -v nexus-data:/app/data \
  ghcr.io/pysugar/oauth-llm-nexus:latest

# Or use Docker Compose
curl -O https://raw.githubusercontent.com/pysugar/oauth-llm-nexus/main/docker-compose.yml
docker-compose up -d
```

### Option 4: Build from Source

```bash
git clone https://github.com/pysugar/oauth-llm-nexus.git
cd oauth-llm-nexus

# Build with Make (automatically injects version)
make build

# Or manual build
# go build -ldflags "-X github.com/pysugar/oauth-llm-nexus/internal/version.Version=dev" -o nexus ./cmd/nexus

./nexus
```

## ⚙️ Quick Start

Just run the binary - no configuration needed for most users:

```bash
./nexus
```

The server will start on `127.0.0.1:8080` by default. Visit `http://localhost:8080` to access the dashboard.

### Environment Variables

| Variable | Default | Description |
|:---------|:--------|:------------|
| `PORT` | `8080` (dev) / `8086` (release) | Server port |
| `HOST` | `127.0.0.1` | Bind address. Set to `0.0.0.0` for LAN access |
| `NEXUS_MODE` | - | Set to `release` for production (changes default port to 8086) |
| `NEXUS_ADMIN_PASSWORD` | - | Optional password to protect Dashboard and API endpoints |
| `NEXUS_VERBOSE` | - | Set to `1` or `true` to enable detailed request/response logging |

**Example: LAN Access with Password Protection**
```bash
export HOST=0.0.0.0
export PORT=8086
export NEXUS_ADMIN_PASSWORD=mysecret
./nexus
# Now accessible from other devices with password protection
```

**Example: Enable Verbose Logging for Debugging**
```bash
NEXUS_VERBOSE=1 ./nexus
# Logs will include full request bodies and API responses
```

### 🔐 Dashboard Security

When `NEXUS_ADMIN_PASSWORD` is set, the Dashboard and `/api/*` endpoints are protected by HTTP Basic Authentication:

- **Username**: Any value (e.g., `admin`, your email, or leave empty)
- **Password**: The value of `NEXUS_ADMIN_PASSWORD`

If not set, the Dashboard is accessible without authentication (default for local development).

### 💡 Deployment Tip: Headless/Cloud Server

Since OAuth-LLM-Nexus requires a browser for the initial Google login (which can be tricky on a headless Linux server), you can:

1.  Run `nexus` on your local PC/Mac first.
2.  Log in via the Dashboard (`http://localhost:8080`) to generate `nexus.db` with your tokens.
3.  Copy the `nexus.db` file to your Linux server.
4.  Run `nexus` on the server - it will pick up the existing valid session!

```bash
# Local
scp nexus.db user@your-server:/path/to/nexus/

# Server
export HOST=0.0.0.0
export NEXUS_ADMIN_PASSWORD=yourpassword
./nexus
```

## 📖 Usage

### 1. Open the Dashboard

Visit `http://localhost:8086` in your browser.

### 2. Link Account

Click "Add Account" and sign in with your Google account (must have access to Gemini/Cloud Code).

### 3. Get API Key

Copy your API Key from the dashboard (`sk-xxxxxxxx...`).

### 4. Configure Clients

**OpenAI SDK / Compatible Apps (Cursor, Continue, etc.)**:
```
Base URL: http://localhost:8086/v1
API Key: sk-xxxxxxxx...
Model: gpt-4o, gpt-4, or gemini-2.5-pro
```

**Anthropic / Claude Code**:
```bash
export ANTHROPIC_BASE_URL=http://localhost:8086/anthropic
export ANTHROPIC_API_KEY=sk-xxxxxxxx...
# Model: claude-sonnet-4-5, claude-3-5-sonnet, etc.
```

**Google GenAI SDK (v0.2+)**:
```python
from google import genai

client = genai.Client(
    api_key="sk-xxx",
    http_options={"base_url": "http://localhost:8086/genai"}
)

response = client.models.generate_content(
    model="gemini-3-flash", 
    contents="Hello world"
)
print(response.text)
```

## 🗺️ Model Mapping

OAuth-LLM-Nexus supports configurable model routing. Configure mappings via the Dashboard or edit `config/model_routes.yaml`:

```yaml
routes:
  - client: gpt-4o
    provider: google
    target: gemini-3-pro-high
  - client: claude-sonnet-4-5
    provider: google
    target: claude-sonnet-4-5
```

Models not in the routing table are passed through as-is (e.g., native Gemini models).

## 🏗️ Architecture

```mermaid
graph LR
    Client["Client Apps<br/>(Claude Code, Cursor)"] -->|OpenAI/Anthropic Protocol| Proxy[OAuth-LLM-Nexus]
    Proxy -->|v1internal Protocol| Google[Google Cloud Code API]
    Proxy --OAuth Flow--> Users[Google Accounts]
```

## 🍺 Homebrew Service

If installed via Homebrew:

```bash
# Start service (runs on boot)
brew services start oauth-llm-nexus

# Stop service
brew services stop oauth-llm-nexus

# View logs
tail -f /opt/homebrew/var/log/oauth-llm-nexus.log
```

**Note**: You need to configure OAuth credentials in the service environment. Edit the plist file or set environment variables in your shell profile.

## 🌐 Offline / Restricted Environment

If you're running in an air-gapped or firewall-restricted environment:

1. **Model Routes**: Download [`config/model_routes.yaml`](https://github.com/pysugar/oauth-llm-nexus/blob/main/config/model_routes.yaml) and place it in one of:
   - `./config/model_routes.yaml`
   - `~/.config/nexus/model_routes.yaml`
   - `/etc/nexus/model_routes.yaml`

2. **Dashboard Styles**: The dashboard uses Tailwind CSS CDN. If CDN is blocked, a fallback message will appear with basic styling.

## 📝 API Endpoints

| Endpoint | Protocol | Description |
|:---------|:---------|:------------|
| `GET /` | - | Dashboard UI |
| `POST /v1/chat/completions` | OpenAI | Chat completions |
| `GET /v1/models` | OpenAI | List models |
| `POST /anthropic/v1/messages` | Anthropic | Messages API |
| `POST /genai/v1beta/models/{model}:generateContent` | GenAI | Generate content |
| `GET /api/accounts` | Internal | List linked accounts |
| `GET /api/model-routes` | Internal | List model routes |

## 🤝 Contributing

Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.

## 📄 License

[Apache License 2.0](LICENSE)
