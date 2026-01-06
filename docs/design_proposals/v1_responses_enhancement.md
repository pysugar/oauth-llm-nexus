# v0.1.6+ Design Proposal: Enhanced Gemini Integration for /v1/responses

基于对 `litellm` 和当前 Nexus 代码库的深入分析，本文档提出 `/v1/responses` 和 `/v1/chat/completions` endpoints 的增强方案，全面支持 **System Instructions**、**Thinking Mode (CoT)**、以及 **Tools (Function Calling & Grounding)**。

## 1. System Instructions

**状态**: ✅ 已实现
当前 `OpenAIToGemini` 已提取 `role: "system"` messages 并转换为 `systemInstruction`。

**细化建议**:
- 确保多条 system message 正确拼接。
- 确认 `system_instruction` 字段正确置于 `GeminiRequestPayload` 顶层。
- **LiteLLM 参考**: 提取所有 system messages，从 conversation history 中移除，并作为单独的 `systemInstruction` 对象发送。

## 2. Thinking Mode (CoT) & Reasoning Effort

**状态**: ❌ 未实现
Gemini 2.x/3.x 模型通过 `thinkingConfig` 支持 CoT 推理。

### API 接口设计

支持标准 OpenAI `reasoning_effort` 和 Anthropic 风格 `thinking` 参数：

```json
// OpenAI o1 style
{
  "model": "gemini-2.0-flash-thinking",
  "messages": [...],
  "reasoning_effort": "high" // minimal, low, medium, high
}

// Anthropic style (via extra_body for SDK compatibility)
{
  "model": "gemini-2.0-flash-thinking",
  "messages": [...],
  "thinking": {
    "type": "enabled",
    "budget_tokens": 1024
  }
}
```

### 内部实现

**Go 结构体更新**:

```go
type GeminiGenerationConfig struct {
    // ... existing fields
    ThinkingConfig *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type GeminiThinkingConfig struct {
    IncludeThoughts bool   `json:"includeThoughts"`
    ThinkingBudget  *int   `json:"thinkingBudget,omitempty"` // For Gemini 2.x
    ThinkingLevel   string `json:"thinkingLevel,omitempty"`  // For Gemini 3+ (minimal, low, high)
}
```

### 映射逻辑 (参考 LiteLLM)

**Gemini 2.x** (使用 `thinkingBudget`):
- `low` → `thinkingBudget: 8192`
- `medium` → `thinkingBudget: 16384`
- `high` → `thinkingBudget: 65536`

**Gemini 3+** (使用 `thinkingLevel`):
- `minimal` → `thinkingLevel: "minimal"`
- `low` → `thinkingLevel: "low"`
- `high` → `thinkingLevel: "high"`

**模型检测**:
```go
func isGemini3OrNewer(model string) bool {
    return strings.Contains(model, "gemini-3")
}
```

## 3. Tools & Grounding (重点)

**状态**: ⚠️ 仅警告日志
当前传递 `tools` 仅触发 warning，无实际功能。

### 3.1 API 接口设计

#### 方案 A: 标准 Function Calling

```json
{
  "model": "gemini-2.0-flash",
  "messages": [{...}],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get weather forecast",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {"type": "string"}
          }
        }
      }
    }
  ]
}
```

#### 方案 B: Google Search Grounding (联网搜索)

**方式 1: 通过特殊 tool 名称触发**
```json
{
  "tools": [
    {
      "type": "function",
      "function": { "name": "google_search" }
    }
  ]
}
```

**方式 2: 通过 `web_search_options` 参数 (推荐)**
```json
{
  "model": "gemini-2.0-flash",
  "messages": [{...}],
  "web_search_options": {}
}
```

**LiteLLM 实现参考**:
```python
def _map_web_search_options(self, value: dict) -> Tools:
    """Base Case: empty dict
    Google doesn't support user_location or search_context_size params"""
    return Tools(googleSearch={})
```

### 3.2 Go 结构体扩展

#### Request Structs

```go
type GeminiRequestPayload struct {
    Contents          []GeminiContent         `json:"contents"`
    SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"`
    GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
    Tools             []GeminiTool            `json:"tools,omitempty"`           // NEW
    ToolConfig        *GeminiToolConfig       `json:"toolConfig,omitempty"`     // NEW
}

type GeminiTool struct {
    FunctionDeclarations  []GeminiFunctionDeclaration `json:"function_declarations,omitempty"`
    GoogleSearch          *struct{}                   `json:"googleSearch,omitempty"`
    GoogleSearchRetrieval *struct{}                   `json:"googleSearchRetrieval,omitempty"`
    CodeExecution         *struct{}                   `json:"codeExecution,omitempty"`
}

type GeminiFunctionDeclaration struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    Parameters  map[string]interface{} `json:"parameters"` // OpenAPI Schema
}

type GeminiToolConfig struct {
    FunctionCallingConfig *GeminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type GeminiFunctionCallingConfig struct {
    Mode                 string   `json:"mode"`                         // AUTO, ANY, NONE
    AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}
```

#### Response Structs (Grounding Metadata)

```go
type GeminiGroundingMetadata struct {
    GroundingSupports []GeminiGroundingSupport `json:"groundingSupports"`
    GroundingChunks   []GeminiGroundingChunk   `json:"groundingChunks"`
    WebSearchQueries  []string                 `json:"webSearchQueries,omitempty"`
}

type GeminiGroundingSupport struct {
    Segment struct {
        StartIndex int `json:"startIndex"`
        EndIndex   int `json:"endIndex"`
    } `json:"segment"`
    GroundingChunkIndices []int `json:"groundingChunkIndices"`
}

type GeminiGroundingChunk struct {
    Web struct {
        URI   string `json:"uri"`
        Title string `json:"title"`
    } `json:"web,omitempty"`
}

// OpenAI Responses API format
type OpenAIAnnotation struct {
    Type        string                        `json:"type"` // "url_citation"
    URLCitation *OpenAIAnnotationURLCitation `json:"url_citation,omitempty"`
}

type OpenAIAnnotationURLCitation struct {
    StartIndex int    `json:"start_index"`
    EndIndex   int    `json:"end_index"`
    URL        string `json:"url"`
    Title      string `json:"title"`
}
```

### 3.3 转换逻辑实现

#### Request Mapping (OpenAI → Gemini)

**Step 1: Tool Detection**
```go
func DetermineToolType(tool map[string]interface{}) string {
    if funcName, ok := tool["function"].(map[string]interface{})["name"]; ok {
        switch funcName {
        case "google_search", "web_search":
            return "google_search"
        case "code_execution":
            return "code_execution"
        default:
            return "function"
        }
    }
    return "function"
}
```

**Step 2: Tool Conversion**
```go
func ConvertToolsToGemini(tools []map[string]interface{}) []GeminiTool {
    var geminiTools []GeminiTool
    var functionDeclarations []GeminiFunctionDeclaration
    hasGoogleSearch := false
    
    for _, tool := range tools {
        toolType := DetermineToolType(tool)
        
        switch toolType {
        case "google_search":
            hasGoogleSearch = true
        case "function":
            funcDecl := ConvertFunctionDeclaration(tool)
            functionDeclarations = append(functionDeclarations, funcDecl)
        }
    }
    
    // Group function declarations into one Tool
    if len(functionDeclarations) > 0 {
        geminiTools = append(geminiTools, GeminiTool{
            FunctionDeclarations: functionDeclarations,
        })
    }
    
    // Add Google Search tool separately
    if hasGoogleSearch {
        geminiTools = append(geminiTools, GeminiTool{
            GoogleSearch: &struct{}{},
        })
    }
    
    return geminiTools
}
```

**Step 3: Schema Conversion (JSON Schema → OpenAPI)**
```go
// Simplified version - LiteLLM uses _build_vertex_schema
func ConvertJSONSchemaToOpenAPI(jsonSchema map[string]interface{}) map[string]interface{} {
    schema := make(map[string]interface{})
    for k, v := range jsonSchema {
        // Remove unsupported fields
        if k == "additionalProperties" || k == "strict" {
            continue
        }
        schema[k] = v
    }
    return schema
}
```

#### Response Mapping (Gemini → OpenAI)

**核心转换函数** (参考 LiteLLM `_convert_grounding_metadata_to_annotations`):

```go
func ConvertGroundingMetadataToAnnotations(
    groundingMetadata []GeminiGroundingMetadata,
) []OpenAIAnnotation {
    var annotations []OpenAIAnnotation
    
    for _, metadata := range groundingMetadata {
        // Build chunk index to URI map
        chunkMap := make(map[int]string)
        chunkTitleMap := make(map[int]string)
        for idx, chunk := range metadata.GroundingChunks {
            if chunk.Web.URI != "" {
                chunkMap[idx] = chunk.Web.URI
                chunkTitleMap[idx] = chunk.Web.Title
            }
        }
        
        // Process each grounding support
        for _, support := range metadata.GroundingSupports {
            if len(support.GroundingChunkIndices) == 0 {
                continue
            }
            
            firstChunkIdx := support.GroundingChunkIndices[0]
            if url, ok := chunkMap[firstChunkIdx]; ok {
                annotation := OpenAIAnnotation{
                    Type: "url_citation",
                    URLCitation: &OpenAIAnnotationURLCitation{
                        StartIndex: support.Segment.StartIndex,
                        EndIndex:   support.Segment.EndIndex,
                        URL:        url,
                        Title:      chunkTitleMap[firstChunkIdx],
                    },
                }
                annotations = append(annotations, annotation)
            }
        }
    }
    
    return annotations
}
```

### 3.4 OpenAI Responses API 特定支持

在 `ConvertChatCompletionToResponses` 中添加 annotations 支持：

```go
func ConvertChatCompletionToResponses(
    chatResp OpenAIChatResponse,
    groundingMetadata []GeminiGroundingMetadata,
) OpenAIResponsesResponse {
    annotations := ConvertGroundingMetadataToAnnotations(groundingMetadata)
    
    return OpenAIResponsesResponse{
        Output: []OutputItem{
            {
                Type:   "message",
                Role:   "assistant",
                Content: []OutputContent{
                    {
                        Type:        "output_text",
                        Text:        chatResp.Choices[0].Message.Content,
                        Annotations: annotations, // NEW
                    },
                },
            },
        },
        // ...
    }
}
```

## 4. 实施计划 (v0.1.6 - v0.1.8)

### Phase 1: 结构体定义 (1-2天)
1. 在 `mappers/openai.go` 中添加所有新的 Go 结构体。
2. 扩展 `OpenAIChatRequest` 以包含 `Tools` 和 `WebSearchOptions` 字段。

### Phase 2: Tools 转换逻辑 (2-3天)
3. 实现 `ConvertToolsToGemini` 函数。
4. 实现 JSON Schema → OpenAPI Schema 转换器。
5. 在 `OpenAIToGemini` 中集成 tools 转换。

### Phase 3: Google Search Grounding (核心，2-3天)
6. 实现 `web_search_options` 参数处理。
7. 实现特殊 tool name 检测 (`google_search`)。
8. 实现 Grounding Metadata 提取逻辑。
9. 实现 `ConvertGroundingMetadataToAnnotations`。

### Phase 4: Thinking Mode (1-2天)
10. 实现 `reasoning_effort` 和 `thinking` 参数映射。
11. 添加模型检测逻辑 (Gemini 2.x vs 3+)。
12. 在响应中提取并转换 thinking blocks。

### Phase 5: 测试验证 (2-3天)
13. 编写单元测试 (tools 转换、grounding 提取、annotations 构建)。
14. 编写集成测试 (end-to-end Google Search grounding)。
15. 手动测试各种 tool 组合和边界情况。

## 5. 关键参考

**LiteLLM 文件**:
- `litellm/llms/vertex_ai/gemini/vertex_and_google_ai_studio_gemini.py`
  - `_map_function`: Tools 映射 (Line 417-604)
  - `_map_web_search_options`: Web search 参数 (Line 305-311)
  - `_convert_grounding_metadata_to_annotations`: Grounding 转换 (Line 1727-1779)
- `litellm/llms/vertex_ai/gemini/transformation.py`
  - `_transform_system_message`: System instruction 提取 (Line 678-721)

## 6. 版本规划

**v0.1.6 (联网搜索基础)**:
- Google Search Grounding (联网搜索)
- Grounding Metadata → Annotations (URL 引用)

**v0.1.7 (工具调用)**:
- 标准 Function Calling (基础工具支持)
- Tool Choice 参数支持

**v0.1.8 (推理模式)**:
- Thinking Mode (CoT 推理)
- Reasoning Effort 参数支持

**未来版本**:
- Code Execution Tool (v0.1.9)
- Google Search Retrieval / Dynamic Retrieval (v0.2.0)

