package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
)

func (a *App) handleBridgeStatus(w http.ResponseWriter, r *http.Request) {
	connected := a.Bridge != nil && a.Bridge.IsConnected()
	tab := ""
	if a.Bridge != nil {
		tab = a.Bridge.TabName()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connected": connected,
		"tab":       tab,
	})
}

func (a *App) handleBridgePoll(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}

	tabName := r.URL.Query().Get("tab")
	if tabName == "" {
		tabName = "active-browser-tab"
	}

	req, err := a.Bridge.WaitForNextRequest(r.Context(), tabName, 20*time.Second)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if req == nil {
		writeJSON(w, http.StatusOK, map[string]any{"timeout": true})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":     req.ID,
		"prompt": req.Prompt,
		"model":  req.Model,
	})
}

func (a *App) handleBridgeChunk(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}

	var chunk bridge.Chunk
	if err := json.NewDecoder(r.Body).Decode(&chunk); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid chunk payload"})
		return
	}

	if chunk.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing chunk id"})
		return
	}

	if err := a.Bridge.SubmitChunk(chunk); err != nil {
		writeJSON(w, http.StatusGone, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
