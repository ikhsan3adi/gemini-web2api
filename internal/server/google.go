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

func (a *App) handleGoogleGenerate(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("target")
	if target == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}

	modelName := target
	stream := false

	if idx := strings.LastIndex(target, ":"); idx != -1 {
		action := target[idx+1:]
		modelName = target[:idx]
		if action == "streamGenerateContent" {
			stream = true
		} else if action != "generateContent" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil || len(bodyBytes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}

	var req models.GoogleGenerateRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}

	if modelName == "" {
		modelName = a.Cfg.DefaultModel
	}

	resolved, err := models.Resolve(modelName, a.Cfg.DefaultModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}

	fcMode := "AUTO"
	if req.ToolConfig != nil && req.ToolConfig.FunctionCallingConfig != nil && req.ToolConfig.FunctionCallingConfig.Mode != "" {
		fcMode = req.ToolConfig.FunctionCallingConfig.Mode
	}

	hasTools := len(req.Tools) > 0 && fcMode != "NONE"

	prompt, images, err := format.GoogleContentsToPrompt(req)
	if err != nil || len(prompt) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "empty content"}})
		return
	}

	fileRefs, err := a.uploadImages(images)
	if err != nil {
		a.Logf("Google API image upload error: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
		return
	}
	a.Logf("Google API: model=%s stream=%t tools=%t prompt_len=%d images=%d", resolved.Name, stream, hasTools, len(prompt), len(fileRefs))

	if stream && !hasTools {
		if !startSSE(w) {
			return
		}

		var fullText string
		emitErr := a.Gem.GenerateStream(prompt, resolved.Mode, resolved.Think, fileRefs, resolved.Extra, func(delta string) error {
			if delta == "" {
				return nil
			}
			fullText += delta
			chunkObj := models.GoogleGenerateResponse{
				Candidates: []models.GoogleCandidate{
					{
						Index: 0,
						Content: &models.GoogleContent{
							Role: "model",
							Parts: []models.GooglePart{
								{Text: delta},
							},
						},
					},
				},
				ModelVersion: resolved.Name,
			}
			return writeSSEData(w, chunkObj)
		})

		if emitErr == nil {
			promptTokens := len(prompt) / 4
			candidatesTokens := len(fullText) / 4
			finalChunk := models.GoogleGenerateResponse{
				Candidates: []models.GoogleCandidate{
					{
						Index:        0,
						FinishReason: "STOP",
					},
				},
				UsageMetadata: &models.GoogleUsageMetadata{
					PromptTokenCount:     promptTokens,
					CandidatesTokenCount: candidatesTokens,
					TotalTokenCount:      promptTokens + candidatesTokens,
				},
				ModelVersion: resolved.Name,
			}
			_ = writeSSEData(w, finalChunk)
		} else {
			a.Logf("Google stream error: %v", emitErr)
		}
		return
	}

	text, err := a.Gem.Generate(prompt, resolved.Mode, resolved.Think, fileRefs, resolved.Extra)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
		return
	}

	var responseParts []models.GooglePart
	if hasTools && text != "" {
		cleanText, fnCalls := format.ParseGoogleFunctionCalls(text)
		if len(fnCalls) > 0 {
			if cleanText != "" {
				responseParts = append(responseParts, models.GooglePart{Text: cleanText})
			}
			for _, fc := range fnCalls {
				responseParts = append(responseParts, models.GooglePart{
					FunctionCall: &models.GoogleFunctionCall{
						Name: fc.Name,
						Args: fc.Args,
					},
				})
			}
		} else {
			responseParts = append(responseParts, models.GooglePart{Text: text})
		}
	} else {
		fallbackText := text
		if fallbackText == "" {
			fallbackText = "I apologize, but I was unable to generate a response. Please try again."
		}
		responseParts = append(responseParts, models.GooglePart{Text: fallbackText})
	}

	promptTokens := len(prompt) / 4
	candidatesTokens := len(text) / 4

	responseObj := models.GoogleGenerateResponse{
		Candidates: []models.GoogleCandidate{
			{
				Index: 0,
				Content: &models.GoogleContent{
					Role:  "model",
					Parts: responseParts,
				},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: &models.GoogleUsageMetadata{
			PromptTokenCount:     promptTokens,
			CandidatesTokenCount: candidatesTokens,
			TotalTokenCount:      promptTokens + candidatesTokens,
		},
		ModelVersion: resolved.Name,
	}

	if stream {
		if !startSSE(w) {
			return
		}
		_ = writeSSEData(w, responseObj)
	} else {
		writeJSON(w, http.StatusOK, responseObj)
	}
}
