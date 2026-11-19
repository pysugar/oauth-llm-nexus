package gemini

import (
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

type gemInRequest struct {
	SystemInstruction *gemInContent    `json:"systemInstruction,omitempty"`
	Contents          []gemInContent   `json:"contents"`
	Tools             []gemInToolEntry `json:"tools,omitempty"`
	ToolConfig        *gemInToolConfig `json:"toolConfig,omitempty"`
	GenerationConfig  *gemInGenConfig  `json:"generationConfig,omitempty"`
}

type gemInContent struct {
	Role  string      `json:"role,omitempty"`
	Parts []gemInPart `json:"parts"`
}

type gemInPart struct {
	Text             string             `json:"text,omitempty"`
	Thought          bool               `json:"thought,omitempty"`
	ThoughtSignature string             `json:"thoughtSignature,omitempty"`
	InlineData       *gemInBlob         `json:"inlineData,omitempty"`
	FileData         *gemInFile         `json:"fileData,omitempty"`
	FunctionCall     *gemInFuncCall     `json:"functionCall,omitempty"`
	FunctionResponse *gemInFuncResponse `json:"functionResponse,omitempty"`
}

type gemInBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gemInFile struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gemInFuncCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type gemInFuncResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gemInToolEntry struct {
	FunctionDeclarations []gemInFuncDecl `json:"functionDeclarations,omitempty"`
}

type gemInFuncDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJsonSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type gemInToolConfig struct {
	FunctionCallingConfig *struct {
		Mode                 string   `json:"mode"`
		AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
	} `json:"functionCallingConfig,omitempty"`
}

type gemInGenConfig struct {
	MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"topP,omitempty"`
	TopK               *int            `json:"topK,omitempty"`
	Seed               *int64          `json:"seed,omitempty"`
	StopSequences      []string        `json:"stopSequences,omitempty"`
	PresencePenalty    *float64        `json:"presencePenalty,omitempty"`
	FrequencyPenalty   *float64        `json:"frequencyPenalty,omitempty"`
	ResponseMimeType   string          `json:"responseMimeType,omitempty"`
	ResponseSchema     json.RawMessage `json:"responseSchema,omitempty"`
	ResponseJsonSchema json.RawMessage `json:"responseJsonSchema,omitempty"`
	ThinkingConfig     *struct {
		ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
		ThinkingLevel   string `json:"thinkingLevel,omitempty"`
		IncludeThoughts *bool  `json:"includeThoughts,omitempty"`
	} `json:"thinkingConfig,omitempty"`
}

// ParseRequest parses a Google Gemini v1beta generateContent payload into the gateway IR.
func (c *Codec) ParseRequest(modelHint string, body []byte) (*ir.Request, *ir.GatewayError) {
	normalized := NormalizeKeysCamel(body)
	var in gemInRequest
	if err := json.Unmarshal(normalized, &in); err != nil {
		return nil, ir.NewError(400, "invalid_json", "invalid Gemini request JSON: "+err.Error())
	}

	model := strings.TrimSpace(modelHint)
	req := &ir.Request{
		Source: ir.ProtoGemini,
		Model:  model,
	}

	if in.SystemInstruction != nil {
		for _, p := range in.SystemInstruction.Parts {
			if p.Text != "" {
				req.System = append(req.System, ir.Part{Kind: ir.KindText, Text: p.Text})
			}
		}
	}

	if gc := in.GenerationConfig; gc != nil {
		req.MaxTokens = gc.MaxOutputTokens
		req.Temperature = gc.Temperature
		req.TopP = gc.TopP
		req.TopK = gc.TopK
		req.Seed = gc.Seed
		req.Stop = gc.StopSequences
		req.PresencePenalty = gc.PresencePenalty
		req.FrequencyPenalty = gc.FrequencyPenalty

		if gc.ResponseMimeType == "application/json" {
			schema := gc.ResponseJsonSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			if len(schema) > 0 && string(schema) != "null" {
				req.Format = &ir.Format{
					Type:   "json_schema",
					Name:   "response_schema",
					Schema: schema,
				}
			} else {
				req.Format = &ir.Format{Type: "json_object"}
			}
		}

		if gc.ThinkingConfig != nil {
			tc := gc.ThinkingConfig
			req.Reasoning.Raw = true
			if tc.ThinkingLevel != "" {
				req.Reasoning.Effort = ir.NormalizeEffort(tc.ThinkingLevel)
			}
			if tc.ThinkingBudget != nil {
				req.Reasoning.Budget = *tc.ThinkingBudget
				if req.Reasoning.Effort == "" {
					req.Reasoning.Effort = ir.BudgetToEffort(*tc.ThinkingBudget)
				}
			}
		}
	}

	for _, te := range in.Tools {
		for _, fd := range te.FunctionDeclarations {
			if fd.Name == "" {
				continue
			}
			schema := fd.ParametersJsonSchema
			if len(schema) == 0 {
				schema = fd.Parameters
			}
			req.Tools = append(req.Tools, ir.Tool{
				Name:        fd.Name,
				Description: fd.Description,
				Schema:      schema,
			})
		}
	}

	if in.ToolConfig != nil && in.ToolConfig.FunctionCallingConfig != nil {
		fcc := in.ToolConfig.FunctionCallingConfig
		switch strings.ToUpper(strings.TrimSpace(fcc.Mode)) {
		case "AUTO":
			req.ToolChoice = ir.ToolChoice{Mode: "auto"}
		case "NONE":
			req.ToolChoice = ir.ToolChoice{Mode: "none"}
		case "ANY":
			if len(fcc.AllowedFunctionNames) == 1 {
				req.ToolChoice = ir.ToolChoice{Mode: "tool", Name: fcc.AllowedFunctionNames[0]}
			} else {
				req.ToolChoice = ir.ToolChoice{Mode: "required"}
			}
		}
	}

	// Track issued call IDs by function name so functionResponse parts without IDs can match FIFO.
	pendingByName := make(map[string][]string)

	for _, c := range in.Contents {
		role := strings.ToLower(strings.TrimSpace(c.Role))
		if role == "model" {
			var parts []ir.Part
			for _, p := range c.Parts {
				switch {
				case p.Thought:
					if p.Text != "" || p.ThoughtSignature != "" {
						parts = append(parts, ir.Part{
							Kind:      ir.KindThought,
							Text:      p.Text,
							Signature: p.ThoughtSignature,
						})
					}
				case p.FunctionCall != nil:
					callID := p.FunctionCall.ID
					if callID == "" {
						callID = ir.StableCallID(len(req.Messages), len(parts), p.FunctionCall.Name, string(p.FunctionCall.Args))
					}
					if p.ThoughtSignature != "" {
						ir.DefaultSigStore.SaveCall(callID, p.ThoughtSignature)
					}
					pendingByName[p.FunctionCall.Name] = append(pendingByName[p.FunctionCall.Name], callID)
					args := "{}"
					if len(p.FunctionCall.Args) > 0 && string(p.FunctionCall.Args) != "null" {
						args = string(p.FunctionCall.Args)
					}
					parts = append(parts, ir.Part{
						Kind:             ir.KindToolCall,
						CallID:           callID,
						Name:             p.FunctionCall.Name,
						Args:             args,
						ThoughtSignature: p.ThoughtSignature,
					})
				case p.Text != "":
					parts = append(parts, ir.Part{Kind: ir.KindText, Text: p.Text})
				}
			}
			if len(parts) > 0 {
				req.Messages = append(req.Messages, ir.Message{Role: ir.RoleAssistant, Parts: parts})
			}
			continue
		}

		// User content may carry functionResponse parts alongside user text/media.
		var toolResults []ir.Part
		var userParts []ir.Part
		for _, p := range c.Parts {
			switch {
			case p.FunctionResponse != nil:
				callID := p.FunctionResponse.ID
				if callID == "" {
					if q := pendingByName[p.FunctionResponse.Name]; len(q) > 0 {
						callID = q[0]
						pendingByName[p.FunctionResponse.Name] = q[1:]
					} else {
						callID = p.FunctionResponse.Name
					}
				}
				resStr := extractFunctionResponseText(p.FunctionResponse.Response)
				toolResults = append(toolResults, ir.Part{
					Kind:   ir.KindToolResult,
					CallID: callID,
					Name:   p.FunctionResponse.Name,
					Result: resStr,
				})
			case p.InlineData != nil && p.InlineData.Data != "":
				decoded, err := codec.DecodeBase64Lenient(p.InlineData.Data)
				if err == nil {
					kind := ir.KindFile
					if strings.HasPrefix(p.InlineData.MimeType, "image/") {
						kind = ir.KindImage
					}
					userParts = append(userParts, ir.Part{
						Kind: kind,
						Mime: p.InlineData.MimeType,
						Data: decoded,
						URL:  codec.EncodeDataURL(p.InlineData.MimeType, decoded),
					})
				}
			case p.FileData != nil && p.FileData.FileURI != "":
				kind := ir.KindFile
				if strings.HasPrefix(p.FileData.MimeType, "image/") {
					kind = ir.KindImage
				}
				userParts = append(userParts, ir.Part{
					Kind: kind,
					Mime: p.FileData.MimeType,
					URL:  p.FileData.FileURI,
				})
			case p.Text != "":
				userParts = append(userParts, ir.Part{Kind: ir.KindText, Text: p.Text})
			}
		}
		if len(toolResults) > 0 {
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleTool, Parts: toolResults})
		}
		if len(userParts) > 0 {
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleUser, Parts: userParts})
		}
	}

	return req, nil
}

func extractFunctionResponseText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil && obj != nil {
		// Unwrap common {"result": "..."} wrapper if single field.
		if len(obj) == 1 {
			if r, ok := obj["result"]; ok {
				if rs, ok := r.(string); ok {
					return rs
				}
			}
			if r, ok := obj["output"]; ok {
				if rs, ok := r.(string); ok {
					return rs
				}
			}
		}
	}
	return string(raw)
}
