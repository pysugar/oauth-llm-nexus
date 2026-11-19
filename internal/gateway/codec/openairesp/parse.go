package openairesp

import (
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type respInRequest struct {
	Model              string            `json:"model"`
	Instructions       string            `json:"instructions,omitempty"`
	Input              json.RawMessage   `json:"input"`
	Tools              []respInTool      `json:"tools,omitempty"`
	ToolChoice         json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool             `json:"parallel_tool_calls,omitempty"`
	Stream             bool              `json:"stream,omitempty"`
	MaxOutputTokens    int               `json:"max_output_tokens,omitempty"`
	Temperature        *float64          `json:"temperature,omitempty"`
	TopP               *float64          `json:"top_p,omitempty"`
	Reasoning          *respInReasoning  `json:"reasoning,omitempty"`
	Text               *respInTextConfig `json:"text,omitempty"`
	User               string            `json:"user,omitempty"`
	Store              *bool             `json:"store,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
}

type respInReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type respInTextConfig struct {
	Format *respInFormat `json:"format,omitempty"`
}

type respInFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type respInTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Function    *struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
		Strict      *bool           `json:"strict,omitempty"`
	} `json:"function,omitempty"`
}

type respInItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Summary   []respTextItem  `json:"summary,omitempty"`
}

type respTextItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ParseRequest parses an OpenAI Responses API JSON request into the gateway IR.
func (c *Codec) ParseRequest(modelHint string, body []byte) (*ir.Request, *ir.GatewayError) {
	var in respInRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, ir.NewError(400, "invalid_json", "invalid OpenAI responses JSON: "+err.Error())
	}

	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = strings.TrimSpace(modelHint)
	}
	if model == "" {
		return nil, ir.NewError(400, "missing_model", "model is required")
	}

	req := &ir.Request{
		Source:             ir.ProtoOpenAIResp,
		Model:              model,
		Stream:             in.Stream,
		MaxTokens:          in.MaxOutputTokens,
		Temperature:        in.Temperature,
		TopP:               in.TopP,
		ParallelToolCalls:  in.ParallelToolCalls,
		User:               in.User,
		Store:              in.Store,
		PreviousResponseID: strings.TrimSpace(in.PreviousResponseID),
	}

	if in.Instructions != "" {
		req.System = append(req.System, ir.Part{Kind: ir.KindText, Text: in.Instructions})
	}

	if in.Reasoning != nil {
		if eff := ir.NormalizeEffort(in.Reasoning.Effort); eff != "" {
			req.Reasoning = ir.Reasoning{Effort: eff, Raw: true}
		}
	}

	if in.Text != nil && in.Text.Format != nil && in.Text.Format.Type != "" {
		req.Format = &ir.Format{
			Type:        in.Text.Format.Type,
			Name:        in.Text.Format.Name,
			Description: in.Text.Format.Description,
			Schema:      in.Text.Format.Schema,
			Strict:      in.Text.Format.Strict,
		}
	}

	for _, t := range in.Tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		name, desc, params := t.Name, t.Description, t.Parameters
		strict := t.Strict != nil && *t.Strict
		if name == "" && t.Function != nil {
			name = t.Function.Name
			desc = t.Function.Description
			params = t.Function.Parameters
			strict = t.Function.Strict != nil && *t.Function.Strict
		}
		if name == "" {
			continue
		}
		req.Tools = append(req.Tools, ir.Tool{
			Name:        name,
			Description: desc,
			Schema:      params,
			Strict:      strict,
		})
	}

	if len(in.ToolChoice) > 0 && string(in.ToolChoice) != "null" {
		req.ToolChoice = parseRespToolChoice(in.ToolChoice)
	}

	if err := parseRespInput(in.Input, req); err != nil {
		return nil, err
	}

	return req, nil
}

func parseRespToolChoice(raw json.RawMessage) ir.ToolChoice {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return ir.ToolChoice{Mode: strings.ToLower(str)}
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Name != "" {
		return ir.ToolChoice{Mode: "tool", Name: obj.Name}
	}
	return ir.ToolChoice{}
}

func parseRespInput(raw json.RawMessage, req *ir.Request) *ir.GatewayError {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if str != "" {
			req.Messages = append(req.Messages, ir.Message{
				Role:  ir.RoleUser,
				Parts: []ir.Part{{Kind: ir.KindText, Text: str}},
			})
		}
		return nil
	}

	var items []respInItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return ir.NewError(400, "invalid_input", "input must be a string or array of items")
	}

	appendAssistantPart := func(p ir.Part) {
		n := len(req.Messages)
		if n > 0 && req.Messages[n-1].Role == ir.RoleAssistant {
			req.Messages[n-1].Parts = append(req.Messages[n-1].Parts, p)
			return
		}
		req.Messages = append(req.Messages, ir.Message{
			Role:  ir.RoleAssistant,
			Parts: []ir.Part{p},
		})
	}

	for _, it := range items {
		itemType := it.Type
		if itemType == "" && it.Role != "" {
			itemType = "message"
		}
		switch itemType {
		case "message":
			role := strings.ToLower(strings.TrimSpace(it.Role))
			parts := parseRespContent(it.Content)
			switch role {
			case "system", "developer":
				for _, p := range parts {
					if p.Text != "" {
						req.System = append(req.System, ir.Part{Kind: ir.KindText, Text: p.Text})
					}
				}
			case "assistant":
				for _, p := range parts {
					appendAssistantPart(p)
				}
			default:
				req.Messages = append(req.Messages, ir.Message{
					Role:  ir.RoleUser,
					Parts: parts,
				})
			}

		case "reasoning":
			var txt strings.Builder
			for _, s := range it.Summary {
				txt.WriteString(s.Text)
			}
			for _, p := range parseRespContent(it.Content) {
				if p.Text != "" {
					txt.WriteString(p.Text)
				}
			}
			if txt.Len() > 0 {
				appendAssistantPart(ir.Part{Kind: ir.KindThought, Text: txt.String()})
			}

		case "function_call":
			callID := it.CallID
			if callID == "" {
				callID = it.ID
			}
			if callID == "" {
				callID = ir.StableCallID(len(req.Messages), 0, it.Name, it.Arguments)
			}
			appendAssistantPart(ir.Part{
				Kind:   ir.KindToolCall,
				CallID: callID,
				Name:   it.Name,
				Args:   it.Arguments,
			})

		case "function_call_output":
			parts := parseRespContent(it.Output)
			text := ir.TextOf(parts)
			if text == "" && len(it.Output) > 0 {
				var rawStr string
				if json.Unmarshal(it.Output, &rawStr) == nil {
					text = rawStr
				}
			}
			req.Messages = append(req.Messages, ir.Message{
				Role: ir.RoleTool,
				Parts: []ir.Part{{
					Kind:        ir.KindToolResult,
					CallID:      it.CallID,
					Result:      text,
					ResultParts: nonTextRespParts(parts),
				}},
			})
		}
	}

	return nil
}

func parseRespContent(raw json.RawMessage) []ir.Part {
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
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Refusal  string          `json:"refusal"`
		ImageURL json.RawMessage `json:"image_url"`
		Detail   string          `json:"detail"`
		FileData string          `json:"file_data"`
		Filename string          `json:"filename"`
		MimeType string          `json:"mime_type"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}

	var parts []ir.Part
	for _, it := range items {
		switch it.Type {
		case "input_text", "output_text", "text", "reasoning_text", "":
			if it.Text != "" {
				parts = append(parts, ir.Part{Kind: ir.KindText, Text: it.Text})
			}
		case "refusal":
			if it.Refusal != "" {
				parts = append(parts, ir.Part{Kind: ir.KindRefusal, Text: it.Refusal})
			}
		case "input_image":
			urlStr, detail := extractRespImage(it.ImageURL, it.Detail)
			if urlStr != "" {
				p := ir.Part{Kind: ir.KindImage, URL: urlStr, Detail: detail}
				if mime, data, ok := codec.DecodeDataURL(urlStr); ok {
					p.Mime = mime
					p.Data = data
				}
				parts = append(parts, p)
			}
		case "input_file":
			if it.FileData != "" {
				p := ir.Part{
					Kind:     ir.KindFile,
					URL:      it.FileData,
					Filename: it.Filename,
					Mime:     it.MimeType,
				}
				if mime, data, ok := codec.DecodeDataURL(it.FileData); ok {
					p.Mime = mime
					p.Data = data
				}
				parts = append(parts, p)
			}
		}
	}
	return parts
}

func extractRespImage(raw json.RawMessage, fallbackDetail string) (string, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", fallbackDetail
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str, fallbackDetail
	}
	var obj struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		detail := obj.Detail
		if detail == "" {
			detail = fallbackDetail
		}
		return obj.URL, detail
	}
	return "", fallbackDetail
}

func nonTextRespParts(parts []ir.Part) []ir.Part {
	var out []ir.Part
	for _, p := range parts {
		if p.Kind != ir.KindText && p.Kind != "" {
			out = append(out, p)
		}
	}
	return out
}
