package format

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

type Image struct {
	Data []byte
	MIME string
	URL  string // Remote URL passthrough (when data not yet fetched)
}

func BuildToolPrompt(defs []models.GoogleFunctionDeclaration) string {
	specBytes, _ := json.Marshal(defs)
	// Tool slimming: if tool definitions exceed 30KB, re-marshal with name+description only.
	if len(specBytes) > 30000 {
		slimmed := make([]models.GoogleFunctionDeclaration, len(defs))
		for i, d := range defs {
			slimmed[i] = models.GoogleFunctionDeclaration{
				Name:        d.Name,
				Description: d.Description,
			}
		}
		specBytes, _ = json.Marshal(slimmed)
	}
	return fmt.Sprintf(
		"# Tool Use\n\n"+
			"You can call the following tools to help accomplish tasks. "+
			"These tools connect to the user's local environment and will execute when called.\n\n"+
			"Call format (use this exact format):\n"+
			"```function_call\n"+
			"{\"name\": \"<tool_name>\", \"args\": {<arguments>}}\n"+
			"```\n\n"+
			"When calling tools:\n"+
			"- Output ONLY the function_call block(s), nothing else\n"+
			"- You may call multiple tools with multiple blocks\n"+
			"- After receiving a [Tool result for ...], use that data to answer the user\n\n"+
			"Available tools:\n%s",
		string(specBytes),
	)
}

func GoogleToolChoiceInstruction(req models.GoogleGenerateRequest) string {
	mode := "AUTO"
	if req.ToolConfig != nil && req.ToolConfig.FunctionCallingConfig != nil {
		if req.ToolConfig.FunctionCallingConfig.Mode != "" {
			mode = req.ToolConfig.FunctionCallingConfig.Mode
		}
	}

	if mode == "NONE" {
		return "\n\nIMPORTANT: Do NOT call any tools. Respond with text only."
	}
	if mode == "ANY" {
		if req.ToolConfig != nil && req.ToolConfig.FunctionCallingConfig != nil && len(req.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) > 0 {
			var names []string
			for _, s := range req.ToolConfig.FunctionCallingConfig.AllowedFunctionNames {
				names = append(names, fmt.Sprintf("\"%s\"", s))
			}
			if len(names) > 0 {
				return fmt.Sprintf("\n\nIMPORTANT: You MUST call one of these tools: %s. Do not respond with text only.", strings.Join(names, ", "))
			}
		}
		return "\n\nIMPORTANT: You MUST call at least one tool. Do not respond with text only."
	}
	return ""
}

func GoogleContentsToPrompt(req models.GoogleGenerateRequest) (string, []Image, error) {
	var parts []string
	var images []Image

	fcMode := "AUTO"
	if req.ToolConfig != nil && req.ToolConfig.FunctionCallingConfig != nil && req.ToolConfig.FunctionCallingConfig.Mode != "" {
		fcMode = req.ToolConfig.FunctionCallingConfig.Mode
	}

	var toolDefs []models.GoogleFunctionDeclaration
	if fcMode != "NONE" {
		for _, toolGroup := range req.Tools {
			toolDefs = append(toolDefs, toolGroup.FunctionDeclarations...)
		}
	}

	var sysText string
	if req.SystemInstruction != nil {
		var tParts []string
		for _, p := range req.SystemInstruction.Parts {
			if p.Text != "" {
				tParts = append(tParts, p.Text)
			}
		}
		sysText = strings.Join(tParts, " ")
	}

	if sysText != "" {
		if len(toolDefs) > 0 {
			constraint := GoogleToolChoiceInstruction(req)
			parts = append(parts, sysText+"\n\n"+BuildToolPrompt(toolDefs)+constraint)
		} else {
			parts = append(parts, sysText)
		}
	} else if len(toolDefs) > 0 {
		constraint := GoogleToolChoiceInstruction(req)
		parts = append(parts, BuildToolPrompt(toolDefs)+constraint)
	}

	for _, content := range req.Contents {
		role := content.Role
		if role == "" {
			role = "user"
		}

		var msgParts []string
		for _, p := range content.Parts {
			if p.Text != "" {
				msgParts = append(msgParts, p.Text)
			} else if p.InlineData != nil {
				mime := p.InlineData.MIMEType
				if mime == "" {
					mime = "image/png"
				}
				if dec, err := base64.StdEncoding.DecodeString(p.InlineData.Data); err == nil {
					images = append(images, Image{Data: dec, MIME: mime})
					msgParts = append(msgParts, "[Image attached]")
				}
			} else if p.FunctionCall != nil {
				args := p.FunctionCall.Args
				if args == nil {
					args = map[string]any{}
				}
				payload, _ := json.Marshal(map[string]any{"name": p.FunctionCall.Name, "args": args})
				msgParts = append(msgParts, fmt.Sprintf("```function_call\n%s\n```", string(payload)))
			} else if p.FunctionResponse != nil {
				resp := p.FunctionResponse.Response
				if resp == nil {
					resp = map[string]any{}
				}
				payload, _ := json.Marshal(resp)
				msgParts = append(msgParts, fmt.Sprintf("[Tool result for %s]: %s", p.FunctionResponse.Name, string(payload)))
			}
		}

		text := strings.Join(msgParts, "\n")
		if role == "model" {
			parts = append(parts, fmt.Sprintf("[Assistant]: %s", text))
		} else {
			parts = append(parts, text)
		}
	}

	return strings.Join(parts, "\n\n"), images, nil
}

var (
	reFencedGoogleCall   = regexp.MustCompile(`(?s)\x60\x60\x60function_call\s*\n(.*?)\n\x60\x60\x60`)
	reUnfencedGoogleCall = regexp.MustCompile(`(?s)(?:^|\n)function_call\s*\n(\{[^\x60]*?\})`)
)

func ParseGoogleFunctionCalls(text string) (string, []models.GoogleFunctionCall) {
	var calls []models.GoogleFunctionCall
	clean := text

	for _, m := range reFencedGoogleCall.FindAllStringSubmatch(clean, -1) {
		var data map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &data); err == nil {
			if name, ok := data["name"].(string); ok && name != "" {
				args, _ := data["args"].(map[string]any)
				if args == nil {
					args, _ = data["arguments"].(map[string]any)
				}
				if args == nil {
					args = map[string]any{}
				}
				calls = append(calls, models.GoogleFunctionCall{Name: name, Args: args})
			}
		}
	}
	clean = strings.TrimSpace(reFencedGoogleCall.ReplaceAllString(clean, ""))

	for _, m := range reUnfencedGoogleCall.FindAllStringSubmatch(clean, -1) {
		var data map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &data); err == nil {
			if name, ok := data["name"].(string); ok && name != "" {
				args, _ := data["args"].(map[string]any)
				if args == nil {
					args, _ = data["arguments"].(map[string]any)
				}
				if args == nil {
					args = map[string]any{}
				}
				calls = append(calls, models.GoogleFunctionCall{Name: name, Args: args})
			}
		}
	}
	clean = strings.TrimSpace(reUnfencedGoogleCall.ReplaceAllString(clean, ""))

	if len(calls) == 0 && strings.HasPrefix(strings.TrimSpace(clean), "{") {
		var data map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(clean)), &data); err == nil {
			if name, ok := data["name"].(string); ok && name != "" {
				args, hasArgs := data["args"].(map[string]any)
				if !hasArgs {
					args, hasArgs = data["arguments"].(map[string]any)
				}
				if hasArgs {
					calls = append(calls, models.GoogleFunctionCall{Name: name, Args: args})
					clean = ""
				}
			}
		}
	}

	return clean, calls
}
