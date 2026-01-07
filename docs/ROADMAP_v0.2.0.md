# OAuth-LLM-Nexus Roadmap

## Current Status: v0.1.20

### ✅ Completed Features (v0.1.x)

#### Multi-Protocol API Compatibility
- [x] **OpenAI Compatible**: `/v1/chat/completions`, `/v1/models`, `/v1/responses`
- [x] **Anthropic Compatible**: `/anthropic/v1/messages`, `/anthropic/v1/models`
- [x] **Google GenAI Compatible**: `/genai/v1beta/models`
- [x] **Auth**: Support both Header (`Authorization: Bearer`) and Query Param (`?key=`)

#### Model Management
- [x] **Dynamic Model Routes**: `/v1/models` returns models from `model_routes` config
- [x] **Default Mappings**: Claude 3.5 → Gemini, GPT-4 → Gemini

#### Tool Calling (v0.1.19)
- [x] **Claude on Vertex AI**: Full tool calling compatibility
- [x] **AnyOf → Enum Flattening**: JSON Schema conversion for Gemini
- [x] **FunctionCall/Response ID Injection**: Stateless thought signature

#### Reliability (v0.1.19 - v0.1.20)
- [x] **Streaming Reliability**: Status code checks + scanner error handling
- [x] **Panic Guards**: Defensive type assertions in `responses.go`
- [x] **Refresh Token Rotation**: RFC-compliant token persistence

### 🔨 Remaining / Future Work

#### P4: Verbose Logging Improvements
- [ ] Correlation IDs across request chain
- [ ] Raw bytes logging (instead of re-marshaled)
- [ ] Unified `NEXUS_VERBOSE` detection across all packages

#### v0.2.0+ Ideas
- [ ] WebSocket streaming support
- [ ] Multi-region failover
- [ ] Usage analytics dashboard
- [ ] Plugin system for custom transformations

