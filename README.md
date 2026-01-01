# OAuth-LLM-Nexus

**OAuth-LLM-Nexus** is a powerful, lightweight proxy server that bridges standard LLM clients (OpenAI, Anthropic, Google GenAI) with Google's internal "Cloud Code" API (Gemini). It allows you to use your Google account's free tier quotas to power your favorite AI tools like Claude Code, Cursor, generic OpenAI clients, and more.

## Features

-   **Multi-Protocol Support**:
    -   **OpenAI Compatible**: `/v1/chat/completions` (Works with Cursor, Open WebUI, etc.)
    -   **Anthropic Compatible**: `/anthropic/v1/messages` (Works with Claude Code, Aider, etc.)
    -   **Google GenAI Compatible**: `/genai/v1beta/models` (Works with official Google SDKs)
-   **Smart Proxying**: Automatically translates requests from standard formats to the internal Cloud Code API format.
-   **Account Pool Management**: Link multiple Google accounts to pool quotas and increase limits.
-   **Automatic Failover**: Automatically switches to the next available account if one hits a rate limit (429).
-   **Dashboard**: A built-in web dashboard to manage accounts, view usage, and get your API key.
-   **Secure**: API Key authentication for client access.

## Getting Started

### Prerequisites

-   Go 1.22+ (to build)
-   A Google Cloud Project with OAuth credentials configured.

### Installation

1.  **Clone the repository**:
    ```bash
    git clone https://github.com/yourusername/oauth-llm-nexus.git
    cd oauth-llm-nexus
    ```

2.  **Build the binary**:
    ```bash
    go build -o nexus ./cmd/nexus
    ```

3.  **Run the server**:
    ```bash
    # Set your OAuth credentials
    export GOOGLE_CLIENT_ID="your-client-id.apps.googleusercontent.com"
    export GOOGLE_CLIENT_SECRET="your-client-secret"
    
    # Optional: Set a custom port (default 8080)
    # export PORT=9090

    ./nexus
    ```

### Usage

1.  **Open the Dashboard**:
    Visit `http://localhost:8080` in your browser.

2.  **Link Account**:
    Click "Add Account" and sign in with your Google account. (You need to use the Google account that has access to Gemini/Cloud Code).

3.  **Get API Key**:
    Copy your API Key from the dashboard (`sk-xxxxxxxx...`).

4.  **Configure Clients**:

    **OpenAI SDK / Compatible Apps**:
    -   Base URL: `http://localhost:8080/v1`
    -   API Key: `sk-xxxxxxxx...`
    -   Model: `gpt-4o`, `gpt-3.5-turbo`, or `gemini-2.5-pro`

    **Anthropic / Claude Code**:
    -   Base URL: `http://localhost:8080/anthropic` (For some tools you might need to set `ANTHROPIC_BASE_URL`)
    -   API Key: `sk-xxxxxxxx...`
    -   Model: `claude-sonnet-4-5`

    **GenAI SDK**:
    -   Base URL: `http://localhost:8080/genai`
    -   API Key: `sk-xxxxxxxx...`

## Architecture

OAuth-LLM-Nexus sits between your AI clients and Google's internal API:

```mermaid
graph LR
    Client[Client Apps\n(Claude Code, Cursor)] -->|OpenAI/Anthropic Protocol| Proxy[OAuth-LLM-Nexus]
    Proxy -->|v1internal Protocol| Google[Google Cloud Code API]
    Proxy --OAuth Flow--> Users[Google Accounts]
```

## Contributing

Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.

## License

[MIT](https://choosealicense.com/licenses/mit/)
