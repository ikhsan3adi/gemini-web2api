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
	cooldownSec := 0
	if a.Bridge != nil {
		tab = a.Bridge.TabName()
		if rem := a.Bridge.CooldownRemaining(); rem > 0 {
			cooldownSec = int(rem.Round(time.Second).Seconds())
			if cooldownSec < 1 {
				cooldownSec = 1
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connected":    connected,
		"tab":          tab,
		"cooldown_sec": cooldownSec,
	})
}

func (a *App) handleBridgeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}

	var payload struct {
		Tab      string `json:"tab"`
		ClientID string `json:"client_id"`
		ConvGen  string `json:"conv_gen"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)

	tabName := payload.Tab
	if tabName == "" {
		tabName = r.URL.Query().Get("tab")
	}
	clientID := payload.ClientID
	if clientID == "" {
		clientID = r.URL.Query().Get("client")
	}
	convGen := payload.ConvGen
	if convGen == "" {
		convGen = r.URL.Query().Get("conv_gen")
	}

	a.Bridge.UpdateTabSession(tabName, clientID, convGen)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	clientID := r.URL.Query().Get("client")
	convGen := r.URL.Query().Get("conv_gen")

	req, err := a.Bridge.WaitForNextRequestForSession(r.Context(), tabName, clientID, convGen, 20*time.Second)
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

	resp := map[string]any{
		"id":     req.ID,
		"prompt": req.Prompt,
		"model":  req.Model,
	}
	if len(req.Attachments) > 0 {
		resp["attachments"] = req.Attachments
	}
	writeJSON(w, http.StatusOK, resp)
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
