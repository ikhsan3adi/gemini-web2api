package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

const toolCallFence = "```tool_call"

var toolCallFences = []string{
	"```tool_call",
	"<tool_call>",
	"```json",
}

// partialToolFenceSuffixLen returns the length of the longest suffix of s
// that is a proper prefix of any supported tool-call opening marker
// ("```tool_call", "<tool_call>", "```json"), so streaming holds only a few bytes
// while confirming whether characters begin a structured tool-call block.
func partialToolFenceSuffixLen(s string) int {
	best := 0
	for _, fence := range toolCallFences {
		maxCheck := len(fence) - 1
		if len(s) < maxCheck {
			maxCheck = len(s)
		}
		for k := maxCheck; k > best; k-- {
			if strings.HasSuffix(s, fence[:k]) {
				best = k
				break
			}
		}
	}
	return best
}

func findToolFenceStart(s string) int {
	earliest := -1
	for _, fence := range toolCallFences {
		if idx := strings.Index(s, fence); idx != -1 {
			if earliest == -1 || idx < earliest {
				earliest = idx
			}
		}
	}
	return earliest
}

func indexToolCalls(toolCalls []models.OpenAIToolCall) []models.OpenAIToolCall {
	indexed := make([]models.OpenAIToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		idx := i
		tc.Index = &idx
		indexed[i] = tc
	}
	return indexed
}

func writeBridgeRateLimitError(w http.ResponseWriter, retryAfter time.Duration, msg string) {
	secs := int(retryAfter.Round(time.Second).Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "rate_limit_error",
			"code":    "rate_limit_exceeded",
		},
	})
}

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
	allowedTools := format.AllowedToolNames(req.Tools)

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
						Delta: &models.OpenAIDelta{
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
						Delta: &models.OpenAIDelta{
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
						Delta:        &models.OpenAIDelta{},
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
		if rem := a.Bridge.CooldownRemaining(); rem > 0 {
			rlErr := &bridge.RateLimitError{RetryAfter: rem}
			a.Logf("Bridge rate-limit circuit breaker active: %v", rlErr)
			writeBridgeRateLimitError(w, rem, rlErr.Error())
			return
		}

		a.Logf("Routing chat completion through active browser bridge (%s)", a.Bridge.TabName())
		prepared := a.prepareBridgeRequest(r, req, prompt, images)
		bridgeOpts := bridge.GenerateOptions{
			Prompt:      prepared.Prompt,
			Model:       resolved.Name,
			Attachments: prepared.Attachments,
			NewChat:     prepared.NewChat,
		}

		if req.Stream {
			if !startSSE(w) {
				return
			}

			var sseMu sync.Mutex
			safeWriteSSE := func(data any) error {
				sseMu.Lock()
				defer sseMu.Unlock()
				return writeSSEData(w, data)
			}
			safeWriteDone := func() error {
				sseMu.Lock()
				defer sseMu.Unlock()
				return writeSSEDone(w)
			}

			firstChunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index: 0,
						Delta: &models.OpenAIDelta{
							Role: "assistant",
						},
						FinishReason: nil,
					},
				},
			}
			_ = safeWriteSSE(firstChunk)

			// Keepalive ticker flushes empty delta chunks ({}) every 5s with mutex synchronization
			// to keep client stream parsers active and prevent idle timeouts.
			stopKeepalive := make(chan struct{})
			doneKeepalive := make(chan struct{})
			keepaliveTicker := time.NewTicker(5 * time.Second)
			go func() {
				defer close(doneKeepalive)
				defer keepaliveTicker.Stop()
				for {
					select {
					case <-keepaliveTicker.C:
						pingChunk := models.OpenAIChatResponse{
							ID:      cid,
							Object:  "chat.completion.chunk",
							Created: time.Now().Unix(),
							Model:   resolved.Name,
							Choices: []models.OpenAIChoice{
								{
									Index: 0,
									Delta: &models.OpenAIDelta{},
								},
							},
						}
						_ = safeWriteSSE(pingChunk)
					case <-stopKeepalive:
						return
					case <-r.Context().Done():
						return
					}
				}
			}()

			var fullText strings.Builder
			var bufferedTail strings.Builder
			hasTools := len(req.Tools) > 0 && !isToolNone
			isBufferingToolCall := false
			pending := ""
			streamedAny := false

			emitContent := func(text string) error {
				if text == "" {
					return nil
				}
				streamedAny = true
				chunk := models.OpenAIChatResponse{
					ID:      cid,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   resolved.Name,
					Choices: []models.OpenAIChoice{
						{
							Index: 0,
							Delta: &models.OpenAIDelta{
								Content: text,
							},
							FinishReason: nil,
						},
					},
				}
				return safeWriteSSE(chunk)
			}

			emitErr := a.Bridge.GenerateStreamWithOptions(r.Context(), bridgeOpts, func(delta string) error {
				fullText.WriteString(delta)

				if !hasTools {
					return emitContent(delta)
				}

				if isBufferingToolCall {
					bufferedTail.WriteString(delta)
					return nil
				}

				pending += delta

				// Detect start of a ```tool_call, <tool_call>, or ```json block; flush any leading prose
				if idx := findToolFenceStart(pending); idx != -1 {
					isBufferingToolCall = true
					bufferedTail.WriteString(pending[idx:])
					if idx > 0 {
						leading := pending[:idx]
						pending = ""
						return emitContent(leading)
					}
					pending = ""
					return nil
				}

				// Hold only a tiny trailing suffix if it could be the opening of a tool fence
				hold := partialToolFenceSuffixLen(pending)
				if len(pending) > hold {
					toEmit := pending[:len(pending)-hold]
					pending = pending[len(pending)-hold:]
					return emitContent(toEmit)
				}
				return nil
			})

			close(stopKeepalive)
			<-doneKeepalive

			if emitErr == nil {
				if strings.TrimSpace(fullText.String()) != "" {
					a.recordBridgeTurnSuccess(prepared)
				}
				if hasTools {
					if isBufferingToolCall {
						_, toolCalls := format.ParseToolCallsWithAllowed(fullText.String(), allowedTools)
						if len(toolCalls) > 0 {
							tcChunk := models.OpenAIChatResponse{
								ID:      cid,
								Object:  "chat.completion.chunk",
								Created: time.Now().Unix(),
								Model:   resolved.Name,
								Choices: []models.OpenAIChoice{
									{
										Index: 0,
										Delta: &models.OpenAIDelta{
											ToolCalls: indexToolCalls(toolCalls),
										},
										FinishReason: nil,
									},
								},
							}
							_ = safeWriteSSE(tcChunk)

							stopReason := "tool_calls"
							endChunk := models.OpenAIChatResponse{
								ID:      cid,
								Object:  "chat.completion.chunk",
								Created: time.Now().Unix(),
								Model:   resolved.Name,
								Choices: []models.OpenAIChoice{
									{
										Index:        0,
										Delta:        &models.OpenAIDelta{},
										FinishReason: &stopReason,
									},
								},
							}
							_ = safeWriteSSE(endChunk)
						} else {
							// Fence did not contain a valid declared tool call (e.g. normal ```json``` snippet);
							// flush the exact buffered remainder verbatim so nothing is dropped or duplicated.
							if tail := bufferedTail.String(); tail != "" {
								_ = emitContent(tail)
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
										Delta:        &models.OpenAIDelta{},
										FinishReason: &stopReason,
									},
								},
							}
							_ = safeWriteSSE(endChunk)
						}
					} else {
						// Flush any held trailing characters (e.g. a normal markdown closing fence)
						if pending != "" {
							_ = emitContent(pending)
							pending = ""
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
									Delta:        &models.OpenAIDelta{},
									FinishReason: &stopReason,
								},
							},
						}
						_ = safeWriteSSE(endChunk)
					}
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
								Delta:        &models.OpenAIDelta{},
								FinishReason: &stopReason,
							},
						},
					}
					_ = safeWriteSSE(endChunk)
				}
				_ = safeWriteDone()
			} else {
				a.Logf("Bridge chat stream error: %v", emitErr)
				if r.Context().Err() == nil {
					if !streamedAny {
						_ = emitContent(fmt.Sprintf("[Bridge Error] %v", emitErr))
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
								Delta:        &models.OpenAIDelta{},
								FinishReason: &stopReason,
							},
						},
					}
					_ = safeWriteSSE(endChunk)
					_ = safeWriteDone()
				}
			}
			return
		}

		text, err := a.Bridge.GenerateWithOptions(r.Context(), bridgeOpts)
		if err != nil {
			var rlErr *bridge.RateLimitError
			if errors.As(err, &rlErr) {
				writeBridgeRateLimitError(w, rlErr.RetryAfter, rlErr.Error())
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("bridge error: %v", err)}})
			return
		}
		if strings.TrimSpace(text) != "" {
			a.recordBridgeTurnSuccess(prepared)
		}

		var toolCalls []models.OpenAIToolCall
		if len(req.Tools) > 0 && text != "" && !isToolNone {
			text, toolCalls = format.ParseToolCallsWithAllowed(text, allowedTools)
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

	// Direct Gemini API mode: upload images to Gemini storage via cookies.
	var fileRefs []string
	if len(images) > 0 {
		fileRefs, err = a.uploadImages(images)
		if err != nil {
			a.Logf("Image upload error: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
			return
		}
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
					Delta: &models.OpenAIDelta{
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
						Delta: &models.OpenAIDelta{
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
						Delta:        &models.OpenAIDelta{},
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
		text, toolCalls = format.ParseToolCallsWithAllowed(text, allowedTools)
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
		delta := &models.OpenAIDelta{
			Role: "assistant",
		}
		if text != "" {
			delta.Content = text
		}
		if len(toolCalls) > 0 {
			delta.ToolCalls = indexToolCalls(toolCalls)
		}
		chunk := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Delta:        delta,
					FinishReason: nil,
				},
			},
		}
		_ = writeSSEData(w, chunk)
		endChunk := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Delta:        &models.OpenAIDelta{},
					FinishReason: &finish,
				},
			},
		}
		_ = writeSSEData(w, endChunk)
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
