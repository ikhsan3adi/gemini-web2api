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

	resolved, err := a.resolveModel(modelStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	setModelHeaders(w, resolved)

	if req.ToolChoice == nil {
		req.ToolChoice = "auto"
	}

	prompt, _, err := format.MessagesToPrompt(req)
	if err != nil || strings.TrimSpace(prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "empty prompt"}})
		return
	}

	cid := fmt.Sprintf("chatcmpl-%s", format.RandHex(12))

	strChoice, isStr := req.ToolChoice.(string)
	isToolNone := isStr && strChoice == "none"

	if req.Stream && (len(req.Tools) == 0 || isToolNone) {
		_, started, emitErr := a.streamWithSession(
			w, r, resolved, req,
			func(_ *SessionPlan) bool {
				if !startSSE(w) {
					return false
				}
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
				return true
			},
			func(delta string) error {
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
			},
		)

		if emitErr != nil {
			if !started {
				if isSessionValidationError(emitErr) {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": emitErr.Error()}})
				} else {
					writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", emitErr)}})
				}
			} else {
				a.Logf("Chat stream error: %v", emitErr)
			}
			return
		}

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
		return
	}

	text, _, err := a.generateWithSession(w, r, resolved, req)
	if err != nil {
		if isSessionValidationError(err) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		} else {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
		}
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
