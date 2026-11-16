package gemini

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

const skipThoughtSignatureValidator = "skip_thought_signature_validator"

// BuildRequest encodes an IR Request into a Google Gemini v1beta generateContent JSON payload.
func (c *Codec) BuildRequest(req *ir.Request, upstreamModel string, _ bool, _ codec.Quirks) ([]byte, *ir.GatewayError) {
	if req == nil {
		return nil, ir.NewError(400, "empty_request", "request is required")
	}

	model := upstreamModel
	if model == "" {
		model = req.Model
	}

	payload := make(map[string]any)

	sysText := req.SystemText()
	for _, msg := range req.Messages {
		if msg.Role == ir.RoleSystem {
			if t := ir.TextOf(msg.Parts); t != "" {
				if sysText != "" {
					sysText += "\n\n" + t
				} else {
					sysText = t
				}
			}
		}
	}
	if sysText != "" {
		payload["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": sysText}},
		}
	}

	genCfg := make(map[string]any)
	if req.MaxTokens > 0 {
		genCfg["maxOutputTokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		genCfg["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genCfg["topP"] = *req.TopP
	}
	if req.TopK != nil {
		genCfg["topK"] = *req.TopK
	}
	if req.Seed != nil {
		genCfg["seed"] = *req.Seed
	}
	if len(req.Stop) > 0 {
		genCfg["stopSequences"] = req.Stop
	}
	if req.PresencePenalty != nil {
		genCfg["presencePenalty"] = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		genCfg["frequencyPenalty"] = *req.FrequencyPenalty
	}
	if req.Format != nil && (req.Format.Type == "json_object" || req.Format.Type == "json_schema") {
		genCfg["responseMimeType"] = "application/json"
		if req.Format.Type == "json_schema" && len(req.Format.Schema) > 0 {
			genCfg["responseSchema"] = CleanSchemaForGemini(req.Format.Schema)
		}
	}
	if tc := buildThinkingConfig(model, req.Reasoning); tc != nil {
		genCfg["thinkingConfig"] = tc
	}
	if len(genCfg) > 0 {
		payload["generationConfig"] = genCfg
	}

	if len(req.Tools) > 0 {
		decls := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			d := map[string]any{
				"name":       t.Name,
				"parameters": CleanSchemaForGemini(t.Schema),
			}
			if t.Description != "" {
				d["description"] = t.Description
			}
			decls = append(decls, d)
		}
		payload["tools"] = []map[string]any{{"functionDeclarations": decls}}

		switch req.ToolChoice.Mode {
		case "auto":
			payload["toolConfig"] = map[string]any{
				"functionCallingConfig": map[string]any{"mode": "AUTO"},
			}
		case "none":
			payload["toolConfig"] = map[string]any{
				"functionCallingConfig": map[string]any{"mode": "NONE"},
			}
		case "required":
			payload["toolConfig"] = map[string]any{
				"functionCallingConfig": map[string]any{"mode": "ANY"},
			}
		case "tool":
			if req.ToolChoice.Name != "" {
				payload["toolConfig"] = map[string]any{
					"functionCallingConfig": map[string]any{
						"mode":                 "ANY",
						"allowedFunctionNames": []string{req.ToolChoice.Name},
					},
				}
			}
		}
	}

	contents := buildGeminiContents(req.Messages)
	if len(contents) == 0 {
		contents = []map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": " "}},
		}}
	}
	payload["contents"] = contents

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, ir.NewError(500, "marshal_error", err.Error())
	}
	return raw, nil
}

func isGeminiThinkingLevelModel(lowerModel string) bool {
	return strings.Contains(lowerModel, "gemini-3") ||
		strings.Contains(lowerModel, "gemini-4") ||
		strings.Contains(lowerModel, "carbon") ||
		strings.Contains(lowerModel, "hydrogen") ||
		strings.Contains(lowerModel, "argon")
}

func buildThinkingConfig(model string, r ir.Reasoning) map[string]any {
	eff := ir.EffectiveEffort(r)
	if eff == "" && r.Budget == 0 && !r.Raw {
		return nil
	}
	lowerModel := strings.ToLower(model)
	usesLevel := isGeminiThinkingLevelModel(lowerModel)

	if eff == "none" {
		if usesLevel && !strings.Contains(lowerModel, "flash") {
			return map[string]any{"thinkingLevel": "LOW"}
		}
		if usesLevel {
			return map[string]any{"thinkingLevel": "MINIMAL"}
		}
		return map[string]any{"thinkingBudget": 0}
	}

	if usesLevel {
		lvl := "MEDIUM"
		switch eff {
		case "minimal":
			lvl = "MINIMAL"
		case "low":
			lvl = "LOW"
		case "high", "xhigh", "max":
			lvl = "HIGH"
		}
		return map[string]any{
			"thinkingLevel":   lvl,
			"includeThoughts": true,
		}
	}

	budget := r.Budget
	if budget == 0 {
		budget = ir.EffortToBudget(eff)
	}
	if budget == 0 {
		budget = -1
	}
	return map[string]any{
		"thinkingBudget":  budget,
		"includeThoughts": true,
	}
}

func buildGeminiContents(msgs []ir.Message) []map[string]any {
	callNames := make(map[string]string)
	for _, msg := range msgs {
		if msg.Role == ir.RoleAssistant {
			for _, p := range msg.Parts {
				if p.Kind == ir.KindToolCall && p.CallID != "" && p.Name != "" {
					callNames[p.CallID] = p.Name
				}
			}
		}
	}

	type gemTurn struct {
		role  string
		parts []map[string]any
		calls []ir.Part
	}
	var turns []gemTurn

	appendTurn := func(role string, parts []map[string]any, calls []ir.Part) {
		if len(parts) == 0 {
			return
		}
		if len(turns) > 0 && turns[len(turns)-1].role == role {
			turns[len(turns)-1].parts = append(turns[len(turns)-1].parts, parts...)
			turns[len(turns)-1].calls = append(turns[len(turns)-1].calls, calls...)
			return
		}
		turns = append(turns, gemTurn{role: role, parts: parts, calls: calls})
	}

	for _, msg := range msgs {
		switch msg.Role {
		case ir.RoleUser:
			var parts []map[string]any
			for _, p := range msg.Parts {
				if gp, ok := partToGeminiUserPart(p); ok {
					parts = append(parts, gp)
				}
			}
			appendTurn("user", parts, nil)

		case ir.RoleTool:
			var parts []map[string]any
			for _, p := range msg.Parts {
				if p.Kind != ir.KindToolResult {
					continue
				}
				name := p.Name
				if name == "" {
					name = callNames[p.CallID]
				}
				if name == "" {
					name = "function"
				}
				respObj := buildFunctionResponseObj(p.Result)
				fr := map[string]any{
					"name":     name,
					"response": respObj,
				}
				if p.CallID != "" {
					fr["id"] = p.CallID
				}
				parts = append(parts, map[string]any{"functionResponse": fr})
			}
			appendTurn("user", parts, nil)

		case ir.RoleAssistant:
			var parts []map[string]any
			var calls []ir.Part
			// Track if first functionCall in turn already carries a thoughtSignature.
			firstCallSigned := false
			for _, p := range msg.Parts {
				switch p.Kind {
				case ir.KindThought:
					if p.Text != "" {
						gp := map[string]any{
							"thought": true,
							"text":    p.Text,
						}
						if p.Signature != "" {
							gp["thoughtSignature"] = p.Signature
						}
						parts = append(parts, gp)
					}
				case ir.KindText, ir.KindRefusal, "":
					if p.Text != "" {
						parts = append(parts, map[string]any{"text": p.Text})
					}
				case ir.KindToolCall:
					calls = append(calls, p)
					fc := map[string]any{
						"name": p.Name,
						"args": ir.ParseArgs(p.Args),
					}
					if p.CallID != "" {
						fc["id"] = p.CallID
					}
					gp := map[string]any{"functionCall": fc}
					sig := p.ThoughtSignature
					if sig == "" {
						sig = ir.DefaultSigStore.LookupCall(p.CallID)
					}
					if sig == "" && !firstCallSigned {
						sig = skipThoughtSignatureValidator
					}
					if sig != "" {
						gp["thoughtSignature"] = sig
						firstCallSigned = true
					}
					parts = append(parts, gp)
				}
			}
			appendTurn("model", parts, calls)
		}
	}

	// Ensure alternating roles starting with user and matching functionCall -> functionResponse pairs.
	out := make([]map[string]any, 0, len(turns))
	if len(turns) > 0 && turns[0].role != "user" {
		out = append(out, map[string]any{
			"role":  "user",
			"parts": []map[string]any{{"text": "Continue."}},
		})
	}
	for i := 0; i < len(turns); i++ {
		t := turns[i]
		out = append(out, map[string]any{
			"role":  t.role,
			"parts": t.parts,
		})
		if t.role == "model" && len(t.calls) > 0 {
			hasNextUser := i+1 < len(turns) && turns[i+1].role == "user"
			if !hasNextUser {
				var synthetic []map[string]any
				for _, c := range t.calls {
					fr := map[string]any{
						"name":     c.Name,
						"response": map[string]any{"result": "completed"},
					}
					if c.CallID != "" {
						fr["id"] = c.CallID
					}
					synthetic = append(synthetic, map[string]any{"functionResponse": fr})
				}
				out = append(out, map[string]any{
					"role":  "user",
					"parts": synthetic,
				})
			}
		}
	}
	return out
}

func partToGeminiUserPart(p ir.Part) (map[string]any, bool) {
	switch p.Kind {
	case ir.KindText, "":
		if p.Text == "" {
			return nil, false
		}
		return map[string]any{"text": p.Text}, true
	case ir.KindImage, ir.KindFile:
		data, mime := p.Data, p.Mime
		if len(data) == 0 && p.URL != "" {
			if m, d, ok := codec.DecodeDataURL(p.URL); ok {
				mime, data = m, d
			} else {
				if mime == "" {
					if p.Kind == ir.KindImage {
						mime = "image/jpeg"
					} else {
						mime = "application/octet-stream"
					}
				}
				return map[string]any{
					"fileData": map[string]any{
						"mimeType": mime,
						"fileUri":  p.URL,
					},
				}, true
			}
		}
		if len(data) > 0 {
			if mime == "" {
				mime = "image/png"
			}
			return map[string]any{
				"inlineData": map[string]any{
					"mimeType": mime,
					"data":     base64.StdEncoding.EncodeToString(data),
				},
			}, true
		}
	}
	return nil, false
}

func buildFunctionResponseObj(result string) map[string]any {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return map[string]any{"result": ""}
	}
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) == nil && obj != nil {
		return obj
	}
	return map[string]any{"result": result}
}
