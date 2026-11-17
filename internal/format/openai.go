package format

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

var reDataURL = regexp.MustCompile(`(?s)^data:([^;,]+)?(;base64)?,(.*)$`)

func RandHex(n int) string {
	bytes := make([]byte, (n+1)/2)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)[:n]
}

// DecodeDataURL decodes a `data:<mime>[;base64],<payload>` URL into bytes and MIME.
// Mirrors upstream _decode_data_url: base64 payloads are strictly decoded,
// non-base64 payloads are percent-decoded.
func DecodeDataURL(dataURL string) ([]byte, string, error) {
	m := reDataURL.FindStringSubmatch(dataURL)
	if m == nil {
		return nil, "", fmt.Errorf("not a data URL")
	}
	mime := m[1]
	if mime == "" {
		mime = "image/png"
	}
	payload := m[3]
	if m[2] != "" {
		decoded, err := multimodal.DecodeBase64Raw(payload)
		if err != nil {
			return nil, "", fmt.Errorf("base64 decode failed: %w", err)
		}
		if m[1] == "" {
			mime = multimodal.DetectImageMime(decoded)
		}
		return decoded, mime, nil
	}
	decodedStr, err := url.PathUnescape(payload)
	if err != nil {
		return nil, "", fmt.Errorf("data URL percent-decode failed: %w", err)
	}
	decoded := []byte(decodedStr)
	if m[1] == "" {
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

// imageFromURLString resolves a URL string (remote or data:) into an Image.
func imageFromURLString(rawURL, mimeHint string) (Image, bool) {
	if rawURL == "" {
		return Image{}, false
	}
	if strings.HasPrefix(rawURL, "data:") {
		data, mime, err := DecodeDataURL(rawURL)
		if err != nil {
			return Image{}, false
		}
		return Image{Data: data, MIME: mime}, true
	}
	img := ImageFromURL(rawURL)
	if mimeHint != "" {
		img.MIME = mimeHint
	}
	return img, true
}

func partMime(m map[string]any) string {
	for _, k := range []string{"mime_type", "mime", "media_type"} {
		if s, _ := m[k].(string); s != "" {
			return s
		}
	}
	return ""
}

// ImageFromPart parses an OpenAI-style content part (image_url, input_image, image) into an Image.
// Mirrors upstream _image_from_part, including image_url as a plain string.
func ImageFromPart(mapItem map[string]any) (Image, bool) {
	iType, _ := mapItem["type"].(string)

	switch iType {
	case "image_url":
		raw := mapItem["image_url"]
		if urlObj, ok := raw.(map[string]any); ok {
			urlStr, _ := urlObj["url"].(string)
			mimeHint := partMime(urlObj)
			return imageFromURLString(urlStr, mimeHint)
		}
		if urlStr, ok := raw.(string); ok {
			return imageFromURLString(urlStr, "")
		}
	case "input_image", "image":
		var urlRaw any
		if u, ok := mapItem["image_url"]; ok {
			urlRaw = u
		} else if u, ok := mapItem["url"]; ok {
			urlRaw = u
		}
		if urlObj, ok := urlRaw.(map[string]any); ok {
			urlStr, _ := urlObj["url"].(string)
			mimeHint := partMime(urlObj)
			if mimeHint == "" {
				mimeHint = partMime(mapItem)
			}
			if urlStr != "" {
				return imageFromURLString(urlStr, mimeHint)
			}
		} else if urlStr, ok := urlRaw.(string); ok && urlStr != "" {
			if mimeHint := partMime(mapItem); mimeHint != "" {
				return imageFromURLString(urlStr, mimeHint)
			}
			return imageFromURLString(urlStr, "")
		}
		var imageData string
		if s, ok := mapItem["data"].(string); ok && s != "" {
			imageData = s
		} else if s, ok := mapItem["base64"].(string); ok && s != "" {
			imageData = s
		}
		if imageData != "" {
			mime := partMime(mapItem)
			if strings.HasPrefix(imageData, "data:") {
				data, decMime, err := DecodeDataURL(imageData)
				if err != nil {
					return Image{}, false
				}
				if mime == "" {
					mime = decMime
				}
				return Image{Data: data, MIME: mime}, true
			}
			decoded, err := multimodal.DecodeBase64Raw(imageData)
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

// FileFromPart parses OpenAI-style file/document content parts (`file`, `input_file`, `document`)
// into an Image/Attachment struct so PDFs and other documents can be uploaded directly via the bridge.
func FileFromPart(mapItem map[string]any) (Image, bool) {
	iType, _ := mapItem["type"].(string)

	switch iType {
	case "file":
		fileObj, ok := mapItem["file"].(map[string]any)
		if !ok {
			fileObj = mapItem
		}
		filename, _ := fileObj["filename"].(string)
		mimeHint := partMime(fileObj)
		if mimeHint == "" {
			mimeHint = partMime(mapItem)
		}
		if fileData, ok := fileObj["file_data"].(string); ok && fileData != "" {
			return decodeFilePayload(fileData, filename, mimeHint)
		}
		if fileURL, ok := fileObj["file_url"].(string); ok && fileURL != "" {
			img, ok := imageFromURLString(fileURL, mimeHint)
			if ok {
				img.Filename = filename
				return img, true
			}
		}
	case "input_file":
		filename, _ := mapItem["filename"].(string)
		mimeHint := partMime(mapItem)
		if fileData, ok := mapItem["file_data"].(string); ok && fileData != "" {
			return decodeFilePayload(fileData, filename, mimeHint)
		}
		if fileURL, ok := mapItem["file_url"].(string); ok && fileURL != "" {
			img, ok := imageFromURLString(fileURL, mimeHint)
			if ok {
				img.Filename = filename
				return img, true
			}
		}
	case "document":
		filename, _ := mapItem["filename"].(string)
		if filename == "" {
			filename, _ = mapItem["title"].(string)
		}
		if source, ok := mapItem["source"].(map[string]any); ok {
			mimeHint := partMime(source)
			if dataStr, ok := source["data"].(string); ok && dataStr != "" {
				return decodeFilePayload(dataStr, filename, mimeHint)
			}
			if urlStr, ok := source["url"].(string); ok && urlStr != "" {
				img, ok := imageFromURLString(urlStr, mimeHint)
				if ok {
					img.Filename = filename
					return img, true
				}
			}
		}
	}

	return Image{}, false
}

func decodeFilePayload(payload, filename, mimeHint string) (Image, bool) {
	if strings.HasPrefix(payload, "data:") {
		data, decMime, err := DecodeDataURL(payload)
		if err != nil {
			return Image{}, false
		}
		if mimeHint == "" {
			mimeHint = decMime
		}
		return Image{Data: data, MIME: mimeHint, Filename: filename}, true
	}
	decoded, err := multimodal.DecodeBase64Raw(payload)
	if err != nil {
		return Image{}, false
	}
	if mimeHint == "" {
		if len(decoded) >= 4 && string(decoded[:4]) == "%PDF" {
			mimeHint = "application/pdf"
		} else {
			mimeHint = "application/octet-stream"
		}
	}
	return Image{Data: decoded, MIME: mimeHint, Filename: filename}, true
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

// ExtractMessageContent extracts text and multimodal attachments (images + files) from one OpenAI message.
func ExtractMessageContent(msg models.OpenAIMessage) (string, []Image) {
	var attachments []Image

	if strContent, ok := msg.Content.(string); ok {
		return strContent, nil
	}

	contentList, ok := msg.Content.([]any)
	if !ok {
		return "", nil
	}

	var textParts []string
	for _, item := range contentList {
		if mapItem, ok := item.(map[string]any); ok {
			iType, _ := mapItem["type"].(string)
			if iType == "text" || iType == "input_text" || iType == "output_text" {
				if t, ok := mapItem["text"].(string); ok {
					textParts = append(textParts, t)
				}
			} else if img, ok := ImageFromPart(mapItem); ok {
				attachments = append(attachments, img)
				textParts = append(textParts, "[Image attached]")
			} else if fileAtt, ok := FileFromPart(mapItem); ok {
				attachments = append(attachments, fileAtt)
				if fileAtt.Filename != "" {
					textParts = append(textParts, fmt.Sprintf("[File attached: %s]", fileAtt.Filename))
				} else {
					textParts = append(textParts, "[File attached]")
				}
			}
		} else if s, ok := item.(string); ok && s != "" {
			textParts = append(textParts, s)
		}
	}

	return strings.Join(textParts, " "), attachments
}

func formatMessagePart(msg models.OpenAIMessage, contentStr string) string {
	role := msg.Role
	if role == "" {
		role = "user"
	}

	switch role {
	case "system", "developer":
		return fmt.Sprintf("[System instruction]: %s", contentStr)
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
			return fmt.Sprintf("[Assistant]: %s\n%s", contentStr, strings.Join(tcStrs, "\n"))
		}
		return fmt.Sprintf("[Assistant]: %s", contentStr)
	case "tool":
		return fmt.Sprintf("[Tool result for %s]: %s", msg.Name, contentStr)
	default:
		return contentStr
	}
}

func MessagesToPrompt(req models.OpenAIChatRequest) (string, []Image, error) {
	var parts []string
	var images []Image

	strChoice, isStr := req.ToolChoice.(string)
	if (!isStr || strChoice != "none") && len(req.Tools) > 0 {
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
					"When calling tools:\n"+
					"- Output ONLY the tool_call block(s).\n"+
					"- Batch all independent reads, searches, or edits as multiple tool_call blocks in a SINGLE response to minimize round-trips.\n"+
					"- For long code files: read directly attached files natively, or use search tools (grep) to jump to target lines instead of paginating chunk-by-chunk; prefer targeted edits over full-file rewrites, and avoid re-reading an entire file after an edit succeeds.\n"+
					"- If a PDF, document, image, or source code file is already attached directly to this message, read the attached file directly instead of calling local tools to re-read or extract it.\n\n"+
					"Available tools:\n%s%s",
				string(defsJSON),
				constraint,
			))
		}
	}

	for _, msg := range req.Messages {
		contentStr, msgAtts := ExtractMessageContent(msg)
		if len(msgAtts) > 0 {
			images = append(images, msgAtts...)
		}
		if formatted := formatMessagePart(msg, contentStr); formatted != "" {
			parts = append(parts, formatted)
		}
	}

	return strings.Join(parts, "\n\n"), images, nil
}

// MessagesToDeltaPrompt formats only newly appended messages for an ongoing browser-bridge
// conversation where the open Gemini tab already holds the earlier turns and tool definitions.
func MessagesToDeltaPrompt(deltaMessages []models.OpenAIMessage, toolChoice any) (string, []Image) {
	if len(deltaMessages) == 0 {
		return "", nil
	}

	// If the first delta message is the assistant turn Gemini itself just generated,
	// skip it when followed by new tool/user messages so we do not echo Gemini's own reply back.
	startIdx := 0
	if deltaMessages[0].Role == "assistant" && len(deltaMessages) > 1 {
		startIdx = 1
	}

	var parts []string
	var attachments []Image
	for _, msg := range deltaMessages[startIdx:] {
		contentStr, msgAtts := ExtractMessageContent(msg)
		if len(msgAtts) > 0 {
			attachments = append(attachments, msgAtts...)
		}
		if formatted := formatMessagePart(msg, contentStr); formatted != "" {
			parts = append(parts, formatted)
		}
	}

	prompt := strings.Join(parts, "\n\n")
	if constraint := strings.TrimSpace(BuildToolChoiceInstruction(toolChoice)); constraint != "" && prompt != "" {
		prompt += "\n\n" + constraint
	}
	return prompt, attachments
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
