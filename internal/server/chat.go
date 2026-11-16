package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
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
	setModelHeaders(w, resolved)

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
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
			return
		}
	}

	gemOpt := gemini.ModelOptions{
		ModelName:   resolved.Name,
		ModelID:     resolved.Mode,
		ThinkMode:   resolved.Think,
		ModelHeader: resolved.Header,
		Extra:       resolved.Extra,
	}

	cid := fmt.Sprintf("chatcmpl-%s", format.RandHex(12))

	strChoice, isStr := req.ToolChoice.(string)
	isToolNone := isStr && strChoice == "none"

	// Intercept automatic client title / topic generation requests (e.g. WorkBuddy)
	// and fulfill them instantly without tying up the browser tab or polluting chat history.
	if isTitle, sessionText := isTitleSummaryRequest(prompt); isTitle {
		titleJSON := generateFastTitleJSON(sessionText)
		a.Logf("Fast-tracking client title generation request locally: %s", titleJSON)

		if req.Stream {
			if !startSSE(w) {
				return
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

			contentChunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index: 0,
						Delta: &models.OpenAIMessage{
							Content: titleJSON,
						},
						FinishReason: nil,
					},
				},
			}
			_ = writeSSEData(w, contentChunk)

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

		finish := "stop"
		msg := models.OpenAIMessage{
			Role:    "assistant",
			Content: titleJSON,
		}
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
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Check if an authenticated browser tab is connected via the extension bridge.
	if a.Bridge != nil && a.Bridge.IsConnected() {
		a.Logf("Routing chat completion through active browser bridge (%s)", a.Bridge.TabName())

		if req.Stream {
			if !startSSE(w) {
				return
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

			// Keepalive ticker flushes SSE comments every 8s to prevent client connection timeout
			stopKeepalive := make(chan struct{})
			keepaliveTicker := time.NewTicker(8 * time.Second)
			go func() {
				for {
					select {
					case <-keepaliveTicker.C:
						_ = writeSSEComment(w, "keepalive")
					case <-stopKeepalive:
						keepaliveTicker.Stop()
						return
					case <-r.Context().Done():
						keepaliveTicker.Stop()
						return
					}
				}
			}()

			var fullText strings.Builder
			hasTools := len(req.Tools) > 0 && !isToolNone
			isBufferingToolCall := false

			emitErr := a.Bridge.GenerateStream(r.Context(), prompt, resolved.Name, func(delta string) error {
				fullText.WriteString(delta)
				current := fullText.String()

				// If tools are defined, check whether model is emitting a structured tool call block.
				// If so, buffer the tool call block so it can be parsed upon completion.
				if hasTools {
					trimmed := strings.TrimSpace(current)
					if strings.HasPrefix(trimmed, "```tool_call") || strings.HasPrefix(trimmed, `{"name"`) || strings.Contains(current, "```tool_call") {
						isBufferingToolCall = true
						return nil
					}
				}

				// Otherwise, stream text chunks in real-time to the client so WorkBuddy renders live tokens
				// and prevents client-side idle timeouts ("User Cancelled").
				if !isBufferingToolCall {
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
				}
				return nil
			})
			close(stopKeepalive)

			if emitErr == nil {
				if hasTools {
					cleanText, toolCalls := format.ParseToolCalls(fullText.String())
					msg := models.OpenAIMessage{}
					stopReason := "stop"
					if len(toolCalls) > 0 {
						msg.ToolCalls = toolCalls
						stopReason = "tool_calls"
					}
					if isBufferingToolCall && cleanText != "" {
						msg.Content = cleanText
					}
					endChunk := models.OpenAIChatResponse{
						ID:      cid,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   resolved.Name,
						Choices: []models.OpenAIChoice{
							{
								Index:        0,
								Delta:        &msg,
								FinishReason: &stopReason,
							},
						},
					}
					_ = writeSSEData(w, endChunk)
				} else {
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
				}
				_ = writeSSEDone(w)
			} else {
				a.Logf("Bridge chat stream error: %v", emitErr)
			}
			return
		}

		text, err := a.Bridge.Generate(r.Context(), prompt, resolved.Name)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("bridge error: %v", err)}})
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
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

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

		emitErr := a.Gem.GenerateStream(prompt, gemOpt, fileRefs, func(delta string) error {
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

	text, err := a.Gem.Generate(prompt, gemOpt, fileRefs)
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
