package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

type preparedAnthropicRequest struct {
	chatReq  models.OpenAIChatRequest
	resolved models.Resolved
	prompt   string
	images   []format.Image
}

func writeAnthropicError(w http.ResponseWriter, status int, errType, msg string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": msg,
		},
	})
}

func (a *App) prepareAnthropicRequest(r *http.Request) (*preparedAnthropicRequest, int, string, string) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil || len(bodyBytes) == 0 {
		return nil, http.StatusBadRequest, "invalid_request_error", "invalid JSON"
	}

	var raw map[string]any
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		return nil, http.StatusBadRequest, "invalid_request_error", "invalid JSON"
	}

	chatReq, err := format.AnthropicToChatRequest(raw)
	if err != nil {
		return nil, http.StatusBadRequest, "invalid_request_error", err.Error()
	}

	modelStr := chatReq.Model
	if modelStr == "" {
		modelStr = a.Cfg.DefaultModel
	}
	resolved, err := a.resolveModel(modelStr)
	if err != nil {
		return nil, http.StatusBadRequest, "invalid_request_error", err.Error()
	}

	prompt, images, err := format.MessagesToPrompt(chatReq)
	if err != nil || strings.TrimSpace(prompt) == "" {
		return nil, http.StatusBadRequest, "invalid_request_error", "empty prompt"
	}

	return &preparedAnthropicRequest{
		chatReq:  chatReq,
		resolved: resolved,
		prompt:   prompt,
		images:   images,
	}, 0, "", ""
}

// handleCountTokens returns a character-based estimate (len(prompt)/4) for
// /v1/messages/count_tokens rather than running a full model tokenizer.
func (a *App) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	prep, status, errType, errMsg := a.prepareAnthropicRequest(r)
	if prep == nil {
		writeAnthropicError(w, status, errType, errMsg)
		return
	}
	setModelHeaders(w, prep.resolved)
	writeJSON(w, http.StatusOK, map[string]any{
		"input_tokens": len(prep.prompt) / 4,
	})
}

func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	prep, status, errType, errMsg := a.prepareAnthropicRequest(r)
	if prep == nil {
		writeAnthropicError(w, status, errType, errMsg)
		return
	}
	setModelHeaders(w, prep.resolved)

	msgID := fmt.Sprintf("msg_%s", format.RandHex(12))
	inputTokens := len(prep.prompt) / 4

	strChoice, isStr := prep.chatReq.ToolChoice.(string)
	isToolNone := isStr && strChoice == "none"

	// TODO(streaming-tools): Live token streaming is used only when no tools are
	// active (or tool_choice is "none"). When tools are enabled (as in Claude Code),
	// we buffer Generate so ```tool_call blocks can be parsed cleanly before replaying
	// Anthropic SSE content blocks.
	if prep.chatReq.Stream && (len(prep.chatReq.Tools) == 0 || isToolNone) {
		a.streamAnthropicLive(w, r, msgID, prep.resolved, prep.chatReq, inputTokens)
		return
	}

	text, plan, err := a.generateWithSession(w, r, prep.resolved, prep.chatReq)
	if err != nil {
		if isSessionValidationError(err) {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		} else {
			writeAnthropicError(w, http.StatusBadGateway, "api_error", fmt.Sprintf("upstream error: %v", err))
		}
		return
	}
	if plan != nil && plan.PromptTokens > 0 {
		inputTokens = plan.PromptTokens
	}

	var toolCalls []models.OpenAIToolCall
	if len(prep.chatReq.Tools) > 0 && text != "" && !isToolNone {
		text, toolCalls = format.ParseToolCalls(text)
	}

	contentBlocks, rawToolArgs := buildAnthropicContentBlocks(text, toolCalls)
	stopReason := "end_turn"
	if len(toolCalls) > 0 {
		stopReason = "tool_use"
	}
	outputTokens := estimateAnthropicOutputTokens(text, rawToolArgs)

	if prep.chatReq.Stream {
		streamAnthropicBlocks(w, msgID, prep.resolved.Name, contentBlocks, rawToolArgs, stopReason, inputTokens, outputTokens)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         prep.resolved.Name,
		"content":       contentBlocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	})
}

func buildAnthropicContentBlocks(text string, toolCalls []models.OpenAIToolCall) ([]map[string]any, []string) {
	var blocks []map[string]any
	var rawArgs []string

	if text != "" {
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": text,
		})
	}

	for _, tc := range toolCalls {
		argsStr := tc.Function.Arguments
		if argsStr == "" {
			argsStr = "{}"
		}
		inputObj := map[string]any{}
		if err := json.Unmarshal([]byte(argsStr), &inputObj); err != nil {
			inputObj = map[string]any{}
		}
		toolID := "toolu_" + format.RandHex(12)
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    toolID,
			"name":  tc.Function.Name,
			"input": inputObj,
		})
		rawArgs = append(rawArgs, argsStr)
	}

	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": "",
		})
	}
	return blocks, rawArgs
}

func estimateAnthropicOutputTokens(text string, rawArgs []string) int {
	totalChars := len(text)
	for _, a := range rawArgs {
		totalChars += len(a)
	}
	return totalChars / 4
}

func emitAnthropicMessageStart(w http.ResponseWriter, msgID, modelName string, inputTokens int) {
	_ = writeSSEEvent(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         modelName,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": 0,
			},
		},
	})
}

func emitAnthropicMessageEnd(w http.ResponseWriter, stopReason string, outputTokens int) {
	_ = writeSSEEvent(w, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": outputTokens,
		},
	})
	_ = writeSSEEvent(w, "message_stop", map[string]any{
		"type": "message_stop",
	})
}

func (a *App) streamAnthropicLive(w http.ResponseWriter, r *http.Request, msgID string, resolved models.Resolved, chatReq models.OpenAIChatRequest, inputTokens int) {
	var totalChars int
	_, started, emitErr := a.streamWithSession(
		w, r, resolved, chatReq,
		func(plan *SessionPlan) bool {
			if !startSSE(w) {
				return false
			}
			inTok := inputTokens
			if plan != nil && plan.PromptTokens > 0 {
				inTok = plan.PromptTokens
			}
			emitAnthropicMessageStart(w, msgID, resolved.Name, inTok)
			_ = writeSSEEvent(w, "content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			})
			return true
		},
		func(delta string) error {
			totalChars += len(delta)
			return writeSSEEvent(w, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": delta,
				},
			})
		},
	)

	if emitErr != nil {
		if !started {
			if isSessionValidationError(emitErr) {
				writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", emitErr.Error())
			} else {
				writeAnthropicError(w, http.StatusBadGateway, "api_error", fmt.Sprintf("upstream error: %v", emitErr))
			}
			return
		}
		a.Logf("Messages stream error: %v", emitErr)
		_ = writeSSEEvent(w, "error", map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": emitErr.Error(),
			},
		})
		return
	}

	_ = writeSSEEvent(w, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})
	emitAnthropicMessageEnd(w, "end_turn", totalChars/4)
}

func streamAnthropicBlocks(w http.ResponseWriter, msgID, modelName string, blocks []map[string]any, rawArgs []string, stopReason string, inputTokens, outputTokens int) {
	if !startSSE(w) {
		return
	}

	emitAnthropicMessageStart(w, msgID, modelName, inputTokens)

	argIdx := 0
	for idx, blk := range blocks {
		bType, _ := blk["type"].(string)
		switch bType {
		case "text":
			_ = writeSSEEvent(w, "content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": idx,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			})
			if txt, _ := blk["text"].(string); txt != "" {
				_ = writeSSEEvent(w, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": idx,
					"delta": map[string]any{
						"type": "text_delta",
						"text": txt,
					},
				})
			}
			_ = writeSSEEvent(w, "content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": idx,
			})
		case "tool_use":
			_ = writeSSEEvent(w, "content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": idx,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    blk["id"],
					"name":  blk["name"],
					"input": map[string]any{},
				},
			})
			partial := "{}"
			if argIdx < len(rawArgs) {
				partial = rawArgs[argIdx]
				argIdx++
			}
			_ = writeSSEEvent(w, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": partial,
				},
			})
			_ = writeSSEEvent(w, "content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": idx,
			})
		}
	}

	emitAnthropicMessageEnd(w, stopReason, outputTokens)
}
