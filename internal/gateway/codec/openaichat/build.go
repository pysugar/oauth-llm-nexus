package openaichat

import (
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type outChatRequest struct {
	Model               string             `json:"model"`
	Messages            []outChatMessage   `json:"messages"`
	Tools               []outChatTool      `json:"tools,omitempty"`
	ToolChoice          any                `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool              `json:"parallel_tool_calls,omitempty"`
	Stream              bool               `json:"stream"`
	StreamOptions       *outStreamOptions  `json:"stream_options,omitempty"`
	MaxTokens           int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                `json:"max_completion_tokens,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	Seed                *int64             `json:"seed,omitempty"`
	Stop                []string           `json:"stop,omitempty"`
	PresencePenalty     *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64           `json:"frequency_penalty,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	ResponseFormat      *outResponseFormat `json:"response_format,omitempty"`
	User                string             `json:"user,omitempty"`
}

type outStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type outChatMessage struct {
	Role             string           `json:"role"`
	Name             string           `json:"name,omitempty"`
	Content          any              `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []outChatToolRef `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}

type outChatToolRef struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function outChatFuncCall `json:"function"`
}

type outChatFuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type outChatTool struct {
	Type     string         `json:"type"`
	Function outChatFuncDef `json:"function"`
}

type outChatFuncDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type outResponseFormat struct {
	Type       string         `json:"type"`
	JSONSchema *outJSONSchema `json:"json_schema,omitempty"`
}

type outJSONSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// BuildRequest encodes an IR Request into an OpenAI Chat Completions JSON payload.
func (c *Codec) BuildRequest(req *ir.Request, upstreamModel string, stream bool, q codec.Quirks) ([]byte, *ir.GatewayError) {
	if req == nil {
		return nil, ir.NewError(400, "empty_request", "request is required")
	}

	model := upstreamModel
	if model == "" {
		model = req.Model
	}

	out := outChatRequest{
		Model:            model,
		Stream:           stream,
		Seed:             req.Seed,
		Stop:             req.Stop,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		User:             req.User,
	}
	if !(q.MaxCompletionTokens && codec.IsStrictReasoningModel(model)) {
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}

	if req.MaxTokens > 0 {
		if q.MaxCompletionTokens {
			out.MaxCompletionTokens = req.MaxTokens
		} else {
			out.MaxTokens = req.MaxTokens
		}
	}

	if !q.OmitParallelToolCalls {
		out.ParallelToolCalls = req.ParallelToolCalls
	}

	if stream && !q.OmitStreamOptions {
		includeUsage := q.IncludeUsageInStream
		if req.IncludeUsage != nil {
			includeUsage = *req.IncludeUsage
		}
		if includeUsage {
			out.StreamOptions = &outStreamOptions{IncludeUsage: true}
		}
	}

	if !q.OmitReasoningEffort {
		if eff := ir.EffectiveEffort(req.Reasoning); eff != "" {
			out.ReasoningEffort = eff
		}
	}

	if req.Format != nil && req.Format.Type != "" {
		rf := &outResponseFormat{Type: req.Format.Type}
		if req.Format.Type == "json_schema" {
			rf.JSONSchema = &outJSONSchema{
				Name:        req.Format.Name,
				Description: req.Format.Description,
				Schema:      req.Format.Schema,
				Strict:      req.Format.Strict,
			}
			if rf.JSONSchema.Name == "" {
				rf.JSONSchema.Name = "response_schema"
			}
		}
		out.ResponseFormat = rf
	}

	for _, t := range req.Tools {
		params := t.Schema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		var strictPtr *bool
		if t.Strict && !q.OmitStrictTools {
			v := true
			strictPtr = &v
		}
		out.Tools = append(out.Tools, outChatTool{
			Type: "function",
			Function: outChatFuncDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
				Strict:      strictPtr,
			},
		})
	}

	if len(out.Tools) > 0 {
		switch req.ToolChoice.Mode {
		case "auto", "none", "required":
			out.ToolChoice = req.ToolChoice.Mode
		case "tool":
			if req.ToolChoice.Name != "" {
				out.ToolChoice = map[string]any{
					"type": "function",
					"function": map[string]string{
						"name": req.ToolChoice.Name,
					},
				}
			}
		}
	}

	if sysText := req.SystemText(); sysText != "" {
		out.Messages = append(out.Messages, outChatMessage{
			Role:    "system",
			Content: sysText,
		})
	}

	msgs := ir.NormalizeToolTurns(req.Messages)
	for _, msg := range msgs {
		switch msg.Role {
		case ir.RoleSystem:
			if txt := ir.TextOf(msg.Parts); txt != "" {
				out.Messages = append(out.Messages, outChatMessage{
					Role:    "system",
					Name:    msg.Name,
					Content: txt,
				})
			}

		case ir.RoleUser:
			content, _ := buildUserContent(msg.Parts)
			out.Messages = append(out.Messages, outChatMessage{
				Role:    "user",
				Name:    msg.Name,
				Content: content,
			})

		case ir.RoleAssistant:
			m := buildAssistantMessage(msg, q)
			out.Messages = append(out.Messages, m)

		case ir.RoleTool:
			// OpenAI Chat accepts only text in role:tool messages, so images a tool returned go to
			// the model in a user message right after the tool messages.
			var images []map[string]any
			for _, p := range msg.Parts {
				if p.Kind != ir.KindToolResult {
					continue
				}
				text := p.Result
				var extra []string
				imgCount := 0
				for _, rp := range p.ResultParts {
					if rp.Kind == ir.KindImage {
						u := rp.URL
						if u == "" && len(rp.Data) > 0 {
							u = codec.EncodeDataURL(rp.Mime, rp.Data)
						}
						if u != "" {
							images = append(images, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
							imgCount++
						}
					} else if rp.Text != "" {
						extra = append(extra, rp.Text)
					}
				}
				for _, e := range extra {
					if text != "" {
						text += "\n"
					}
					text += e
				}
				if imgCount > 0 {
					if text != "" {
						text += "\n"
					}
					text += "(image output attached in the next user message)"
				}
				if p.IsError && text != "" && !strings.HasPrefix(text, "[") {
					text = "[error] " + text
				}
				out.Messages = append(out.Messages, outChatMessage{
					Role:       "tool",
					Name:       p.Name,
					ToolCallID: p.CallID,
					Content:    text,
				})
			}
			if len(images) > 0 {
				content := append([]map[string]any{{"type": "text", "text": "Images returned by the tool call(s) above:"}}, images...)
				out.Messages = append(out.Messages, outChatMessage{Role: "user", Content: content})
			}
		}
	}

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, ir.NewError(500, "marshal_error", err.Error())
	}
	return raw, nil
}

func buildUserContent(parts []ir.Part) (any, bool) {
	hasMedia := false
	for _, p := range parts {
		if p.Kind == ir.KindImage || p.Kind == ir.KindFile {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		return ir.TextOf(parts), false
	}

	var arr []map[string]any
	for _, p := range parts {
		switch p.Kind {
		case ir.KindText, "":
			if p.Text != "" {
				arr = append(arr, map[string]any{"type": "text", "text": p.Text})
			}
		case ir.KindImage:
			urlStr := p.URL
			if urlStr == "" && len(p.Data) > 0 {
				urlStr = codec.EncodeDataURL(p.Mime, p.Data)
			}
			if urlStr == "" {
				continue
			}
			imgObj := map[string]any{"url": urlStr}
			if p.Detail != "" {
				imgObj["detail"] = p.Detail
			}
			arr = append(arr, map[string]any{
				"type":      "image_url",
				"image_url": imgObj,
			})
		case ir.KindFile:
			fileData := p.URL
			if fileData == "" && len(p.Data) > 0 {
				fileData = codec.EncodeDataURL(p.Mime, p.Data)
			}
			if fileData != "" {
				arr = append(arr, map[string]any{
					"type": "file",
					"file": map[string]any{
						"file_data": fileData,
						"filename":  p.Filename,
						"mime_type": p.Mime,
					},
				})
			}
		}
	}
	if len(arr) == 0 {
		return "", false
	}
	return arr, true
}

func buildAssistantMessage(msg ir.Message, q codec.Quirks) outChatMessage {
	var textParts []string
	var thoughtParts []string
	var toolCalls []outChatToolRef

	for _, p := range msg.Parts {
		switch p.Kind {
		case ir.KindText, ir.KindRefusal, "":
			if p.Text != "" {
				textParts = append(textParts, p.Text)
			}
		case ir.KindThought:
			if p.Text != "" {
				thoughtParts = append(thoughtParts, p.Text)
			}
		case ir.KindToolCall:
			callID := p.CallID
			if callID == "" {
				callID = ir.NewID("call_")
			}
			toolCalls = append(toolCalls, outChatToolRef{
				ID:   callID,
				Type: "function",
				Function: outChatFuncCall{
					Name:      p.Name,
					Arguments: ir.ArgsOrEmpty(p.Args),
				},
			})
		}
	}

	m := outChatMessage{
		Role:      "assistant",
		Name:      msg.Name,
		ToolCalls: toolCalls,
	}
	if len(textParts) > 0 {
		m.Content = joinStrings(textParts)
	} else if len(toolCalls) > 0 {
		// OpenAI Chat allows null content when tool_calls are present.
		m.Content = nil
	} else {
		m.Content = ""
	}

	if q.EchoReasoningContent {
		if len(thoughtParts) > 0 {
			m.ReasoningContent = joinStrings(thoughtParts)
		} else if len(toolCalls) > 0 {
			// DeepSeek reasoner requires reasoning_content field when tool_calls are present.
			m.ReasoningContent = " "
		}
	}
	return m
}

func joinStrings(ss []string) string {
	if len(ss) == 1 {
		return ss[0]
	}
	total := 0
	for _, s := range ss {
		total += len(s)
	}
	buf := make([]byte, 0, total)
	for _, s := range ss {
		buf = append(buf, s...)
	}
	return string(buf)
}
