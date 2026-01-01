# OAuth-LLM-Nexus

**OAuth-LLM-Nexus** 是一个功能强大且轻量级的代理服务器，它将标准的 LLM 客户端 API（OpenAI, Anthropic, Google GenAI）桥接到 Google 内部的 "Cloud Code" API (Gemini)。通过它，你可以利用 Google 账号的免费配额来驱动你最喜欢的 AI 工具，如 Claude Code, Cursor, 各类 OpenAI 客户端等。

## 功能特性

-   **多协议支持**：
    -   **OpenAI 兼容**：`/v1/chat/completions` (支持 Cursor, Open WebUI 等)
    -   **Anthropic 兼容**：`/anthropic/v1/messages` (支持 Claude Code, Aider 等)
    -   **Google GenAI 兼容**：`/genai/v1beta/models` (支持官方 Google SDK)
-   **智能代理**：自动将标准格式的请求转换为 Cloud Code API 所需的内部格式。
-   **账号池管理**：支持链接多个 Google 账号，通过配额池化来提高使用限制。
-   **自动故障转移**：如果当前账号触发速率限制 (429)，自动切换至下一个可用账号。
-   **管理面板**：内置 Web 仪表盘，用于管理账号、查看状态和获取 API Key。
-   **安全**：提供 API Key 认证机制。

## 快速开始

### 前置要求

-   Go 1.22+ (用于编译)
-   配置好 OAuth 凭证的 Google Cloud Project。

### 安装步骤

1.  **克隆仓库**：
    ```bash
    git clone https://github.com/yourusername/oauth-llm-nexus.git
    cd oauth-llm-nexus
    ```

2.  **编译二进制文件**：
    ```bash
    go build -o nexus ./cmd/nexus
    ```

3.  **运行服务器**：
    ```bash
    # 设置 OAuth 凭证
    export GOOGLE_CLIENT_ID="your-client-id.apps.googleusercontent.com"
    export GOOGLE_CLIENT_SECRET="your-client-secret"
    
    # 可选：设置端口 (默认 8080)
    # export PORT=9090

    ./nexus
    ```

### 使用方法

1.  **打开仪表盘**：
    在浏览器中访问 `http://localhost:8080`。

2.  **链接账号**：
    点击 "Add Account" 并登录你的 Google 账号。（需要使用有权访问 Gemini/Cloud Code 的 Google 账号）。

3.  **获取 API Key**：
    从仪表盘复制你的 API Key (`sk-xxxxxxxx...`)。

4.  **配置客户端**：

    **OpenAI SDK / 兼容应用**：
    -   Base URL: `http://localhost:8080/v1`
    -   API Key: `sk-xxxxxxxx...`
    -   Model: `gpt-4o`, `gpt-3.5-turbo`, 或 `gemini-2.5-pro`

    **Anthropic / Claude Code**：
    -   Base URL: `http://localhost:8080/anthropic` (部分工具可能需要设置 `ANTHROPIC_BASE_URL`)
    -   API Key: `sk-xxxxxxxx...`
    -   Model: `claude-sonnet-4-5`

    **GenAI SDK**：
    -   Base URL: `http://localhost:8080/genai`
    -   API Key: `sk-xxxxxxxx...`

## 架构

OAuth-LLM-Nexus 位于你的 AI 客户端和 Google 内部 API 之间：

```mermaid
graph LR
    Client[客户端应用\n(Claude Code, Cursor)] -->|OpenAI/Anthropic 协议| Proxy[OAuth-LLM-Nexus]
    Proxy -->|v1internal 协议| Google[Google Cloud Code API]
    Proxy --OAuth 流程--> Users[Google 账号]
```

## 贡献

欢迎提交 Pull Request。对于重大更改，请先开 issue 讨论您想要改变的内容。

## 许可证

[MIT](https://choosealicense.com/licenses/mit/)
