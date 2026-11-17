package format

import (
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func TestParseToolCalls(t *testing.T) {
	input := "Here is a call:\n```tool_call\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Jakarta\"}}\n```\nDone."
	clean, calls := ParseToolCalls(input)

	if len(calls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(calls))
	}

	if calls[0].Function.Name != "get_weather" {
		t.Errorf("Expected name get_weather, got %s", calls[0].Function.Name)
	}

	if !strings.Contains(clean, "Here is a call:") || !strings.Contains(clean, "Done.") {
		t.Errorf("Unexpected clean text: %q", clean)
	}
}

func TestMessagesToPrompt(t *testing.T) {
	req := models.OpenAIChatRequest{
		Messages: []models.OpenAIMessage{
			{Role: "system", Content: "Be helpful."},
			{Role: "user", Content: "Hello!"},
		},
		ToolChoice: "auto",
	}

	prompt, images, err := MessagesToPrompt(req)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(images) != 0 {
		t.Errorf("Expected 0 images, got %d", len(images))
	}

	if !strings.Contains(prompt, "[System instruction]: Be helpful.") {
		t.Errorf("Prompt missing system instruction: %q", prompt)
	}
	if !strings.Contains(prompt, "Hello!") {
		t.Errorf("Prompt missing user message: %q", prompt)
	}
}

func TestDecodeDataURL(t *testing.T) {
	// Valid data URL
	b64 := "SGVsbG8="
	dataURL := "data:text/plain;base64," + b64
	data, mime, err := DecodeDataURL(dataURL)
	if err != nil {
		t.Fatalf("DecodeDataURL: unexpected error: %v", err)
	}
	if string(data) != "Hello" {
		t.Errorf("DecodeDataURL data = %q, want %q", string(data), "Hello")
	}
	if mime != "text/plain" {
		t.Errorf("DecodeDataURL mime = %q, want %q", mime, "text/plain")
	}

	// Invalid: not a data URL
	_, _, err = DecodeDataURL("https://example.com/img.png")
	if err == nil {
		t.Error("DecodeDataURL should fail for non-data URL")
	}

	// Non-base64 data URL: percent-decoded per upstream parity
	data, mime, err = DecodeDataURL("data:text/plain,hello%20world")
	if err != nil {
		t.Fatalf("DecodeDataURL non-base64: unexpected error: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("DecodeDataURL non-base64 data = %q, want %q", string(data), "hello world")
	}
	if mime != "text/plain" {
		t.Errorf("DecodeDataURL non-base64 mime = %q, want %q", mime, "text/plain")
	}
}

func TestImageFromURL(t *testing.T) {
	img := ImageFromURL("https://example.com/photo.jpg")
	if img.URL != "https://example.com/photo.jpg" {
		t.Errorf("ImageFromURL URL = %q, want %q", img.URL, "https://example.com/photo.jpg")
	}
	if img.MIME != "image/jpeg" {
		t.Errorf("ImageFromURL MIME = %q, want %q", img.MIME, "image/jpeg")
	}

	img2 := ImageFromURL("https://example.com/pic.png")
	if img2.MIME != "image/png" {
		t.Errorf("ImageFromURL PNG MIME = %q, want %q", img2.MIME, "image/png")
	}
}

func TestImageFromPartDataURL(t *testing.T) {
	part := map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:image/png;base64," + "iVBORw0KGgo=",
		},
	}
	img, ok := ImageFromPart(part)
	if !ok {
		t.Fatal("ImageFromPart should succeed for data URL")
	}
	if img.MIME != "image/png" {
		t.Errorf("ImageFromPart MIME = %q, want %q", img.MIME, "image/png")
	}
	if len(img.Data) == 0 {
		t.Error("ImageFromPart should have data")
	}
}

func TestImageFromPartRemoteURL(t *testing.T) {
	part := map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "https://example.com/photo.jpg",
		},
	}
	img, ok := ImageFromPart(part)
	if !ok {
		t.Fatal("ImageFromPart should succeed for remote URL")
	}
	if img.URL != "https://example.com/photo.jpg" {
		t.Errorf("ImageFromPart URL = %q, want %q", img.URL, "https://example.com/photo.jpg")
	}
}

func TestImageFromPartInputImage(t *testing.T) {
	part := map[string]any{
		"type": "input_image",
		"data": "SGVsbG8=",
		"mime": "image/jpeg",
	}
	img, ok := ImageFromPart(part)
	if !ok {
		t.Fatal("ImageFromPart should succeed for input_image")
	}
	if img.MIME != "image/jpeg" {
		t.Errorf("ImageFromPart MIME = %q, want %q", img.MIME, "image/jpeg")
	}
	if string(img.Data) != "Hello" {
		t.Errorf("ImageFromPart data = %q, want %q", string(img.Data), "Hello")
	}
}

func TestImageFromPartStringForm(t *testing.T) {
	// Upstream parity: image_url may be a plain string, not just {url:...}.
	part := map[string]any{
		"type":      "image_url",
		"image_url": "https://example.com/photo.png",
	}
	img, ok := ImageFromPart(part)
	if !ok {
		t.Fatal("ImageFromPart should succeed for string image_url")
	}
	if img.URL != "https://example.com/photo.png" {
		t.Errorf("ImageFromPart URL = %q, want %q", img.URL, "https://example.com/photo.png")
	}
}

func TestImageFromPartMimeTypeKey(t *testing.T) {
	// Upstream parity: mime_type / media_type keys.
	part := map[string]any{
		"type":      "input_image",
		"data":      "SGVsbG8=",
		"mime_type": "image/jpeg",
	}
	img, ok := ImageFromPart(part)
	if !ok {
		t.Fatal("ImageFromPart should succeed for mime_type key")
	}
	if img.MIME != "image/jpeg" {
		t.Errorf("ImageFromPart MIME = %q, want %q", img.MIME, "image/jpeg")
	}
}

func TestResponsesInputPreservesImages(t *testing.T) {
	// Fatal fix: image parts must survive as structured content,
	// not flattened to a placeholder string.
	input := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "see this"},
				map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "https://example.com/a.png"},
				},
			},
		},
	}
	messages, err := ResponsesInputToMessages(input, "")
	if err != nil {
		t.Fatalf("ResponsesInputToMessages error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}
	content, ok := messages[0]["content"].([]any)
	if !ok {
		t.Fatalf("content should stay []any, got %T", messages[0]["content"])
	}
	chatReq := models.OpenAIChatRequest{
		Messages:   []models.OpenAIMessage{{Role: "user", Content: content}},
		ToolChoice: "auto",
	}
	prompt, images, err := MessagesToPrompt(chatReq)
	if err != nil {
		t.Fatalf("MessagesToPrompt error: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("Expected 1 extracted image, got %d", len(images))
	}
	if !strings.Contains(prompt, "[Image attached]") {
		t.Errorf("Prompt should contain [Image attached], got %q", prompt)
	}
}

func TestImageFromPartUnsupported(t *testing.T) {
	part := map[string]any{
		"type": "text",
		"text": "hello",
	}
	_, ok := ImageFromPart(part)
	if ok {
		t.Error("ImageFromPart should fail for text type")
	}
}

func TestMessagesToPromptWithImages(t *testing.T) {
	req := models.OpenAIChatRequest{
		Messages: []models.OpenAIMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Look at this image"},
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "data:image/png;base64," + "iVBORw0KGgo=",
						},
					},
				},
			},
		},
		ToolChoice: "auto",
	}

	prompt, images, err := MessagesToPrompt(req)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("Expected 1 image, got %d", len(images))
	}
	if !strings.Contains(prompt, "[Image attached]") {
		t.Errorf("Prompt should contain [Image attached], got %q", prompt)
	}
	if !strings.Contains(prompt, "Look at this image") {
		t.Errorf("Prompt should contain text, got %q", prompt)
	}
}

func TestToolSlimming(t *testing.T) {
	// Create enough tools to exceed 30KB threshold
	var tools []models.OpenAITool
	for i := 0; i < 200; i++ {
		tools = append(tools, models.OpenAITool{
			Function: models.OpenAIFunction{
				Name:        "tool_" + strings.Repeat("x", 100),
				Description: strings.Repeat("description with <html> chars ", 50),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"param1": map[string]any{"type": "string", "description": strings.Repeat("param desc ", 20)},
					},
				},
			},
		})
	}

	req := models.OpenAIChatRequest{
		Messages: []models.OpenAIMessage{
			{Role: "user", Content: "test"},
		},
		Tools:      tools,
		ToolChoice: "auto",
	}

	prompt, _, err := MessagesToPrompt(req)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// The prompt should still contain the tool definitions section (slimmed)
	// Verify slimming happened: the log message "Tool defs too large ... slimming"
	// confirms the threshold was hit. With 200 large tools, even slimmed output is substantial.
	// The key verification is that the function doesn't error and produces a valid prompt.
	if !strings.Contains(prompt, "# Tool Use") {
		t.Errorf("Prompt should contain tool section, got length %d", len(prompt))
	}
}

func TestResponsesInputToMessagesMultiPartText(t *testing.T) {
	input := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "Hello"},
				map[string]any{"type": "text", "text": "world"},
			},
		},
	}

	messages, err := ResponsesInputToMessages(input, "System instructions")
	if err != nil {
		t.Fatalf("ResponsesInputToMessages error: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages (system + user), got %d", len(messages))
	}

	userContent, _ := messages[1]["content"].(string)
	expected := "Hello world"
	if userContent != expected {
		t.Errorf("Got content %q, want %q", userContent, expected)
	}
}

func TestFileFromPartOpenAIAndDocument(t *testing.T) {
	pdfB64 := "JVBERi0xLjQK" // "%PDF-1.4\n"

	req := models.OpenAIChatRequest{
		Messages: []models.OpenAIMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Summarize these PDFs"},
					map[string]any{
						"type": "file",
						"file": map[string]any{
							"filename":  "quarterly_report.pdf",
							"file_data": "data:application/pdf;base64," + pdfB64,
						},
					},
					map[string]any{
						"type":      "input_file",
						"filename":  "notes.pdf",
						"file_data": pdfB64,
					},
					map[string]any{
						"type":     "document",
						"filename": "memo.pdf",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "application/pdf",
							"data":       pdfB64,
						},
					},
				},
			},
		},
		ToolChoice: "auto",
	}

	prompt, atts, err := MessagesToPrompt(req)
	if err != nil {
		t.Fatalf("MessagesToPrompt error: %v", err)
	}
	if len(atts) != 3 {
		t.Fatalf("expected 3 file attachments, got %d", len(atts))
	}
	for i, wantName := range []string{"quarterly_report.pdf", "notes.pdf", "memo.pdf"} {
		if atts[i].Filename != wantName {
			t.Errorf("att[%d].Filename = %q, want %q", i, atts[i].Filename, wantName)
		}
		if atts[i].MIME != "application/pdf" {
			t.Errorf("att[%d].MIME = %q, want application/pdf", i, atts[i].MIME)
		}
		if string(atts[i].Data) != "%PDF-1.4\n" {
			t.Errorf("att[%d].Data = %q, want %%PDF-1.4\\n", i, string(atts[i].Data))
		}
	}
	if !strings.Contains(prompt, "[File attached: quarterly_report.pdf]") {
		t.Errorf("prompt missing file marker, got %q", prompt)
	}
}

func TestResponsesInputPreservesInputFile(t *testing.T) {
	input := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "Read this PDF"},
				map[string]any{
					"type":      "input_file",
					"filename":  "brief.pdf",
					"file_data": "data:application/pdf;base64,JVBERi0xLjQK",
				},
			},
		},
	}

	messages, err := ResponsesInputToMessages(input, "")
	if err != nil {
		t.Fatalf("ResponsesInputToMessages error: %v", err)
	}
	content, ok := messages[0]["content"].([]any)
	if !ok {
		t.Fatalf("expected structured []any content for input_file, got %T", messages[0]["content"])
	}
	_, atts, err := MessagesToPrompt(models.OpenAIChatRequest{
		Messages:   []models.OpenAIMessage{{Role: "user", Content: content}},
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatalf("MessagesToPrompt error: %v", err)
	}
	if len(atts) != 1 || atts[0].Filename != "brief.pdf" {
		t.Fatalf("expected brief.pdf attachment, got %+v", atts)
	}
}

func TestMessagesToDeltaPromptSkipsPriorAssistant(t *testing.T) {
	delta := []models.OpenAIMessage{
		{
			Role: "assistant",
			ToolCalls: []models.OpenAIToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: models.OpenAIToolCallFunction{
						Name:      "read_file",
						Arguments: `{"path":"a.txt"}`,
					},
				},
			},
		},
		{
			Role:    "tool",
			Name:    "read_file",
			Content: "file contents here",
		},
	}

	prompt, _ := MessagesToDeltaPrompt(delta, "auto")
	if strings.Contains(prompt, "[Assistant]:") {
		t.Errorf("delta prompt should skip leading assistant echo, got %q", prompt)
	}
	if !strings.Contains(prompt, "[Tool result for read_file]: file contents here") {
		t.Errorf("delta prompt missing tool result, got %q", prompt)
	}
}

func TestCompactToolDefinitionsPreservesParamsAndBridgeProtocol(t *testing.T) {
	// 25 verbose WorkBuddy-like tools (> 15KB raw JSON) must compact cleanly while
	// keeping every tool's parameter names & required markers AND explaining the local bridge.
	var tools []models.OpenAITool
	for i := 0; i < 25; i++ {
		suffix := string(rune('a' + i))
		tools = append(tools, models.OpenAITool{
			Type: "function",
			Function: models.OpenAIFunction{
				Name:        "local_tool_" + suffix,
				Description: "Execute local workspace operation " + strings.Repeat("with detailed guidance ", 15),
				Parameters: map[string]any{
					"type":     "object",
					"required": []any{"target_path"},
					"properties": map[string]any{
						"target_path": map[string]any{"type": "string", "description": strings.Repeat("Absolute path on disk ", 8)},
						"dry_run":     map[string]any{"type": "boolean", "description": "Preview changes before deleting"},
					},
				},
			},
		})
	}

	req := models.OpenAIChatRequest{
		Tools:      tools,
		ToolChoice: "auto",
		Messages:   []models.OpenAIMessage{{Role: "user", Content: "Organize my files"}},
	}
	prompt, _, err := MessagesToPrompt(req)
	if err != nil {
		t.Fatalf("MessagesToPrompt error: %v", err)
	}
	if len(prompt) > 12000 {
		t.Errorf("expected compacted tool prompt well under 12KB, got %d bytes", len(prompt))
	}
	if !strings.Contains(prompt, "local client bridge") || !strings.Contains(prompt, "NEVER refuse") {
		t.Errorf("expected explicit local client bridge anti-refusal guidance in # Tool Use")
	}
	if !strings.Contains(prompt, `"target_path":"string*`) {
		t.Errorf("expected compacted tool defs to preserve required parameter marker target_path:string*, got snippet: %q", prompt[:min(1200, len(prompt))])
	}
}

func TestParseToolCallsMultiFormatAndJSONSafety(t *testing.T) {
	allowed := AllowedToolNames([]models.OpenAITool{
		{Type: "function", Function: models.OpenAIFunction{Name: "list_dir"}},
		{Type: "function", Function: models.OpenAIFunction{Name: "delete_file"}},
	})

	// 1. JSON array inside ```tool_call```
	arrayOut := "```tool_call\n[{\"name\":\"list_dir\",\"arguments\":{\"path\":\".\"}},{\"name\":\"delete_file\",\"arguments\":{\"path\":\"old.json\"}}]\n```"
	_, calls := ParseToolCallsWithAllowed(arrayOut, allowed)
	if len(calls) != 2 || calls[0].Function.Name != "list_dir" || calls[1].Function.Name != "delete_file" {
		t.Fatalf("expected 2 array tool calls, got %+v", calls)
	}

	// 2. <tool_call> XML wrapper
	xmlOut := "Let me check.\n<tool_call>\n{\"name\":\"list_dir\",\"arguments\":{\"path\":\"/tmp\"}}\n</tool_call>"
	cleanXML, xmlCalls := ParseToolCallsWithAllowed(xmlOut, allowed)
	if len(xmlCalls) != 1 || xmlCalls[0].Function.Name != "list_dir" {
		t.Fatalf("expected 1 XML tool call, got %+v", xmlCalls)
	}
	if strings.Contains(cleanXML, "<tool_call>") {
		t.Errorf("clean text should strip <tool_call>, got %q", cleanXML)
	}

	// 3. ```json``` matching a declared tool name
	jsonTool := "```json\n{\"name\":\"list_dir\",\"arguments\":{\"path\":\".\"}}\n```"
	_, jsonCalls := ParseToolCallsWithAllowed(jsonTool, allowed)
	if len(jsonCalls) != 1 || jsonCalls[0].Function.Name != "list_dir" {
		t.Fatalf("expected declared ```json``` tool call to parse, got %+v", jsonCalls)
	}

	// 4. Ordinary ```json``` data snippet (not a declared tool) MUST NOT be treated as a tool call
	normalJSON := "Here is your config:\n```json\n{\"name\":\"my-app\",\"version\":\"1.0.0\"}\n```"
	cleanNormal, normalCalls := ParseToolCallsWithAllowed(normalJSON, allowed)
	if len(normalCalls) != 0 {
		t.Fatalf("ordinary JSON snippet must NOT parse as tool call, got %+v", normalCalls)
	}
	if cleanNormal != normalJSON {
		t.Errorf("ordinary JSON snippet should remain untouched, got %q", cleanNormal)
	}
}

