package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func (a *App) handleResponses(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil || len(bodyBytes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}

	var req map[string]any
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}

	modelStr, _ := req["model"].(string)
	if modelStr == "" {
		modelStr = a.Cfg.DefaultModel
	}

	resolved, err := models.Resolve(modelStr, a.Cfg.DefaultModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}

	inputRaw := req["input"]
	instructions, _ := req["instructions"].(string)

	messages, err := format.ResponsesInputToMessages(inputRaw, instructions)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid input structure"}})
		return
	}

	var reqMessages []models.OpenAIMessage
	for _, m := range messages {
		role, _ := m["role"].(string)
		content := m["content"]
		reqMessages = append(reqMessages, models.OpenAIMessage{Role: role, Content: content})
	}

	var reqTools []models.OpenAITool
	if toolsRaw, ok := req["tools"].([]any); ok {
		for _, t := range toolsRaw {
			if tm, ok := t.(map[string]any); ok {
				var fn models.OpenAIFunction
				if f, ok := tm["function"].(map[string]any); ok {
					fn.Name, _ = f["name"].(string)
					fn.Description, _ = f["description"].(string)
					fn.Parameters = f["parameters"]
				} else {
					fn.Name, _ = tm["name"].(string)
					fn.Description, _ = tm["description"].(string)
					fn.Parameters = tm["parameters"]
				}
				reqTools = append(reqTools, models.OpenAITool{Function: fn})
			}
		}
	}

	toolChoice := req["tool_choice"]
	if toolChoice == nil {
		toolChoice = "auto"
	}

	chatReq := models.OpenAIChatRequest{
		Messages:   reqMessages,
		Tools:      reqTools,
		ToolChoice: toolChoice,
	}

	prompt, images, err := format.MessagesToPrompt(chatReq)
	if err != nil || strings.TrimSpace(prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "empty input"}})
		return
	}

	// Upload images to Gemini storage.
	var fileRefs []string
	if len(images) > 0 {
		fileRefs, err = a.uploadImages(images)
		if err != nil {
			a.Logf("Image upload error: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("image upload failed: %v", err)}})
			return
		}
	}

	text, err := a.Gem.Generate(prompt, resolved.Mode, resolved.Think, fileRefs, resolved.Extra)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
		return
	}

	strChoice, isStr := toolChoice.(string)
	isToolNone := isStr && strChoice == "none"

	var toolCalls []models.OpenAIToolCall
	if len(reqTools) > 0 && text != "" && !isToolNone {
		text, toolCalls = format.ParseToolCalls(text)
	}

	rid := fmt.Sprintf("resp_%s", format.RandHex(16))
	mid := fmt.Sprintf("msg_%s", format.RandHex(12))

	outputItems := format.BuildResponseOutput(text, toolCalls, mid)

	stream, _ := req["stream"].(bool)
	promptTokens := len(prompt) / 4
	outputTokens := len(text) / 4

	if stream {
		if !startSSE(w) {
			return
		}

		seqNum := 0

		// Build initial empty response for response.created
		createdResp := map[string]any{
			"id":            rid,
			"object":        "response",
			"status":        "in_progress",
			"model":         resolved.Name,
			"output":        []any{},
			"sequence_number": seqNum,
		}
		_ = writeSSEEvent(w, "response.created", map[string]any{
			"type":     "response.created",
			"response": createdResp,
		})
		seqNum++

		// response.in_progress
		_ = writeSSEEvent(w, "response.in_progress", map[string]any{
			"type":     "response.in_progress",
			"response": createdResp,
		})

		for itemIdx, item := range outputItems {
			iType, _ := item["type"].(string)
			itemID, _ := item["id"].(string)

			if iType == "function_call" {
				// output_item.added (function_call)
				_ = writeSSEEvent(w, "response.output_item.added", map[string]any{
					"type":          "response.output_item.added",
					"output_index":  itemIdx,
					"item":          item,
					"sequence_number": seqNum,
				})
				seqNum++

				// function_call_arguments.delta
				args, _ := item["arguments"].(string)
				_ = writeSSEEvent(w, "response.function_call_arguments.delta", map[string]any{
					"type":      "response.function_call_arguments.delta",
					"item_id":   itemID,
					"call_id":   item["call_id"],
					"delta":     args,
					"sequence_number": seqNum,
				})
				seqNum++

				// function_call_arguments.done
				_ = writeSSEEvent(w, "response.function_call_arguments.done", map[string]any{
					"type":      "response.function_call_arguments.done",
					"item_id":   itemID,
					"call_id":   item["call_id"],
					"name":      item["name"],
					"arguments": args,
					"sequence_number": seqNum,
				})
				seqNum++

				// output_item.done (function_call)
				_ = writeSSEEvent(w, "response.output_item.done", map[string]any{
					"type":          "response.output_item.done",
					"output_index":  itemIdx,
					"item":          item,
					"sequence_number": seqNum,
				})
				seqNum++

			} else if iType == "message" {
				if content, ok := item["content"].([]map[string]any); ok {
					for ci, cp := range content {
						// content_part.added
						_ = writeSSEEvent(w, "response.content_part.added", map[string]any{
							"type":           "response.content_part.added",
							"output_index":   itemIdx,
							"content_index":  ci,
							"part":           cp,
							"sequence_number": seqNum,
						})
						seqNum++

						// output_text.delta
						text, _ := cp["text"].(string)
						_ = writeSSEEvent(w, "response.output_text.delta", map[string]any{
							"type":           "response.output_text.delta",
							"item_id":        itemID,
							"output_index":   itemIdx,
							"content_index":  ci,
							"delta":          text,
							"sequence_number": seqNum,
						})
						seqNum++

						// output_text.done
						_ = writeSSEEvent(w, "response.output_text.done", map[string]any{
							"type":           "response.output_text.done",
							"item_id":        itemID,
							"output_index":   itemIdx,
							"content_index":  ci,
							"text":           text,
							"sequence_number": seqNum,
						})
						seqNum++

						// content_part.done
						_ = writeSSEEvent(w, "response.content_part.done", map[string]any{
							"type":           "response.content_part.done",
							"output_index":   itemIdx,
							"content_index":  ci,
							"part":           cp,
							"sequence_number": seqNum,
						})
						seqNum++
					}
				}

				// output_item.done (message)
				_ = writeSSEEvent(w, "response.output_item.done", map[string]any{
					"type":          "response.output_item.done",
					"output_index":  itemIdx,
					"item":          item,
					"sequence_number": seqNum,
				})
				seqNum++
			}
		}

		// response.completed
		respObj := map[string]any{
			"id":               rid,
			"object":           "response",
			"status":           "completed",
			"model":            resolved.Name,
			"output":           outputItems,
			"sequence_number":  seqNum,
			"usage": map[string]any{
				"input_tokens":  promptTokens,
				"output_tokens": outputTokens,
				"total_tokens":  promptTokens + outputTokens,
			},
		}
		_ = writeSSEEvent(w, "response.completed", map[string]any{
			"type":     "response.completed",
			"response": respObj,
		})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{
			"id":         rid,
			"object":     "response",
			"created_at": time.Now().Unix(),
			"status":     "completed",
			"model":      resolved.Name,
			"output":     outputItems,
			"usage": map[string]any{
				"input_tokens":  promptTokens,
				"output_tokens": outputTokens,
				"total_tokens":  promptTokens + outputTokens,
			},
		})
	}
}
