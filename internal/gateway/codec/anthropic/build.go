package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type anthOutRequest struct {
	Model         string           `json:"model"`
	System        string           `json:"system,omitempty"`
	OutputConfig  map[string]any   `json:"output_config,omitempty"`
	Messages      []anthOutMessage `json:"messages"`
	MaxTokens     int              `json:"max_tokens"`
	Stream        bool             `json:"stream"`
	Temperature   *float64         `json:"temperature,omitempty"`
	TopP          *float64         `json:"top_p,omitempty"`
	TopK          *int             `json:"top_k,omitempty"`
	StopSequences []string         `json:"stop_sequences,omitempty"`
	Tools         []anthOutTool    `json:"tools,omitempty"`
	ToolChoice    map[string]any   `json:"tool_choice,omitempty"`
	Thinking      map[string]any   `json:"thinking,omitempty"`
	Metadata      map[string]any   `json:"metadata,omitempty"`
}

type anthOutMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type anthOutTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// BuildRequest encodes an IR Request into an Anthropic Messages API JSON payload.
func (c *Codec) BuildRequest(req *ir.Request, upstreamModel string, stream bool, _ codec.Quirks) ([]byte, *ir.GatewayError) {
	if req == nil {
		return nil, ir.NewError(400, "empty_request", "request is required")
	}

	model := upstreamModel
	if model == "" {
		model = req.Model
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	out := anthOutRequest{
		Model:         model,
		System:        req.SystemText(),
		MaxTokens:     maxTokens,
		Stream:        stream,
		StopSequences: req.Stop,
	}
	if req.User != "" {
		out.Metadata = map[string]any{"user_id": req.User}
	}

	thinkingEnabled := false
	adaptive := isClaudeModel(model) && AdaptiveOnly(model)
	if eff := ir.EffectiveEffort(req.Reasoning); eff != "" && adaptive {
		// Claude 4.6+ thinks only adaptively: no budget, effort in output_config, and "disabled"
		// is refused by the newest models, so "none" becomes the lowest effort.
		thinkingEnabled = true
		out.Thinking = map[string]any{"type": "adaptive", "display": "summarized"}
		out.OutputConfig = map[string]any{"effort": adaptiveEffort(model, eff)}
	} else if eff != "" {
		if eff == "none" {
			out.Thinking = map[string]any{"type": "disabled"}
		} else {
			budget := ir.EffectiveBudget(req.Reasoning)
			if budget <= 0 {
				budget = ir.EffortToBudget(eff)
			}
			if budget < 1024 {
				budget = 1024
			}
			if req.MaxTokens == 0 && out.MaxTokens <= budget {
				out.MaxTokens = budget + 4096
			} else if budget >= out.MaxTokens {
				if out.MaxTokens > 1024 {
					budget = out.MaxTokens - 512
				} else {
					budget = 0
				}
			}
			if budget >= 1024 {
				thinkingEnabled = true
				out.Thinking = map[string]any{
					"type":          "enabled",
					"budget_tokens": budget,
				}
			}
		}
	}

	// Anthropic disallows temperature/top_k overrides when extended thinking is enabled,
	// and newer Claude models reject passing both temperature and top_p together.
	if !thinkingEnabled {
		if req.Temperature != nil {
			out.Temperature = req.Temperature
		} else if req.TopP != nil {
			out.TopP = req.TopP
		}
		out.TopK = req.TopK
	}

	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, anthOutTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	if len(out.Tools) > 0 {
		disableParallel := req.ParallelToolCalls != nil && !*req.ParallelToolCalls
		switch req.ToolChoice.Mode {
		case "auto", "":
			if req.ToolChoice.Mode == "auto" || disableParallel {
				tc := map[string]any{"type": "auto"}
				if disableParallel {
					tc["disable_parallel_tool_use"] = true
				}
				out.ToolChoice = tc
			}
		case "required":
			tc := map[string]any{"type": "any"}
			if disableParallel {
				tc["disable_parallel_tool_use"] = true
			}
			out.ToolChoice = tc
		case "none":
			out.ToolChoice = map[string]any{"type": "none"}
		case "tool":
			if req.ToolChoice.Name != "" {
				tc := map[string]any{"type": "tool", "name": req.ToolChoice.Name}
				if disableParallel {
					tc["disable_parallel_tool_use"] = true
				}
				out.ToolChoice = tc
			}
		}
	}

	// Convert IR messages into Anthropic alternating user/assistant messages and repair tool pairing.
	rawMsgs := buildAnthropicMessages(ir.NormalizeToolTurns(req.Messages), &out)
	out.Messages = pairToolBlocks(rawMsgs)
	if thinkingEnabled && lastToolTurnLacksThinking(out.Messages) {
		// Claude requires the assistant turn of an ongoing tool loop to open with its signed
		// thinking; when the client lost it (and it is not cached), ask without thinking instead
		// of getting a 400.
		out.Thinking = nil
		out.OutputConfig = nil
	}
	if len(out.Messages) == 0 {
		out.Messages = []anthOutMessage{{
			Role:    "user",
			Content: []any{map[string]any{"type": "text", "text": " "}},
		}}
	}

	data, err := json.Marshal(out)
	if err != nil {
		return nil, ir.NewError(500, "marshal_error", err.Error())
	}
	return data, nil
}

func buildAnthropicMessages(msgs []ir.Message, out *anthOutRequest) []anthOutMessage {
	var result []anthOutMessage
	claude := isClaudeModel(out.Model)

	appendMsg := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if len(result) > 0 && result[len(result)-1].Role == role {
			result[len(result)-1].Content = append(result[len(result)-1].Content, blocks...)
			return
		}
		result = append(result, anthOutMessage{Role: role, Content: blocks})
	}

	for _, msg := range msgs {
		switch msg.Role {
		case ir.RoleSystem:
			if txt := ir.TextOf(msg.Parts); txt != "" {
				if out.System != "" {
					out.System += "\n\n" + txt
				} else {
					out.System = txt
				}
			}

		case ir.RoleUser:
			var blocks []any
			for _, p := range msg.Parts {
				if b, ok := partToAnthropicBlock(p); ok {
					blocks = append(blocks, b)
				}
			}
			if len(blocks) > 0 {
				appendMsg("user", blocks)
			}

		case ir.RoleTool:
			var blocks []any
			for _, p := range msg.Parts {
				if p.Kind != ir.KindToolResult {
					continue
				}
				content := buildToolResultContent(p)
				block := map[string]any{
					"type":        "tool_result",
					"tool_use_id": ir.SanitizeToolID(p.CallID),
					"content":     content,
				}
				if p.IsError {
					block["is_error"] = true
				}
				blocks = append(blocks, block)
			}
			if len(blocks) > 0 {
				appendMsg("user", blocks)
			}

		case ir.RoleAssistant:
			var blocks []any
			for _, p := range msg.Parts {
				switch p.Kind {
				case ir.KindThought:
					// Anthropic rejects unsigned thinking blocks, so only attach thinking when signed.
					sig := p.Signature
					if sig == "" {
						sig = ir.DefaultSigStore.LookupThought(p.Text)
					}
					if claude && !ValidClaudeSignature(sig) {
						sig = "" // another vendor's signature (Gemini, DeepSeek UUIDs) is a 400 on Claude
					}
					if sig != "" && p.Text != "" {
						blocks = append(blocks, map[string]any{
							"type":      "thinking",
							"thinking":  p.Text,
							"signature": sig,
						})
					}
				case ir.KindText, ir.KindRefusal, "":
					if strings.TrimSpace(p.Text) != "" {
						blocks = append(blocks, map[string]any{
							"type": "text",
							"text": p.Text,
						})
					}
				case ir.KindToolCall:
					inputMap := ir.ParseArgs(p.Args)
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    ir.SanitizeToolID(p.CallID),
						"name":  p.Name,
						"input": inputMap,
					})
				}
			}
			if claude {
				blocks = restoreCallThinking(blocks, msg)
			}
			if len(blocks) > 0 {
				appendMsg("assistant", blocks)
			}
		}
	}

	return result
}

// restoreCallThinking puts a Claude reply's saved thinking back in front of its tool turn when the
// client dropped it (OpenAI Chat and Gemini clients cannot carry Claude signatures).
func restoreCallThinking(blocks []any, msg ir.Message) []any {
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok && (m["type"] == "thinking" || m["type"] == "redacted_thinking") {
			return blocks
		}
	}
	for _, p := range msg.Parts {
		if p.Kind != ir.KindToolCall {
			continue
		}
		saved := ir.DefaultSigStore.LookupCallThinking(p.CallID)
		if len(saved) == 0 {
			continue
		}
		head := make([]any, 0, len(saved)+len(blocks))
		for _, tb := range saved {
			if tb.Redacted {
				head = append(head, map[string]any{"type": "redacted_thinking", "data": tb.Signature})
			} else {
				head = append(head, map[string]any{"type": "thinking", "thinking": tb.Text, "signature": tb.Signature})
			}
		}
		return append(head, blocks...)
	}
	return blocks
}

// lastToolTurnLacksThinking reports whether the final assistant turn calls tools without opening
// with a thinking block.
func lastToolTurnLacksThinking(msgs []anthOutMessage) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		hasTool := false
		for _, b := range msgs[i].Content {
			if m, ok := b.(map[string]any); ok && m["type"] == "tool_use" {
				hasTool = true
			}
		}
		if !hasTool || len(msgs[i].Content) == 0 {
			return false
		}
		first, _ := msgs[i].Content[0].(map[string]any)
		return first["type"] != "thinking" && first["type"] != "redacted_thinking"
	}
	return false
}

func partToAnthropicBlock(p ir.Part) (any, bool) {
	switch p.Kind {
	case ir.KindText, "":
		if strings.TrimSpace(p.Text) == "" {
			return nil, false
		}
		return map[string]any{"type": "text", "text": p.Text}, true
	case ir.KindImage:
		data, mime := p.Data, p.Mime
		if len(data) == 0 && p.URL != "" {
			if m, d, ok := codec.DecodeDataURL(p.URL); ok {
				mime, data = m, d
			} else {
				return map[string]any{
					"type": "image",
					"source": map[string]any{
						"type": "url",
						"url":  p.URL,
					},
				}, true
			}
		}
		if len(data) > 0 {
			if mime == "" {
				mime = "image/png"
			}
			return map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": mime,
					"data":       base64.StdEncoding.EncodeToString(data),
				},
			}, true
		}
	case ir.KindFile:
		data, mime := p.Data, p.Mime
		if len(data) == 0 && p.URL != "" {
			if m, d, ok := codec.DecodeDataURL(p.URL); ok {
				mime, data = m, d
			}
		}
		if len(data) > 0 && mime == "application/pdf" {
			return map[string]any{
				"type": "document",
				"source": map[string]any{
					"type":       "base64",
					"media_type": mime,
					"data":       base64.StdEncoding.EncodeToString(data),
				},
			}, true
		}
	}
	return nil, false
}

func buildToolResultContent(p ir.Part) any {
	if len(p.ResultParts) == 0 {
		return p.Result
	}
	var blocks []any
	if p.Result != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": p.Result})
	}
	for _, rp := range p.ResultParts {
		if b, ok := partToAnthropicBlock(rp); ok {
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 {
		return p.Result
	}
	return blocks
}

// pairToolBlocks enforces Anthropic's invariant that conversations start with user,
// alternate user/assistant, and every tool_use has an immediately following tool_result.
func pairToolBlocks(msgs []anthOutMessage) []anthOutMessage {
	if len(msgs) == 0 {
		return nil
	}
	if msgs[0].Role != "user" {
		msgs = append([]anthOutMessage{{
			Role:    "user",
			Content: []any{map[string]any{"type": "text", "text": "Continue."}},
		}}, msgs...)
	}

	out := make([]anthOutMessage, 0, len(msgs))
	for i := 0; i < len(msgs); i++ {
		cur := msgs[i]
		if cur.Role != "assistant" {
			// Strip any orphaned tool_result when not immediately following assistant tool_use.
			filtered := cur.Content[:0]
			for _, b := range cur.Content {
				if m, ok := b.(map[string]any); ok && m["type"] == "tool_result" {
					continue
				}
				filtered = append(filtered, b)
			}
			if len(filtered) > 0 {
				out = append(out, anthOutMessage{Role: cur.Role, Content: filtered})
			}
			continue
		}

		var toolIDs []string
		for _, b := range cur.Content {
			if m, ok := b.(map[string]any); ok && m["type"] == "tool_use" {
				if id, ok := m["id"].(string); ok && id != "" {
					toolIDs = append(toolIDs, id)
				}
			}
		}
		out = append(out, cur)
		if len(toolIDs) == 0 {
			continue
		}

		// Inspect the immediately following user message (if any) and ensure exact matching.
		want := make(map[string]bool, len(toolIDs))
		for _, id := range toolIDs {
			want[id] = true
		}

		var nextUserBlocks []any
		hasNextUser := i+1 < len(msgs) && msgs[i+1].Role == "user"
		if hasNextUser {
			for _, b := range msgs[i+1].Content {
				m, ok := b.(map[string]any)
				if ok && m["type"] == "tool_result" {
					tid, _ := m["tool_use_id"].(string)
					if want[tid] {
						nextUserBlocks = append(nextUserBlocks, b)
						delete(want, tid)
					}
					continue
				}
				nextUserBlocks = append(nextUserBlocks, b)
			}
			i++
		}

		// Place missing synthetic tool_results before other user blocks per Anthropic ordering rule.
		var missing []any
		for _, id := range toolIDs {
			if want[id] {
				missing = append(missing, map[string]any{
					"type":        "tool_result",
					"tool_use_id": id,
					"content":     ir.MissingToolResult,
					"is_error":    true,
				})
			}
		}
		combined := append(missing, nextUserBlocks...)
		if len(combined) > 0 {
			out = append(out, anthOutMessage{Role: "user", Content: combined})
		}
	}
	return out
}
