# OAuth-LLM-Nexus v0.2.0 Roadmap: API Compliance & Standardization

## 1. Context & Motivation

Current users (using tools like Zed AI, Cursor, etc.) are encountering issues where:
1.  **Unsupported Endpoints**: 
    - `/anthropic/v1/models` returns 404 (Used by some Claude clients).
    - `/v1/responses` returns 404 (Used by Zed AI for agentic flows).
2.  **Auth Issues**: GenAI SDK uses `?key=...` query param which we don't currently support (only headers).
3.  **Static Models**: `/v1/models` returns a hardcoded list, ignoring custom `model_routes`.

## 2. Development Phase 1: v0.1.3 Immediate Fixes (Hotfix)

Target: Resolve current 404/401 errors.

-   **GenAI Auth Compatibility**:
    *   Fix: Middleware must support `?key=` query parameter (standard Google API style).
-   **Anthropic Models Endpoint**:
    *   Fix: Implement `GET /anthropic/v1/models`. Although not in official public docs, clients assume it exists. Return a simple list of supported Claude models.
-   **Default Model Routes**:
    *   Update `model_routes.yaml` with requested mappings:
        *   `claude-3-5-haiku-latest` -> `gemini-3-flash`
        *   `claude-3-5-sonnet-latest` -> `gemini-3-flash`
        *   `claude-3-5-opus-latest` -> `gemini-3-flash`

## 3. Development Phase 2: v0.2.0 Core Architecture (Feature)

Target: Full API compliance and dynamic configuration.

-   **Dynamic Models API (`/v1/models`)**:
    *   **Goal**: `/models` should return the exact list of "Client Model Names" configured in `Model Routes`.
    *   **Logic**: Query `model_routes` table -> Return keys.
    *   **Client Awareness**: If accessed via `/anthropic/v1/...`, return Anthropic-compatible format.
-   **Standardize `/v1/responses`**:
    *   Implement as a compatibility proxy.
    *   Goal: Solve Zed AI Agent mode issues.

## 4. Task List

### v0.1.3 (Hotfix)
- [ ] **Auth**: Update `middleware/auth.go` to check `key` query param.
- [ ] **Feat**: Add `ClaudeModelsHandler` for `GET /anthropic/v1/models`.
- [ ] **Config**: Add new Claude 3.5 mappings to `model_routes.yaml` defaults.
- [ ] **Core**: Inject Database into Model Handlers.
- [ ] **Feat**: Implement `DynamicModelListHandler` reading from `model_routes`.
- [ ] **Feat**: Create `OpenAIResponsesHandler` for `/v1/responses`.
- [ ] **Docs**: Update API documentation.
