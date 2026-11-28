package format

import (
	"strings"
	"testing"
)

func TestAnthropicToChatRequest(t *testing.T) {
	req := map[string]any{
		"model": "claude-sonnet-4-20250514",
		"system": []any{
			map[string]any{"type": "text", "text": "You are a coding assistant."},
			map[string]any{"type": "text", "text": "Be concise."},
		},
		"tools": []any{
			map[string]any{
				"name":        "Bash",
				"description": "Run shell commands",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
				},
			},
		},
		"tool_choice": map[string]any{"type": "tool", "name": "Bash"},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "Check git status",
			},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "Let me check."},
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_01",
						"name":  "Bash",
						"input": map[string]any{"command": "git status"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_01",
						"content":     "nothing to commit",
					},
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "image/png",
							"data":       "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
						},
					},
				},
			},
		},
	}

	chatReq, err := AnthropicToChatRequest(req)
	if err != nil {
		t.Fatalf("AnthropicToChatRequest error: %v", err)
	}

	prompt, images, err := MessagesToPrompt(chatReq)
	if err != nil {
		t.Fatalf("MessagesToPrompt error: %v", err)
	}

	for _, want := range []string{
		"[System instruction]: You are a coding assistant.\nBe concise.",
		`You MUST call the tool "Bash"`,
		`"name":"Bash"`,
		`{"command":"git status"}`,
		"[Tool result for Bash]: nothing to commit",
		"[Image attached]",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\nprompt:\n%s", want, prompt)
		}
	}

	if len(images) != 1 || images[0].MIME != "image/png" {
		t.Errorf("expected 1 image/png attachment, got %+v", images)
	}
}

func TestAnthropicToolChoiceVariants(t *testing.T) {
	if got := convertAnthropicToolChoice(map[string]any{"type": "any"}); got != "required" {
		t.Errorf("any -> %v, want required", got)
	}
	if got := convertAnthropicToolChoice(map[string]any{"type": "none"}); got != "none" {
		t.Errorf("none -> %v, want none", got)
	}
	if got := convertAnthropicToolChoice(nil); got != "auto" {
		t.Errorf("nil -> %v, want auto", got)
	}
}
