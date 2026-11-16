package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type anthInRequest struct {
	Model         string            `json:"model"`
	System        json.RawMessage   `json:"system,omitempty"`
	Messages      []anthInMessage   `json:"messages"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	TopK          *int              `json:"top_k,omitempty"`
	StopSequences []string          `json:"stop_sequences,omitempty"`
	Tools         []anthInTool      `json:"tools,omitempty"`
	ToolChoice    *anthInToolChoice `json:"tool_choice,omitempty"`
	Thinking      *anthInThinking   `json:"thinking,omitempty"`
	OutputConfig  *anthOutputConfig `json:"output_config,omitempty"`
	Metadata      *struct {
		UserID string `json:"user_id,omitempty"`
	} `json:"metadata,omitempty"`
}

type anthInThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type anthOutputConfig struct {
	Effort string `json:"effort,omitempty"`
	Format *struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema,omitempty"`
	} `json:"format,omitempty"`
}

type anthInTool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthInToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

type anthInMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthInBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Source    *anthMediaSrc   `json:"source,omitempty"`
}

type anthMediaSrc struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// ParseRequest parses an Anthropic Messages API JSON payload into the gateway IR.
func (c *Codec) ParseRequest(modelHint string, body []byte) (*ir.Request, *ir.GatewayError) {
	var in anthInRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, ir.NewError(400, "invalid_json", "invalid Anthropic messages JSON: "+err.Error())
	}

	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = strings.TrimSpace(modelHint)
	}
	if model == "" {
		return nil, ir.NewError(400, "missing_model", "model is required")
	}

	req := &ir.Request{
		Source:      ir.ProtoAnthropic,
		Model:       model,
		Stream:      in.Stream,
		MaxTokens:   in.MaxTokens,
		Temperature: in.Temperature,
		TopP:        in.TopP,
		TopK:        in.TopK,
		Stop:        in.StopSequences,
	}
	if in.Metadata != nil {
		req.User = in.Metadata.UserID
	}

	if len(in.System) > 0 && string(in.System) != "null" {
		req.System = parseSystemBlocks(in.System)
	}

	if in.Thinking != nil {
		switch strings.ToLower(in.Thinking.Type) {
		case "enabled":
			budget := in.Thinking.BudgetTokens
			req.Reasoning = ir.Reasoning{
				Budget: budget,
				Effort: ir.BudgetToEffort(budget),
				Raw:    true,
			}
		case "adaptive":
			req.Reasoning = ir.Reasoning{Effort: "medium", Raw: true}
		case "disabled":
			req.Reasoning = ir.Reasoning{Effort: "none", Raw: true}
		}
	}
	if in.OutputConfig != nil {
		if eff := ir.NormalizeEffort(in.OutputConfig.Effort); eff != "" {
			req.Reasoning.Effort = eff
			req.Reasoning.Raw = true
		}
		if in.OutputConfig.Format != nil && in.OutputConfig.Format.Type != "" {
			req.Format = &ir.Format{
				Type:   in.OutputConfig.Format.Type,
				Schema: in.OutputConfig.Format.Schema,
			}
		}
	}

	for _, t := range in.Tools {
		if t.Name == "" {
			continue
		}
		req.Tools = append(req.Tools, ir.Tool{
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.InputSchema,
		})
	}

	if in.ToolChoice != nil {
		switch strings.ToLower(in.ToolChoice.Type) {
		case "auto":
			req.ToolChoice = ir.ToolChoice{Mode: "auto"}
		case "any":
			req.ToolChoice = ir.ToolChoice{Mode: "required"}
		case "none":
			req.ToolChoice = ir.ToolChoice{Mode: "none"}
		case "tool":
			req.ToolChoice = ir.ToolChoice{Mode: "tool", Name: in.ToolChoice.Name}
		}
		if in.ToolChoice.DisableParallelToolUse != nil {
			val := !*in.ToolChoice.DisableParallelToolUse
			req.ParallelToolCalls = &val
		}
	}

	for _, msg := range in.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		blocks := parseAnthropicBlocks(msg.Content)
		if role == "assistant" {
			var parts []ir.Part
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						parts = append(parts, ir.Part{Kind: ir.KindText, Text: b.Text})
					}
				case "thinking":
					if b.Signature != "" && b.Thinking != "" {
						ir.DefaultSigStore.SaveThought(b.Thinking, b.Signature)
					}
					parts = append(parts, ir.Part{
						Kind:      ir.KindThought,
						Text:      b.Thinking,
						Signature: b.Signature,
					})
				case "tool_use", "server_tool_use":
					callID := ir.UnsanitizeToolID(b.ID)
					if callID == "" {
						callID = ir.NewID("call_")
					}
					args := "{}"
					if len(b.Input) > 0 && string(b.Input) != "null" {
						args = string(b.Input)
					}
					parts = append(parts, ir.Part{
						Kind:   ir.KindToolCall,
						CallID: callID,
						Name:   b.Name,
						Args:   args,
					})
				}
			}
			req.Messages = append(req.Messages, ir.Message{
				Role:  ir.RoleAssistant,
				Parts: parts,
			})
			continue
		}

		// User message may interleave tool_result blocks with text/image/document blocks.
		var toolResults []ir.Part
		var userParts []ir.Part
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				subBlocks := parseAnthropicBlocks(b.Content)
				var subParts []ir.Part
				for _, sb := range subBlocks {
					if p, ok := blockToPart(sb); ok {
						subParts = append(subParts, p)
					}
				}
				resText := ir.TextOf(subParts)
				toolResults = append(toolResults, ir.Part{
					Kind:        ir.KindToolResult,
					CallID:      ir.UnsanitizeToolID(b.ToolUseID),
					Result:      resText,
					IsError:     b.IsError,
					ResultParts: nonTextParts(subParts),
				})
			default:
				if p, ok := blockToPart(b); ok {
					userParts = append(userParts, p)
				}
			}
		}
		if len(toolResults) > 0 {
			req.Messages = append(req.Messages, ir.Message{
				Role:  ir.RoleTool,
				Parts: toolResults,
			})
		}
		if len(userParts) > 0 || len(toolResults) == 0 {
			req.Messages = append(req.Messages, ir.Message{
				Role:  ir.RoleUser,
				Parts: userParts,
			})
		}
	}

	return req, nil
}

func parseSystemBlocks(raw json.RawMessage) []ir.Part {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		if str == "" {
			return nil
		}
		return []ir.Part{{Kind: ir.KindText, Text: str}}
	}
	var blocks []anthInBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []ir.Part
	for _, b := range blocks {
		if b.Text != "" {
			out = append(out, ir.Part{Kind: ir.KindText, Text: b.Text})
		}
	}
	return out
}

func parseAnthropicBlocks(raw json.RawMessage) []anthInBlock {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		if str == "" {
			return nil
		}
		return []anthInBlock{{Type: "text", Text: str}}
	}
	var blocks []anthInBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

func blockToPart(b anthInBlock) (ir.Part, bool) {
	switch b.Type {
	case "text", "":
		if b.Text == "" {
			return ir.Part{}, false
		}
		return ir.Part{Kind: ir.KindText, Text: b.Text}, true
	case "image":
		if b.Source == nil {
			return ir.Part{}, false
		}
		if b.Source.Type == "base64" && b.Source.Data != "" {
			data, err := base64.StdEncoding.DecodeString(b.Source.Data)
			if err != nil {
				return ir.Part{}, false
			}
			return ir.Part{
				Kind: ir.KindImage,
				Mime: b.Source.MediaType,
				Data: data,
				URL:  codec.EncodeDataURL(b.Source.MediaType, data),
			}, true
		}
		if b.Source.URL != "" {
			return ir.Part{Kind: ir.KindImage, URL: b.Source.URL}, true
		}
	case "document":
		if b.Source != nil && b.Source.Type == "base64" && b.Source.Data != "" {
			data, err := base64.StdEncoding.DecodeString(b.Source.Data)
			if err != nil {
				return ir.Part{}, false
			}
			return ir.Part{
				Kind: ir.KindFile,
				Mime: b.Source.MediaType,
				Data: data,
				URL:  codec.EncodeDataURL(b.Source.MediaType, data),
			}, true
		}
	}
	return ir.Part{}, false
}

func nonTextParts(parts []ir.Part) []ir.Part {
	var out []ir.Part
	for _, p := range parts {
		if p.Kind != ir.KindText && p.Kind != "" {
			out = append(out, p)
		}
	}
	return out
}
