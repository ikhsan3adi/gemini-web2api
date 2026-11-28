package format

import (
	"encoding/json"
	"fmt"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

// AnthropicToChatRequest converts an Anthropic Messages API request payload
// into an OpenAIChatRequest so prompt construction, image extraction, and
// tool definitions share the same pipeline.
func AnthropicToChatRequest(req map[string]any) (models.OpenAIChatRequest, error) {
	var out models.OpenAIChatRequest
	out.Model, _ = req["model"].(string)
	out.Stream, _ = req["stream"].(bool)

	if sysText := extractAnthropicSystem(req["system"]); sysText != "" {
		out.Messages = append(out.Messages, models.OpenAIMessage{
			Role:    "system",
			Content: sysText,
		})
	}

	rawMsgs, _ := req["messages"].([]any)
	toolNamesByID := make(map[string]string)
	for _, raw := range rawMsgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "" {
			role = "user"
		}
		converted := convertAnthropicMessage(role, m["content"], toolNamesByID)
		out.Messages = append(out.Messages, converted...)
	}

	if rawTools, ok := req["tools"].([]any); ok {
		for _, t := range rawTools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			name, _ := tm["name"].(string)
			if name == "" {
				continue
			}
			desc, _ := tm["description"].(string)
			params := tm["input_schema"]
			if params == nil {
				params = tm["parameters"]
			}
			out.Tools = append(out.Tools, models.OpenAITool{
				Type: "function",
				Function: models.OpenAIFunction{
					Name:        name,
					Description: desc,
					Parameters:  params,
				},
			})
		}
	}

	out.ToolChoice = convertAnthropicToolChoice(req["tool_choice"])
	return out, nil
}

func extractAnthropicSystem(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var out string
		for _, item := range v {
			var chunk string
			switch blk := item.(type) {
			case string:
				chunk = blk
			case map[string]any:
				if t, _ := blk["type"].(string); t == "" || t == "text" {
					chunk, _ = blk["text"].(string)
				}
			}
			if chunk != "" {
				if out != "" {
					out += "\n"
				}
				out += chunk
			}
		}
		return out
	default:
		return ""
	}
}

func convertAnthropicMessage(role string, content any, toolNamesByID map[string]string) []models.OpenAIMessage {
	if s, ok := content.(string); ok {
		return []models.OpenAIMessage{{Role: role, Content: s}}
	}

	blocks, ok := content.([]any)
	if !ok {
		return nil
	}

	var (
		contentParts []any
		toolCalls    []models.OpenAIToolCall
		toolResults  []models.OpenAIMessage
	)

	for _, item := range blocks {
		blk, ok := item.(map[string]any)
		if !ok {
			if s, ok := item.(string); ok && s != "" {
				contentParts = append(contentParts, map[string]any{"type": "text", "text": s})
			}
			continue
		}

		bType, _ := blk["type"].(string)
		switch bType {
		case "text":
			if t, _ := blk["text"].(string); t != "" {
				contentParts = append(contentParts, map[string]any{"type": "text", "text": t})
			}
		case "image":
			contentParts = append(contentParts, normalizeAnthropicImage(blk))
		case "tool_use":
			id, _ := blk["id"].(string)
			name, _ := blk["name"].(string)
			if id != "" && name != "" {
				toolNamesByID[id] = name
			}
			argsStr := "{}"
			if input, exists := blk["input"]; exists && input != nil {
				if b, err := json.Marshal(input); err == nil {
					argsStr = string(b)
				}
			}
			toolCalls = append(toolCalls, models.OpenAIToolCall{
				ID:   id,
				Type: "function",
				Function: models.OpenAIToolCallFunction{
					Name:      name,
					Arguments: argsStr,
				},
			})
		case "tool_result":
			toolID, _ := blk["tool_use_id"].(string)
			toolName := toolNamesByID[toolID]
			if toolName == "" {
				toolName = toolID
			}
			isErr, _ := blk["is_error"].(bool)
			resContent := normalizeAnthropicToolResult(blk["content"], isErr)
			toolResults = append(toolResults, models.OpenAIMessage{
				Role:    "tool",
				Name:    toolName,
				Content: resContent,
			})
		}
	}

	var out []models.OpenAIMessage
	out = append(out, toolResults...)
	if len(contentParts) > 0 || len(toolCalls) > 0 {
		var msgContent any = ""
		if len(contentParts) > 0 {
			msgContent = contentParts
		}
		out = append(out, models.OpenAIMessage{
			Role:      role,
			Content:   msgContent,
			ToolCalls: toolCalls,
		})
	}
	return out
}

func normalizeAnthropicImage(blk map[string]any) map[string]any {
	src, ok := blk["source"].(map[string]any)
	if !ok {
		return blk
	}
	srcType, _ := src["type"].(string)
	mediaType, _ := src["media_type"].(string)
	switch srcType {
	case "base64":
		data, _ := src["data"].(string)
		return map[string]any{
			"type":       "image",
			"media_type": mediaType,
			"data":       data,
		}
	case "url":
		u, _ := src["url"].(string)
		return map[string]any{
			"type":       "image",
			"media_type": mediaType,
			"url":        u,
		}
	default:
		return blk
	}
}

func normalizeAnthropicToolResult(raw any, isErr bool) any {
	switch v := raw.(type) {
	case string:
		if isErr {
			return "[Error] " + v
		}
		return v
	case []any:
		parts := make([]any, 0, len(v)+1)
		if isErr {
			parts = append(parts, map[string]any{"type": "text", "text": "[Error]"})
		}
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, _ := m["type"].(string); t == "image" {
					parts = append(parts, normalizeAnthropicImage(m))
					continue
				}
			}
			parts = append(parts, item)
		}
		return parts
	default:
		if v == nil {
			if isErr {
				return "[Error]"
			}
			return ""
		}
		return fmt.Sprintf("%v", v)
	}
}

func convertAnthropicToolChoice(raw any) any {
	if raw == nil {
		return "auto"
	}
	if s, ok := raw.(string); ok {
		return s
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "auto"
	}
	t, _ := m["type"].(string)
	switch t {
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		if name, _ := m["name"].(string); name != "" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": name,
				},
			}
		}
		return "auto"
	default:
		return "auto"
	}
}
