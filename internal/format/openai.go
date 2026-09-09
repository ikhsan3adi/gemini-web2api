package format

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

func RandHex(n int) string {
	bytes := make([]byte, (n+1)/2)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)[:n]
}

// DecodeDataURL decodes a `data:<mime>;base64,<payload>` URL into bytes and MIME.
func DecodeDataURL(dataURL string) ([]byte, string, error) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURL, prefix) {
		return nil, "", fmt.Errorf("not a data URL")
	}
	semiIdx := strings.Index(dataURL, ";")
	if semiIdx == -1 || semiIdx+1 >= len(dataURL) {
		return nil, "", fmt.Errorf("invalid data URL format")
	}
	mime := dataURL[len(prefix):semiIdx]

	b64Idx := strings.Index(dataURL, "base64,")
	if b64Idx == -1 {
		return nil, "", fmt.Errorf("data URL must be base64-encoded")
	}
	b64Payload := dataURL[b64Idx+len("base64,"):]
	decoded, err := multimodal.DecodeBase64Raw(b64Payload)
	if err != nil {
		return nil, "", fmt.Errorf("base64 decode failed: %w", err)
	}
	if mime == "" {
		mime = multimodal.DetectImageMime(decoded)
	}
	return decoded, mime, nil
}

// ImageFromURL returns an Image with URL set for remote passthrough.
func ImageFromURL(rawURL string) Image {
	// Try to extract MIME from URL extension
	mime := "image/png"
	if extIdx := strings.LastIndex(rawURL, "."); extIdx != -1 {
		ext := strings.ToLower(rawURL[extIdx:])
		// Strip query params
		if qIdx := strings.Index(ext, "?"); qIdx != -1 {
			ext = ext[:qIdx]
		}
		switch ext {
		case ".jpg", ".jpeg":
			mime = "image/jpeg"
		case ".png":
			mime = "image/png"
		case ".gif":
			mime = "image/gif"
		case ".webp":
			mime = "image/webp"
		case ".bmp":
			mime = "image/bmp"
		case ".svg":
			mime = "image/svg+xml"
		}
	}
	return Image{URL: rawURL, MIME: mime}
}

// ImageFromPart parses an OpenAI-style content part (image_url, input_image, image) into an Image.
func ImageFromPart(mapItem map[string]any) (Image, bool) {
	iType, _ := mapItem["type"].(string)

	switch iType {
	case "image_url":
		if urlObj, ok := mapItem["image_url"].(map[string]any); ok {
			if url, ok := urlObj["url"].(string); ok && url != "" {
				if strings.HasPrefix(url, "data:") {
					data, mime, err := DecodeDataURL(url)
					if err != nil {
						return Image{}, false
					}
					return Image{Data: data, MIME: mime}, true
				}
				return ImageFromURL(url), true
			}
		}
	case "input_image":
		// OpenAI input_image: {"type":"input_image","data":"<base64>","mime":"..."} or {"type":"input_image","url":"..."}
		if url, ok := mapItem["url"].(string); ok && url != "" {
			if strings.HasPrefix(url, "data:") {
				data, mime, err := DecodeDataURL(url)
				if err != nil {
					return Image{}, false
				}
				return Image{Data: data, MIME: mime}, true
			}
			return ImageFromURL(url), true
		}
		if data, ok := mapItem["data"].(string); ok && data != "" {
			mime, _ := mapItem["mime"].(string)
			decoded, err := multimodal.DecodeBase64Raw(data)
			if err != nil {
				return Image{}, false
			}
			if mime == "" {
				mime = multimodal.DetectImageMime(decoded)
			}
			return Image{Data: decoded, MIME: mime}, true
		}
	case "image":
		// Google-style inline_data embedded in OpenAI content
		if url, ok := mapItem["url"].(string); ok && url != "" {
			return ImageFromURL(url), true
		}
		if data, ok := mapItem["data"].(string); ok && data != "" {
			mime, _ := mapItem["mime"].(string)
			decoded, err := multimodal.DecodeBase64Raw(data)
			if err != nil {
				return Image{}, false
			}
			if mime == "" {
				mime = multimodal.DetectImageMime(decoded)
			}
			return Image{Data: decoded, MIME: mime}, true
		}
	}

	return Image{}, false
}

// BuildToolChoiceInstruction returns a prompt suffix based on the tool_choice parameter.
func BuildToolChoiceInstruction(toolChoice any) string {
	if strChoice, ok := toolChoice.(string); ok {
		if strChoice == "none" {
			return "\n\nIMPORTANT: Do NOT call any tools. Respond with text only."
		}
		if strChoice == "required" {
			return "\n\nIMPORTANT: You MUST call at least one tool. Do not respond with text only."
		}
	} else if mapChoice, ok := toolChoice.(map[string]any); ok {
		if fn, ok := mapChoice["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				return fmt.Sprintf("\n\nIMPORTANT: You MUST call the tool \"%s\". Do not call other tools.", name)
			}
		}
	}
	return ""
}

func MessagesToPrompt(req models.OpenAIChatRequest) (string, []Image, error) {
	var parts []string
	var images []Image

	strChoice, isStr := req.ToolChoice.(string)
	if !(isStr && strChoice == "none") && len(req.Tools) > 0 {
		var toolDefs []models.OpenAIFunction
		for _, tool := range req.Tools {
			fn := tool.Function
			if fn.Name == "" {
				fn.Name = tool.Name
			}
			if fn.Description == "" {
				fn.Description = tool.Description
			}
			if fn.Parameters == nil {
				fn.Parameters = tool.Parameters
			}
			if fn.Parameters == nil {
				fn.Parameters = map[string]any{}
			}

			toolDefs = append(toolDefs, fn)
		}

		if len(toolDefs) > 0 {
			constraint := BuildToolChoiceInstruction(req.ToolChoice)
			defsJSON, _ := json.Marshal(toolDefs)
			// Tool slimming: if tool definitions exceed 30KB, re-marshal with name+description only.
			if len(defsJSON) > 30000 {
				log.Printf("Tool defs too large (%d bytes), slimming to name+description only", len(defsJSON))
				slimmed := make([]models.OpenAIFunction, len(toolDefs))
				for i, fn := range toolDefs {
					slimmed[i] = models.OpenAIFunction{
						Name:        fn.Name,
						Description: fn.Description,
					}
				}
				defsJSON, _ = json.Marshal(slimmed)
			}
			parts = append(parts, fmt.Sprintf(
				"# Tool Use\n\n"+
					"You can call the following tools. Call format:\n"+
					"```tool_call\n{\"name\": \"func_name\", \"arguments\": {...}}\n```\n"+
					"When calling tools, output ONLY the tool_call block(s).\n\n"+
					"Available tools:\n%s%s",
				string(defsJSON),
				constraint,
			))
		}
	}

	for _, msg := range req.Messages {
		role := msg.Role
		if role == "" {
			role = "user"
		}

		var contentStr string
		if strContent, ok := msg.Content.(string); ok {
			contentStr = strContent
		} else if contentList, ok := msg.Content.([]any); ok {
			var textParts []string
			for _, item := range contentList {
				if mapItem, ok := item.(map[string]any); ok {
					iType, _ := mapItem["type"].(string)
					if iType == "text" || iType == "input_text" {
						if t, ok := mapItem["text"].(string); ok {
							textParts = append(textParts, t)
						}
					} else if iType == "image_url" || iType == "input_image" || iType == "image" {
						if img, ok := ImageFromPart(mapItem); ok {
							images = append(images, img)
							textParts = append(textParts, "[Image attached]")
						}
					}
				}
			}
			contentStr = strings.Join(textParts, " ")
		}

		switch role {
		case "system":
			parts = append(parts, fmt.Sprintf("[System instruction]: %s", contentStr))
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				var tcStrs []string
				for _, tc := range msg.ToolCalls {
					argsStr := tc.Function.Arguments
					if argsStr == "" {
						argsStr = "{}"
					}
					tcStrs = append(tcStrs, fmt.Sprintf("```tool_call\n{\"name\": \"%s\", \"arguments\": %s}\n```", tc.Function.Name, argsStr))
				}
				parts = append(parts, fmt.Sprintf("[Assistant]: %s\n%s", contentStr, strings.Join(tcStrs, "\n")))
			} else {
				parts = append(parts, fmt.Sprintf("[Assistant]: %s", contentStr))
			}
		case "tool":
			parts = append(parts, fmt.Sprintf("[Tool result for %s]: %s", msg.Name, contentStr))
		default:
			if contentStr != "" {
				parts = append(parts, contentStr)
			}
		}
	}

	return strings.Join(parts, "\n\n"), images, nil
}

var reToolCall = regexp.MustCompile(`(?s)\x60\x60\x60tool_call\s*\n(.*?)\n\x60\x60\x60`)

// ParseToolCalls extracts tool calls from the raw string output of the model.
// The model is instructed to output tool calls in markdown blocks e.g. ```tool_call\n{...}\n```.
// This function parses those blocks, removes them from the text, and returns the cleaned text along with structured ToolCalls.
func ParseToolCalls(text string) (string, []models.OpenAIToolCall) {
	var toolCalls []models.OpenAIToolCall
	matches := reToolCall.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	var cleanParts []string
	lastEnd := 0

	for _, m := range matches {
		cleanParts = append(cleanParts, text[lastEnd:m[0]])
		lastEnd = m[1]

		content := strings.TrimSpace(text[m[2]:m[3]])
		var data struct {
			Name      string `json:"name"`
			Arguments any    `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(content), &data); err == nil && data.Name != "" {
			var argsStr string
			if str, ok := data.Arguments.(string); ok {
				argsStr = str
			} else if data.Arguments != nil {
				b, _ := json.Marshal(data.Arguments)
				argsStr = string(b)
			} else {
				argsStr = "{}"
			}

			toolCalls = append(toolCalls, models.OpenAIToolCall{
				ID:   fmt.Sprintf("call_%s", RandHex(8)),
				Type: "function",
				Function: models.OpenAIToolCallFunction{
					Name:      data.Name,
					Arguments: argsStr,
				},
			})
		}
	}

	cleanParts = append(cleanParts, text[lastEnd:])
	clean := strings.TrimSpace(strings.Join(cleanParts, ""))
	return clean, toolCalls
}
