# OAuth-LLM-Nexus

[![Release](https://img.shields.io/github/v/release/pysugar/oauth-llm-nexus)](https://github.com/pysugar/oauth-llm-nexus/releases)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/license-Sustainable%20Use-green.svg)](LICENSE)

**OAuth-LLM-Nexus** 是一个强大的轻量级代理服务器，它能够将标准 LLM 客户端（OpenAI、Anthropic、Google GenAI）与 Google 内部的 "Cloud Code" API (Gemini) 连接起来。让你使用 Google 账号的免费配额来驱动你喜欢的 AI 工具，如 Claude Code、Cursor、通用 OpenAI 客户端等。

## ✨ 特性

-   **多协议支持**：
    -   **OpenAI 兼容**：`/v1/chat/completions`、`/v1/responses`（支持 Cursor、Open WebUI 等）
    -   **Anthropic 兼容**：`/anthropic/v1/messages`、`/v1/messages`（支持 Claude Code、Aider 等）
    -   **Google GenAI 兼容**：`/genai/v1beta/models`（支持官方 Google SDK）
    -   **Gemini API 统一前端 / 透明代理**：`/v1beta/models/*`（支持原样透传或跨后端协议转换）
    -   **Vertex AI 透明代理**：`/v1/publishers/google/models/*`（服务端注入 Vertex key）
    -   **Codex 适配（provider=codex）**：对外提供 OpenAI 风格 `/v1/chat/completions` 与 `/v1/responses`，采用 stream-first 策略
-   **多后端 API 聚合与运行时切换**：将 **OpenAI**、**OpenRouter**、**NVIDIA NIM**、**AMD Radeon Token Factory**、**Ollama**、**DeepSeek API** 与 **Google Gemini API** 统一接入 4 种客户端协议（`config/providers.yaml`），支持多 Key 轮询、指数冷却、有序故障转移 / 固定策略，以及通过 Dashboard 与 Admin API 实时切换后端（详见 [设计文档](docs/design_proposals/api-aggregation-gateway.md)）。
-   **统一检索接口**：`/v1/embeddings`（OpenAI 兼容后端直通、Gemini 自动翻译、`input_type` 检索提示）与 `/v1/rerank`（Cohere / Jina / Voyage 请求体），与聊天共用路由与故障转移；内置 Voyage AI 预设。
-   **用量、成本与花费上限**：持久化用量账本，按 [models.dev](https://models.dev)（或自定义价格）计算成本；`/ui` 新增「用量与成本」视图；支持按客户端 key、上游 key 设置每日/每月美元或 token 上限。
-   **可靠流式**：首个内容到达前先缓冲（期间发送协议正确的保活），空回复、截断、迟迟不出首字的上游都会自动故障转移；已提交后的截断以流内错误告知，不再伪造完成；可按 provider 设置 `first_token_timeout`。
-   **更聪明的路由**：`failover`、`pinned`、`rotate`、`usage`（最少使用）、`fastest` 五种策略；会话粘住持有提示缓存的后端；带图请求优先视觉模型；翻译到 Claude 的请求自动打提示缓存标记。
-   **隐私脱敏**（可选开启）：请求离开网关前把提示中的密钥与个人信息替换为占位符，回复中自动还原（含流式工具调用参数）；无法脱敏时直接拒绝发送。另支持声明式请求改写（`extra_body`、按模型追加系统提示）。
-   **按应用发放的客户端 Key**：带 provider / 模型白名单的命名 key（例如让笔记 App 只能访问本地或可信后端）。
-   **智能模型映射**：通过 Dashboard 配置客户端模型名到后端模型的路由。
-   **账号池管理**：链接多个 Google 账号以池化配额，提升限制。
-   **用户级配额路由**：使用 `X-Nexus-Account` 请求头将请求路由到指定账号，实现配额隔离。
-   **自动故障转移**：当一个账号触发速率限制 (429) 时，自动切换到下一个可用账号。
-   **仪表盘**：内置 Web 仪表盘，管理账号、模型路由、查看使用情况和获取 API Key。
-   **请求监控**：实时请求监控，包含详细日志、延迟追踪和错误分析。
-   **安全性**：API Key 认证保护客户端访问。
-   **Homebrew 支持**：通过 `brew tap` 轻松安装，支持服务管理。

📚 **快速入门**：[Claude Code 配置指南](docs/CLAUDE_CODE_SETUP.md) | [English](README.md) | [中文](README_CN.md)

📌 **OpenClaw 集成 SOP**：[docs/openclaw-integration-sop.md](docs/openclaw-integration-sop.md)

## 🖼️ 界面预览

### 仪表盘总览
**账号管理、API Key（默认脱敏）、模型路由**
![Dashboard 演示](docs/dashboard_fluent.webp)

### 请求监控
**实时请求历史与隐私脱敏**
![Monitor 演示](docs/monitor_fluent.webp)

> **🔒 隐私保护**：所有敏感信息（邮箱和 API Key）**默认脱敏显示**。鼠标悬停可查看完整内容。

## 🚀 安装

### 方式一：Homebrew (macOS/Linux)

```bash
# 添加 tap
brew tap pysugar/tap

# 安装
brew install oauth-llm-nexus

# 启动服务
brew services start oauth-llm-nexus
```

### 方式二：下载预编译二进制

从 [Releases](https://github.com/pysugar/oauth-llm-nexus/releases) 下载适合你平台的最新版本。

```bash
# macOS Apple Silicon
curl -LO https://github.com/pysugar/oauth-llm-nexus/releases/latest/download/nexus-darwin-arm64
chmod +x nexus-darwin-arm64
./nexus-darwin-arm64
```

### 方式三：从源码构建

```bash
git clone https://github.com/pysugar/oauth-llm-nexus.git
cd oauth-llm-nexus

# 使用 Make 构建 (自动注入版本号)
make build

# 或者手动构建
# go build -ldflags "-X github.com/pysugar/oauth-llm-nexus/internal/version.Version=dev" -o nexus ./cmd/nexus

./nexus
```

### 方式四：Docker

```bash
# 从 GitHub Container Registry 拉取
docker pull ghcr.io/pysugar/oauth-llm-nexus:latest

# 运行（先创建目录以确保权限正确）
mkdir -p ~/.oauth-llm-nexus
docker run -d \
  --name oauth-llm-nexus \
  -p 8086:8080 \
  -v ~/.oauth-llm-nexus:/home/nexus \
  ghcr.io/pysugar/oauth-llm-nexus:latest

# 或使用 Docker Compose
curl -O https://raw.githubusercontent.com/pysugar/oauth-llm-nexus/main/docker-compose.yml
docker-compose up -d
```

## ⚙️ 快速开始

直接运行二进制文件即可，大多数用户无需任何配置：

```bash
./nexus
```

服务器默认在 `127.0.0.1:8080` 启动。访问 `http://localhost:8080` 打开仪表盘。

### 环境变量

| 变量 | 默认值 | 说明 |
|:---------|:--------|:------------|
| `PORT` | `8080` (开发) / `8086` (发布) | 服务端口 |
| `HOST` | `127.0.0.1` | 绑定地址。设置为 `0.0.0.0` 可供局域网访问 |
| `NEXUS_MODE` | - | 设置为 `release` 启用生产模式（默认端口改为 8086） |
| `NEXUS_ADMIN_PASSWORD` | - | 可选密码，用于保护 Dashboard 和 API 端点 |
| `NEXUS_VERBOSE` | - | 设置为 `1` 或 `true` 启用详细的请求/响应日志 |
| `NEXUS_ADMIN_HOSTS` | - | 允许访问 `/ui`、`/api` 的额外主机名（逗号分隔，支持 `*.后缀`）；回环名与 IP 地址始终允许（防 DNS rebinding） |
| `NEXUS_CORS_ORIGINS` | - | 为 `/v1`、`/v1beta`、`/anthropic` 开启 CORS（如 `http://localhost:*,tauri://localhost`） |
| `NEXUS_MASKING` | 关闭 | 设为 `on` 后在请求离开网关前脱敏密钥与个人信息（也可用 `PUT /api/gateway/masking`） |
| `NEXUS_USAGE_RETENTION_DAYS` | `90` | 用量账本保留天数（`0` 为永久） |
| `NEXUS_MODELS_DEV` / `NEXUS_MODELS_DEV_URL` | 开启 / `https://models.dev/api.json` | 模型价格与上下文窗口数据源（`off` 关闭联网） |
| `NEXUS_<ID>_FIRST_TOKEN_TIMEOUT` | - | 上游迟迟不出首个 token 时故障转移（如 `20s`） |
| `NEXUS_<ID>_CACHE_TTL` | - | 设为 `1h` 时为翻译到 Claude 的请求使用一小时提示缓存 |
| `NEXUS_ANTHROPIC_ADAPTIVE_MODELS` | - | 额外指定只接受 adaptive thinking 的模型前缀 |
| `NEXUS_ANTIGRAVITY_USER_AGENT` | `antigravity/1.15.8 windows/amd64` | 覆盖上游 Antigravity User-Agent |
| `NEXUS_VERTEX_API_KEY` | - | 启用 Vertex 透明代理（`/v1/publishers/google/models/*`） |
| `NEXUS_VERTEX_BASE_URL` | `https://aiplatform.googleapis.com` | Vertex 上游基地址覆盖 |
| `NEXUS_VERTEX_PROXY_TIMEOUT` | `5m` | Vertex 兼容代理上游超时 |
| `NEXUS_GEMINI_API_KEYS` | - | 逗号分隔的多 key 列表，Gemini API 代理支持 429 自动切换 |
| `NEXUS_GEMINI_API_KEY` | - | Gemini API 透明代理单 key（`/v1beta/models/*`） |
| `GEMINI_API_KEY` | - | 当 `NEXUS_GEMINI_API_KEYS` 和 `NEXUS_GEMINI_API_KEY` 均未设置时的回退 key |
| `NEXUS_GEMINI_BASE_URL` | `https://generativelanguage.googleapis.com` | Gemini API 上游基地址覆盖 |
| `NEXUS_GEMINI_PROXY_TIMEOUT` | `5m` | Gemini API 透明代理上游超时 |

**示例：带密码保护的局域网共享**
```bash
export HOST=0.0.0.0
export PORT=8086
export NEXUS_ADMIN_PASSWORD=mysecret
./nexus
# 现在可以从局域网其他设备访问，需要密码验证
```

**示例：启用详细日志进行调试**
```bash
NEXUS_VERBOSE=1 ./nexus
# 日志将包含完整的请求体和 API 响应内容
```

### 协议兼容说明（重要）

-   **Codex 路径采用 stream-first**：当 `provider=codex` 时，优先保证流式兼容能力。
-   **Codex `/v1/responses` 行为**：底层是 responses 流式上游，建议客户端使用流式模式。当前实现中即使 `stream=false`，codex 也可能返回 SSE。
-   **Codex 参数过滤**：为避免上游 4xx，代理会过滤上游不支持参数（例如 `temperature`、`top_p`、`max_output_tokens`）。
-   **过滤透明化**：被过滤参数会通过响应头 `X-Nexus-Codex-Filtered-Params` 返回。
-   **Codex 过滤头语义**：仅当实际发生过滤时才返回 `X-Nexus-Codex-Filtered-Params`；未过滤时该响应头缺省。
-   **Responses 兼容标记**：`X-Nexus-Responses-Compat: request_id_smuggled` 表示 `conversation` / `previous_response_id` 已编码进 upstream `requestId` 并在最终响应中还原。
-   **Responses 非 2xx 错误映射**：`/v1/responses` 在 non-stream 与 stream preflight 场景下，都会将 upstream 非 2xx 归一为 OpenAI 风格错误结构（`error.message/type/code`）。
-   **Gemini-3 搜索**：在 Google antigravity upstream 下，Gemini-3 系列搜索按“明确不支持”处理（见 `docs/gemini-search-support.md`）。

#### Responses 兼容响应头矩阵

| Endpoint | 触发条件 | 响应头 | 含义 |
| --- | --- | --- | --- |
| `/v1/responses` | 请求包含 `conversation` 和/或 `previous_response_id`，且 requestId smuggling 成功 | `X-Nexus-Responses-Compat: request_id_smuggled` | 兼容字段通过 upstream `requestId` 透传，并已在 OpenAI 兼容响应中还原 |
| `/v1/responses` | 无兼容字段，或无需/未走 smuggling | _(无该头)_ | 未使用兼容字段 smuggling 路径 |

### 🔐 仪表盘安全

当设置了 `NEXUS_ADMIN_PASSWORD` 时，Dashboard 和 `/api/*` 端点会启用 HTTP Basic 认证：

- **用户名**：任意值（如 `admin`、你的邮箱，或留空）
- **密码**：`NEXUS_ADMIN_PASSWORD` 的值

如果未设置，Dashboard 可以无需认证直接访问（本地开发默认行为）。

### 💡 无头服务器/Docker 部署

> **⚠️ OAuth 限制**：Google 的 Antigravity OAuth 客户端只允许 `localhost` 回调。这意味着 OAuth 登录必须在运行 nexus 的机器上完成。这是 Antigravity OAuth 客户端的安全特性，而非 Bug。

**远程服务器或 Docker 容器部署步骤：**

1. **先在本地完成 OAuth**：
   ```bash
   # 在有浏览器的本地机器上
   ./nexus
   # 访问 http://localhost:8086，完成 OAuth 登录
   ```

2. **将数据库复制到服务器**：
   ```bash
   # 数据库包含你的认证会话
   scp nexus.db user@your-server:/path/to/nexus/
   
   # Docker 部署：先创建目录并设置正确权限
   mkdir -p ~/.oauth-llm-nexus
   cp nexus.db ~/.oauth-llm-nexus/
   # 如果目录被 root 创建，修复权限：
   # sudo chown -R $(id -u):$(id -g) ~/.oauth-llm-nexus/
   ```

3. **在服务器上启动 nexus**：
   ```bash
   # 原生方式
   HOST=0.0.0.0 NEXUS_ADMIN_PASSWORD=yourpassword ./nexus
   
   # Docker（数据库已在 ~/.oauth-llm-nexus/）
   docker-compose up -d
   ```

你的认证会话会被自动读取。Token 刷新在后台自动进行。

## 📖 使用方法

### 1. 打开仪表盘

在浏览器中访问 `http://localhost:8086`。

### 2. 链接账号

点击 "Add Account" 并使用你的 Google 账号登录（必须有 Gemini/Cloud Code 访问权限）。

### 3. 获取 API Key

从仪表盘复制你的 API Key (`sk-xxxxxxxx...`)。

### 4. 配置客户端

**OpenAI SDK / 兼容应用（Cursor、Continue 等）**：
```
Base URL: http://localhost:8086/v1
API Key: sk-xxxxxxxx...
Model: gpt-4o, gpt-4, 或 gemini-2.5-pro
```

**Anthropic / Claude Code**：
```bash
export ANTHROPIC_BASE_URL=http://localhost:8086/anthropic
export ANTHROPIC_API_KEY=sk-xxxxxxxx...
# Model: claude-sonnet-4-5, claude-3-5-sonnet, 等
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
    contents="你好，世界"
)
print(response.text)
```

**OpenClaw（通过 Nexus 使用 google provider）**：
```bash
# OpenClaw 运行环境：
# GEMINI_API_KEY 需要填写 Nexus API key（sk-...），不是真实上游 Gemini/Vertex key
export GEMINI_API_KEY="sk-your-nexus-key"

# OpenClaw 配置中：
# models.providers.google.baseUrl = "http://127.0.0.1:8080"
```

完整步骤见：[docs/openclaw-integration-sop.md](docs/openclaw-integration-sop.md)

## 🗺️ 模型映射

OAuth-LLM-Nexus 支持可配置的模型路由。通过 Dashboard 配置映射或编辑 `config/model_routes.yaml`：

```yaml
routes:
  - client: gpt-4o
    provider: google
    target: gemini-3-pro-high
  - client: claude-sonnet-4-5
    provider: google
    target: claude-sonnet-4-5
```

不在路由表中的模型会直接透传（如原生 Gemini 模型）。

## 🎯 用户级配额路由

默认情况下，所有请求使用 **Primary（主账号）** 的配额。你可以使用 `X-Nexus-Account` 请求头将特定请求路由到不同账号：

```bash
# 通过邮箱指定账号
curl -X POST http://localhost:8086/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" \
  -H "X-Nexus-Account: user@example.com" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "你好"}]}'
```

**使用场景**：
- **团队配额隔离**：为不同团队成员分配不同账号
- **项目级路由**：为不同项目使用独立账号  
- **速率限制管理**：跨多个账号分散高并发工作负载

| 请求头 | 值 | 描述 |
|:-------|:------|:------------|
| `X-Nexus-Account` | 邮箱或账号 ID | 路由到指定账号而非主账号 |

> **注意**：指定的账号必须已在 Dashboard 中链接且处于活跃状态。如果找不到，请求将返回 401 Unauthorized。

## 🛠️ 工具

### 快速配额检查

检查**本地已登录 Antigravity 账号**的实时配额（非 Nexus 中链接的账号）：

```bash
# 安装依赖
pip install requests

# 运行配额检查
python3 scripts/antigravity_quota.py

# 输出格式
python3 scripts/antigravity_quota.py --json    # JSON 输出
python3 scripts/antigravity_quota.py --raw     # 原始 API 响应
```

此脚本从本地 Antigravity 安装读取凭证，显示所有可用模型的精确配额百分比。

## 🏗️ 架构

```mermaid
graph LR
    Client["客户端应用<br/>(Claude Code, Cursor)"] -->|OpenAI/Anthropic 协议| Proxy[OAuth-LLM-Nexus]
    Proxy -->|v1internal 协议| Google[Google Cloud Code API]
    Proxy --OAuth 流程--> Users[Google 账号]
```

## 🍺 Homebrew 服务

如果通过 Homebrew 安装：

```bash
# 启动服务（开机自启）
brew services start oauth-llm-nexus

# 停止服务
brew services stop oauth-llm-nexus

# 查看日志
tail -f /opt/homebrew/var/log/oauth-llm-nexus.log
```

**自定义环境变量**：编辑 `$(brew --prefix)/etc/oauth-llm-nexus.env`：

```bash
# 创建/编辑环境文件
echo 'export NEXUS_VERBOSE="true"' >> $(brew --prefix)/etc/oauth-llm-nexus.env
echo 'export NEXUS_ADMIN_PASSWORD="yourpassword"' >> $(brew --prefix)/etc/oauth-llm-nexus.env

# 重启服务以应用
brew services restart oauth-llm-nexus
```

## 🌐 离线 / 受限环境

如果在隔离网络或防火墙受限环境中运行：

1. **模型路由**：下载 [`config/model_routes.yaml`](https://github.com/pysugar/oauth-llm-nexus/blob/main/config/model_routes.yaml) 并放置到以下位置之一：
   - `./config/model_routes.yaml`
   - `~/.config/nexus/model_routes.yaml`
   - `/etc/nexus/model_routes.yaml`

2. **仪表盘样式**：仪表盘使用 Tailwind CSS CDN。如果 CDN 被阻止，将显示基础样式的回退提示信息。

## 📝 API 端点

| 端点 | 协议 | 描述 |
|:---------|:---------|:------------|
| `GET /ui` | - | Gateway 控制台 UI（后端与模型、多后端矩阵、四协议测试台、接入与日志） |
| `GET /` | - | 经典仪表盘 UI（OAuth 账号与路由管理） |
| `POST /v1/chat/completions` | OpenAI | 聊天补全 |
| `POST /v1/responses` | OpenAI | Responses API（支持跨后端 `previous_response_id` 会话桥接） |
| `GET /v1/models` | OpenAI | 列出模型 |
| `GET /v1/codex/quota` | OpenAI | Codex 配额与账号信息 |
| `POST /anthropic/v1/messages` | Anthropic | Messages API（亦支持 `POST /v1/messages`） |
| `GET /anthropic/v1/models` | Anthropic | 列出 Claude 模型 |
| `POST /genai/v1beta/models/{model}:generateContent` | GenAI | 生成内容 |
| `POST /genai/v1beta/models/{model}:streamGenerateContent` | GenAI | 流式生成内容 |
| `GET /genai/v1beta/models` | GenAI | 列出可用模型 |
| `POST /v1/publishers/google/models/{model}:generateContent` | Vertex AI | 透明代理到 Vertex `generateContent` |
| `POST /v1/publishers/google/models/{model}:streamGenerateContent` | Vertex AI | 透明代理到 Vertex `streamGenerateContent` |
| `POST /v1/publishers/google/models/{model}:countTokens` | Vertex AI | 透明代理到 Vertex `countTokens` |
| `GET /v1beta/models` | Gemini API | 列出 Gemini API 模型 |
| `GET /v1beta/models/{model}` | Gemini API | 获取 Gemini API 模型详情 |
| `POST /v1beta/models/{model}:generateContent` | Gemini API | 生成内容 |
| `POST /v1beta/models/{model}:streamGenerateContent` | Gemini API | 流式生成内容 |
| `POST /v1beta/models/{model}:countTokens` | Gemini API | 计算 token |
| `POST /v1beta/models/{model}:embedContent` | Gemini API | 单条向量嵌入 |
| `POST /v1beta/models/{model}:batchEmbedContents` | Gemini API | 批量向量嵌入 |
| `POST /v1beta/openai/chat/completions` | Gemini API | `/v1beta` 下 OpenAI Chat 兼容入口 |
| `GET /api/accounts` | 内部 | 列出已链接账号 |
| `GET /api/model-routes` | 内部 | 列出模型路由 |
| `GET /api/gateway/providers` | 内部 | 列出聚合网关后端、每把 Key 健康状态、冷却、实时统计及缓存模型 |
| `PATCH /api/gateway/providers/{id}` | 内部 | 热更新后端开关（`enabled`/`free_only`/`include_non_chat`/`prefer`/`key_strategy`）与密钥池 |
| `POST /api/gateway/providers/{id}/refresh` | 内部 | 刷新指定后端的线上模型目录 |
| `GET /api/gateway/stats` / `POST /api/gateway/stats/reset` | 内部 | 查询或重置常驻多维度请求 / 延迟 / Token 统计 |
| `GET /api/gateway/models` | 内部 | 列出统一模型、当前主后端与 `failover`/`pinned` 策略 |
| `GET /api/gateway/models/matrix` | 内部 | 按归一化模型 ID 聚合列出所有支持的后端矩阵 |
| `POST /api/gateway/models/{client}/failover` | 内部 | 将指定模型支持的所有后端一键同步为多后端 Failover 链 |
| `POST /api/gateway/select` | 内部 | 运行时将客户端模型切换到指定后端 |
| `PUT /api/gateway/policy` | 内部 | 更新模型路由策略（`failover` \| `pinned`） |
| `POST /api/gateway/cooldown/clear` | 内部 | 清除后端及每把 Key 的冷却状态 |
| `POST /api/gateway/test` | 内部 | 四协议在线兼容性探针（Chat / Responses / Anthropic / Gemini） |
| `GET /monitor` | 内部 | 请求监控面板 |

### 请求与响应头

| 请求/响应头 | 方向 | 描述 |
|:-------|:---------|:------------|
| `Authorization` | 请求（必需） | API key，格式 `Bearer sk-xxx` |
| `X-Nexus-Provider` | 请求（可选） | 强制指定上游后端（如 `openai`、`openrouter`、`nvidia`、`amd`、`ollama`、`deepseek`、`gemini` 等） |
| `X-Nexus-Account` | 请求（可选） | 通过邮箱或 ID 路由到指定遗留 OAuth 账号 |
| `X-Request-ID` | 请求（可选） | 自定义请求 ID 用于追踪 |
| `X-Nexus-Provider` | 响应 | 实际命中的上游后端 ID |
| `X-Nexus-Upstream-Model` | 响应 | 实际发送到上游的模型名 |
| `X-Nexus-Route` | 响应 | 路由执行模式（`passthrough`、`translated:<proto>` 或 `legacy-oauth`） |
| `X-Nexus-Route-Source` | 响应 | 路由决策来源（`header`、`prefix`、`db`、`catalog`、`legacy-default`） |

## 🔀 多后端 API 聚合网关

`oauth-llm-nexus` 支持将 4 种客户端入口协议（`/v1/chat/completions`、`/v1/responses`、`/anthropic/v1/messages` 与 `/v1/messages`、`/v1beta/models/*`）统一路由或跨协议转换到以下 7 个内置后端（配置文件见 [`config/providers.yaml`](config/providers.yaml)，支持从 `NEXUS_<ID>_API_KEYS`、`<ID>_API_KEYS`（如 `GEMINI_API_KEYS`）、`NEXUS_<ID>_API_KEY`、`<ID>_API_KEY` 合并去重加载多把密钥，并支持 `round-robin` 轮询分摊与 `sticky` 接力及 `/ui` 多后端矩阵视图）：

| 后端 ID | 默认地址 | 密钥环境变量 | 原生协议 |
|:--------|:---------|:-------------|:---------|
| `openai` | `https://api.openai.com/v1` | `NEXUS_OPENAI_API_KEY` | `openai-chat`, `openai-responses` |
| `openrouter` | `https://openrouter.ai/api/v1` | `NEXUS_OPENROUTER_API_KEY` | `openai-chat`, `anthropic` |
| `nvidia` | `https://integrate.api.nvidia.com/v1` | `NEXUS_NVIDIA_API_KEY` | `openai-chat` |
| `amd` | `https://developer.amd.com.cn/radeon/api/v1` | `NEXUS_AMD_API_KEY` / `AMD_API_KEY` | `openai-chat`, `anthropic` |
| `ollama` | `http://localhost:11434/v1` | *（无需密钥）* | `openai-chat`, `anthropic` |
| `deepseek` | `https://api.deepseek.com/v1` | `NEXUS_DEEPSEEK_API_KEY` / `DEEPSEEK_API_KEY` | `openai-chat`, `anthropic` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta` | `NEXUS_GEMINI_API_KEYS` / `GEMINI_API_KEYS` / `GEMINI_API_KEY` | `gemini` |

更多关于 IR 协议转换架构、运行时切换与故障转移的细节请参阅 [`docs/design_proposals/api-aggregation-gateway.md`](docs/design_proposals/api-aggregation-gateway.md)。

## 🤝 贡献

欢迎提交 Pull Request。对于重大更改，请先开 Issue 讨论你想要更改的内容。

## 📄 许可证

[Sustainable Use License](LICENSE) - 仅供教育和研究用途。详见 LICENSE 文件。
