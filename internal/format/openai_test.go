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
