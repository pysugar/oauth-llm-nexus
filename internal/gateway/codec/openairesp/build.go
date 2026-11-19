package openairesp

import (
	"encoding/json"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type outRespRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             []any             `json:"input"`
	Tools             []outRespTool     `json:"tools,omitempty"`
	ToolChoice        any               `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Stream            bool              `json:"stream"`
	MaxOutputTokens   int               `json:"max_output_tokens,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Reasoning         *outRespReasoning `json:"reasoning,omitempty"`
	Text              *outRespText      `json:"text,omitempty"`
	User              string            `json:"user,omitempty"`
	Store             *bool             `json:"store,omitempty"`
}

type outRespTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type outRespReasoning struct {
	Effort string `json:"effort"`
}

type outRespText struct {
	Format map[string]any `json:"format"`
}

// BuildRequest encodes an IR Request into an OpenAI Responses API JSON payload.
func (c *Codec) BuildRequest(req *ir.Request, upstreamModel string, stream bool, q codec.Quirks) ([]byte, *ir.GatewayError) {
	if req == nil {
		return nil, ir.NewError(400, "empty_request", "request is required")
	}
	if req.PreviousResponseID != "" {
		return nil, ir.NewError(400, "unsupported_previous_response_id",
			"previous_response_id requires direct passthrough to an OpenAI Responses backend")
	}

	model := upstreamModel
	if model == "" {
		model = req.Model
	}

	out := outRespRequest{
		Model:           model,
		Instructions:    req.SystemText(),
		Input:           make([]any, 0, len(req.Messages)),
		Stream:          stream,
		MaxOutputTokens: req.MaxTokens,
		User:            req.User,
		Store:           req.Store,
	}
	if !(q.MaxCompletionTokens && codec.IsStrictReasoningModel(model)) {
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}

	if !q.OmitParallelToolCalls {
		out.ParallelToolCalls = req.ParallelToolCalls
	}

	if !q.OmitReasoningEffort {
		if eff := ir.EffectiveEffort(req.Reasoning); eff != "" {
			out.Reasoning = &outRespReasoning{Effort: eff}
		}
	}

	if req.Format != nil && req.Format.Type != "" {
		fmtObj := map[string]any{"type": req.Format.Type}
		if req.Format.Type == "json_schema" {
			name := req.Format.Name
			if name == "" {
				name = "response_schema"
			}
			fmtObj["name"] = name
			if req.Format.Description != "" {
				fmtObj["description"] = req.Format.Description
			}
			if len(req.Format.Schema) > 0 {
				fmtObj["schema"] = req.Format.Schema
			}
			if req.Format.Strict != nil {
				fmtObj["strict"] = *req.Format.Strict
			}
		}
		out.Text = &outRespText{Format: fmtObj}
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
		out.Tools = append(out.Tools, outRespTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
			Strict:      strictPtr,
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
					"name": req.ToolChoice.Name,
				}
			}
		}
	}

	for _, msg := range ir.NormalizeToolTurns(req.Messages) {
		switch msg.Role {
		case ir.RoleSystem:
			txt := ir.TextOf(msg.Parts)
			if txt != "" {
				if out.Instructions != "" {
					out.Instructions += "\n\n" + txt
				} else {
					out.Instructions = txt
				}
			}

		case ir.RoleUser:
			content := buildUserInputItems(msg.Parts)
			out.Input = append(out.Input, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": content,
			})

		case ir.RoleAssistant:
			var textParts []map[string]any
			var thoughts string
			var toolCalls []ir.Part
			for _, p := range msg.Parts {
				switch p.Kind {
				case ir.KindThought:
					thoughts += p.Text
				case ir.KindText, ir.KindRefusal, "":
					if p.Text != "" {
						textParts = append(textParts, map[string]any{
							"type": "output_text",
							"text": p.Text,
						})
					}
				case ir.KindToolCall:
					toolCalls = append(toolCalls, p)
				}
			}
			if thoughts != "" {
				out.Input = append(out.Input, map[string]any{
					"type": "reasoning",
					"summary": []map[string]any{{
						"type": "summary_text",
						"text": thoughts,
					}},
				})
			}
			if len(textParts) > 0 {
				out.Input = append(out.Input, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": textParts,
				})
			}
			for _, tc := range toolCalls {
				cid := tc.CallID
				if cid == "" {
					cid = ir.NewID("call_")
				}
				out.Input = append(out.Input, map[string]any{
					"type":      "function_call",
					"call_id":   cid,
					"name":      tc.Name,
					"arguments": ir.ArgsOrEmpty(tc.Args),
				})
			}

		case ir.RoleTool:
			for _, p := range msg.Parts {
				if p.Kind != ir.KindToolResult {
					continue
				}
				out.Input = append(out.Input, map[string]any{
					"type":    "function_call_output",
					"call_id": p.CallID,
					"output":  p.Result,
				})
			}
		}
	}

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, ir.NewError(500, "marshal_error", err.Error())
	}
	return raw, nil
}

func buildUserInputItems(parts []ir.Part) []map[string]any {
	var items []map[string]any
	for _, p := range parts {
		switch p.Kind {
		case ir.KindText, "":
			if p.Text != "" {
				items = append(items, map[string]any{"type": "input_text", "text": p.Text})
			}
		case ir.KindImage:
			u := p.URL
			if u == "" && len(p.Data) > 0 {
				u = codec.EncodeDataURL(p.Mime, p.Data)
			}
			if u != "" {
				item := map[string]any{
					"type":      "input_image",
					"image_url": u,
				}
				if p.Detail != "" {
					item["detail"] = p.Detail
				}
				items = append(items, item)
			}
		case ir.KindFile:
			fileData := p.URL
			if fileData == "" && len(p.Data) > 0 {
				fileData = codec.EncodeDataURL(p.Mime, p.Data)
			}
			if fileData != "" {
				item := map[string]any{
					"type":      "input_file",
					"file_data": fileData,
				}
				if p.Filename != "" {
					item["filename"] = p.Filename
				}
				items = append(items, item)
			}
		}
	}
	if len(items) == 0 {
		items = append(items, map[string]any{"type": "input_text", "text": ""})
	}
	return items
}
