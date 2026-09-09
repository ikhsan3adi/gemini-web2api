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

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil || len(bodyBytes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}

	var req models.OpenAIChatRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}

	modelStr := req.Model
	if modelStr == "" {
		modelStr = a.Cfg.DefaultModel
	}

	resolved, err := models.Resolve(modelStr, a.Cfg.DefaultModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}

	if req.ToolChoice == nil {
		req.ToolChoice = "auto"
	}

	prompt, images, err := format.MessagesToPrompt(req)
	if err != nil || strings.TrimSpace(prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "empty prompt"}})
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

	cid := fmt.Sprintf("chatcmpl-%s", format.RandHex(12))

	strChoice, isStr := req.ToolChoice.(string)
	isToolNone := isStr && strChoice == "none"

	if req.Stream && (len(req.Tools) == 0 || isToolNone) {
		if !startSSE(w) {
			return
		}

		// Emit first chunk with role to match OpenAI streaming protocol.
		firstChunk := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index: 0,
					Delta: &models.OpenAIMessage{
						Role: "assistant",
					},
					FinishReason: nil,
				},
			},
		}
		_ = writeSSEData(w, firstChunk)

		emitErr := a.Gem.GenerateStream(prompt, resolved.Mode, resolved.Think, fileRefs, resolved.Extra, func(delta string) error {
			chunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index: 0,
						Delta: &models.OpenAIMessage{
							Content: delta,
						},
						FinishReason: nil,
					},
				},
			}
			return writeSSEData(w, chunk)
		})

		if emitErr == nil {
			stopReason := "stop"
			endChunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index:        0,
						Delta:        &models.OpenAIMessage{},
						FinishReason: &stopReason,
					},
				},
			}
			_ = writeSSEData(w, endChunk)
			_ = writeSSEDone(w)
		} else {
			a.Logf("Chat stream error: %v", emitErr)
		}
		return
	}

	text, err := a.Gem.Generate(prompt, resolved.Mode, resolved.Think, fileRefs, resolved.Extra)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
		return
	}

	var toolCalls []models.OpenAIToolCall
	if len(req.Tools) > 0 && text != "" && !isToolNone {
		text, toolCalls = format.ParseToolCalls(text)
	}

	msg := models.OpenAIMessage{
		Role:    "assistant",
		Content: text,
	}
	if text == "" {
		msg.Content = nil
	}

	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}

	if req.Stream {
		if !startSSE(w) {
			return
		}
		chunk := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Delta:        &msg,
					FinishReason: &finish,
				},
			},
		}
		_ = writeSSEData(w, chunk)
		_ = writeSSEDone(w)
	} else {
		promptTokens := len(prompt) / 4
		completionTokens := len(text) / 4
		resp := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Message:      &msg,
					FinishReason: &finish,
				},
			},
			Usage: &models.OpenAIUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			},
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
