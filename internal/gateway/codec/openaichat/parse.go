package openaichat

import (
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type chatRequest struct {
	Model               string            `json:"model"`
	Messages            []chatInMessage   `json:"messages"`
	Tools               []chatInTool      `json:"tools,omitempty"`
	ToolChoice          json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	StreamOptions       *chatStreamOpts   `json:"stream_options,omitempty"`
	MaxTokens           int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	TopP                *float64          `json:"top_p,omitempty"`
	Seed                *int64            `json:"seed,omitempty"`
	Stop                json.RawMessage   `json:"stop,omitempty"`
	PresencePenalty     *float64          `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64          `json:"frequency_penalty,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
	Reasoning           *chatReasoningObj `json:"reasoning,omitempty"`
	Thinking            *chatThinkingObj  `json:"thinking,omitempty"`
	ResponseFormat      *chatRespFormat   `json:"response_format,omitempty"`
	User                string            `json:"user,omitempty"`
}

type chatStreamOpts struct {
	IncludeUsage *bool `json:"include_usage,omitempty"`
}

type chatReasoningObj struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type chatThinkingObj struct {
	Type         string `json:"type,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type chatRespFormat struct {
	Type       string          `json:"type"`
	JSONSchema *chatJSONSchema `json:"json_schema,omitempty"`
}

type chatJSONSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type chatInTool struct {
	Type     string         `json:"type"`
	Function *chatInFuncDef `json:"function,omitempty"`
}

type chatInFuncDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type chatInMessage struct {
	Role             string          `json:"role"`
	Name             string          `json:"name,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
	Refusal          string          `json:"refusal,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	ToolCalls        []chatInToolRef `json:"tool_calls,omitempty"`
	FunctionCall     *chatInFuncCall `json:"function_call,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
}

type chatInToolRef struct {
	Index    *int            `json:"index,omitempty"`
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`
	Function *chatInFuncCall `json:"function,omitempty"`
}

type chatInFuncCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ParseRequest parses an OpenAI Chat Completions JSON request into the gateway IR.
func (c *Codec) ParseRequest(modelHint string, body []byte) (*ir.Request, *ir.GatewayError) {
	var in chatRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, ir.NewError(400, "invalid_json", "invalid OpenAI chat completions JSON: "+err.Error())
	}

	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = strings.TrimSpace(modelHint)
	}
	if model == "" {
		return nil, ir.NewError(400, "missing_model", "model is required")
	}

	maxTok := in.MaxCompletionTokens
	if maxTok <= 0 {
		maxTok = in.MaxTokens
	}

	req := &ir.Request{
		Source:            ir.ProtoOpenAIChat,
		Model:             model,
		Stream:            in.Stream,
		MaxTokens:         maxTok,
		Temperature:       in.Temperature,
		TopP:              in.TopP,
		Seed:              in.Seed,
		PresencePenalty:   in.PresencePenalty,
		FrequencyPenalty:  in.FrequencyPenalty,
		ParallelToolCalls: in.ParallelToolCalls,
		User:              in.User,
	}

	if in.StreamOptions != nil && in.StreamOptions.IncludeUsage != nil {
		req.IncludeUsage = in.StreamOptions.IncludeUsage
	}

	if len(in.Stop) > 0 && string(in.Stop) != "null" {
		req.Stop = parseStop(in.Stop)
	}

	// Parse reasoning / thinking controls.
	if eff := ir.NormalizeEffort(in.ReasoningEffort); eff != "" {
		req.Reasoning = ir.Reasoning{Effort: eff, Raw: true}
	} else if in.Reasoning != nil {
		req.Reasoning = ir.Reasoning{
			Effort: ir.NormalizeEffort(in.Reasoning.Effort),
			Budget: in.Reasoning.MaxTokens,
			Raw:    true,
		}
	} else if in.Thinking != nil {
		switch strings.ToLower(in.Thinking.Type) {
		case "enabled":
			req.Reasoning = ir.Reasoning{
				Budget: in.Thinking.BudgetTokens,
				Effort: ir.BudgetToEffort(in.Thinking.BudgetTokens),
				Raw:    true,
			}
			if req.Reasoning.Budget == 0 {
				req.Reasoning.Budget = 8192
				req.Reasoning.Effort = "medium"
			}
		case "disabled":
			req.Reasoning = ir.Reasoning{Effort: "none", Raw: true}
		}
	}

	if in.ResponseFormat != nil && in.ResponseFormat.Type != "" {
		f := &ir.Format{Type: in.ResponseFormat.Type}
		if in.ResponseFormat.JSONSchema != nil {
			f.Name = in.ResponseFormat.JSONSchema.Name
			f.Description = in.ResponseFormat.JSONSchema.Description
			f.Schema = in.ResponseFormat.JSONSchema.Schema
			f.Strict = in.ResponseFormat.JSONSchema.Strict
		}
		req.Format = f
	}

	for _, t := range in.Tools {
		if t.Function == nil || t.Function.Name == "" {
			continue
		}
		strict := t.Function.Strict != nil && *t.Function.Strict
		req.Tools = append(req.Tools, ir.Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Schema:      t.Function.Parameters,
			Strict:      strict,
		})
	}

	if len(in.ToolChoice) > 0 && string(in.ToolChoice) != "null" {
		req.ToolChoice = parseToolChoice(in.ToolChoice)
	}

	for _, msg := range in.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "system", "developer":
			parts := parseContentParts(msg.Content)
			for _, p := range parts {
				if p.Text != "" {
					req.System = append(req.System, ir.Part{Kind: ir.KindText, Text: p.Text})
				}
			}
		case "user":
			parts := parseContentParts(msg.Content)
			req.Messages = append(req.Messages, ir.Message{
				Role:  ir.RoleUser,
				Name:  msg.Name,
				Parts: parts,
			})
		case "assistant":
			var parts []ir.Part
			thought := msg.ReasoningContent
			if thought == "" {
				thought = msg.Reasoning
			}
			if thought != "" {
				parts = append(parts, ir.Part{Kind: ir.KindThought, Text: thought})
			}
			parts = append(parts, parseContentParts(msg.Content)...)
			if msg.Refusal != "" {
				parts = append(parts, ir.Part{Kind: ir.KindRefusal, Text: msg.Refusal})
			}
			for _, tc := range msg.ToolCalls {
				if tc.Function == nil {
					continue
				}
				callID := tc.ID
				if callID == "" {
					callID = ir.NewID("call_")
				}
				parts = append(parts, ir.Part{
					Kind:   ir.KindToolCall,
					CallID: callID,
					Name:   tc.Function.Name,
					Args:   tc.Function.Arguments,
				})
			}
			if msg.FunctionCall != nil && msg.FunctionCall.Name != "" {
				parts = append(parts, ir.Part{
					Kind:   ir.KindToolCall,
					CallID: ir.NewID("call_"),
					Name:   msg.FunctionCall.Name,
					Args:   msg.FunctionCall.Arguments,
				})
			}
			req.Messages = append(req.Messages, ir.Message{
				Role:  ir.RoleAssistant,
				Name:  msg.Name,
				Parts: parts,
			})
		case "tool", "function":
			parts := parseContentParts(msg.Content)
			text := ir.TextOf(parts)
			callID := msg.ToolCallID
			if callID == "" {
				callID = msg.Name
			}
			req.Messages = append(req.Messages, ir.Message{
				Role: ir.RoleTool,
				Name: msg.Name,
				Parts: []ir.Part{{
					Kind:        ir.KindToolResult,
					CallID:      callID,
					Name:        msg.Name,
					Result:      text,
					ResultParts: nonTextParts(parts),
				}},
			})
		}
	}

	return req, nil
}

func parseStop(raw json.RawMessage) []string {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single != "" {
			return []string{single}
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	return nil
}

func parseToolChoice(raw json.RawMessage) ir.ToolChoice {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return ir.ToolChoice{Mode: strings.ToLower(str)}
	}
	var obj struct {
		Type     string `json:"type"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Function != nil && obj.Function.Name != "" {
		return ir.ToolChoice{Mode: "tool", Name: obj.Function.Name}
	}
	return ir.ToolChoice{}
}

func parseContentParts(raw json.RawMessage) []ir.Part {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if str == "" {
			return nil
		}
		return []ir.Part{{Kind: ir.KindText, Text: str}}
	}

	var items []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
		ImageURL *struct {
			URL    string `json:"url"`
			Detail string `json:"detail"`
		} `json:"image_url"`
		File *struct {
			FileData string `json:"file_data"`
			Filename string `json:"filename"`
			MimeType string `json:"mime_type"`
		} `json:"file"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}

	var parts []ir.Part
	for _, it := range items {
		switch it.Type {
		case "text", "input_text", "output_text", "":
			if it.Text != "" {
				parts = append(parts, ir.Part{Kind: ir.KindText, Text: it.Text})
			}
		case "thinking":
			if it.Thinking != "" || it.Text != "" {
				txt := it.Thinking
				if txt == "" {
					txt = it.Text
				}
				parts = append(parts, ir.Part{Kind: ir.KindThought, Text: txt})
			}
		case "image_url":
			if it.ImageURL != nil && it.ImageURL.URL != "" {
				p := ir.Part{
					Kind:   ir.KindImage,
					URL:    it.ImageURL.URL,
					Detail: it.ImageURL.Detail,
				}
				if mime, data, ok := codec.DecodeDataURL(it.ImageURL.URL); ok {
					p.Mime = mime
					p.Data = data
				}
				parts = append(parts, p)
			}
		case "file":
			if it.File != nil && it.File.FileData != "" {
				p := ir.Part{
					Kind:     ir.KindFile,
					Filename: it.File.Filename,
					Mime:     it.File.MimeType,
					URL:      it.File.FileData,
				}
				if mime, data, ok := codec.DecodeDataURL(it.File.FileData); ok {
					p.Mime = mime
					p.Data = data
				}
				parts = append(parts, p)
			}
		}
	}
	return parts
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
