package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestPartialToolFenceSuffixLen(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"hello world", 0},
		{"hello `", 1},
		{"hello ``", 2},
		{"hello ```", 3},
		{"hello ```tool_cal", 11},
		{"hello ```tool_call", 0},
	}
	for _, tc := range cases {
		if got := partialToolFenceSuffixLen(tc.in); got != tc.want {
			t.Errorf("partialToolFenceSuffixLen(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestGenerateFastTitleJSONUTF8(t *testing.T) {
	raw := generateFastTitleJSON("你好 世界")
	if !utf8.ValidString(raw) {
		t.Fatalf("generateFastTitleJSON returned invalid UTF-8: %q", raw)
	}
	var parsed struct {
		IsNewTopic bool   `json:"isNewTopic"`
		Title      string `json:"title"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, raw)
	}
	if parsed.Title != "你好 世界" {
		t.Errorf("got title %q, want %q", parsed.Title, "你好 世界")
	}
}

func TestBridgeStreamChatWithToolsAndValidDeltas(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	app := New(cfg, "test")
	handler := app.Handler()

	// Register active browser tab via heartbeat
	hbReq := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	hbRec := httptest.NewRecorder()
	handler.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d", hbRec.Code)
	}

	// Simulate browser tab polling and streaming answer chunks
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 2*time.Second)
		if err != nil || req == nil {
			return
		}
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "Hello! "})
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "How can I help you today?"})
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Done: true})
	}()

	chatBody := `{
		"model": "gemini-4-hydrogen",
		"stream": true,
		"tools": [{"type":"function","function":{"name":"read_file","description":"Read a file"}}],
		"messages": [{"role":"user","content":"greetings"}]
	}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	<-workerDone

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var combined strings.Builder
	sawDone := false
	sawStop := false

	scanner := bufio.NewScanner(bytes.NewReader(rec.Body.Bytes()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		if strings.Contains(payload, `"role":""`) {
			t.Errorf("SSE chunk must not contain empty role field: %s", payload)
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE JSON chunk %q: %v", payload, err)
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		c0, _ := choices[0].(map[string]any)
		if delta, ok := c0["delta"].(map[string]any); ok {
			if content, ok := delta["content"].(string); ok {
				combined.WriteString(content)
			}
		}
		if fr, ok := c0["finish_reason"].(string); ok && fr == "stop" {
			sawStop = true
		}
	}

	if !sawDone {
		t.Errorf("expected data: [DONE] terminator")
	}
	if !sawStop {
		t.Errorf("expected finish_reason=stop")
	}
	if combined.String() != "Hello! How can I help you today?" {
		t.Errorf("got streamed content %q, want %q", combined.String(), "Hello! How can I help you today?")
	}
}

func TestBridgeStreamChatToolCallIndexed(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.Heartbeat("vertexaisearch.cloud.google.com", "tab_2")

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_2", 2*time.Second)
		if err != nil || req == nil {
			return
		}
		// Split opening ``` across chunks to verify fence buffering
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "```"})
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"main.go\"}}\n```"})
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Done: true})
	}()

	chatBody := `{
		"model": "gemini-4-hydrogen",
		"stream": true,
		"tools": [{"type":"function","function":{"name":"read_file","description":"Read a file"}}],
		"messages": [{"role":"user","content":"read main.go"}]
	}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	<-workerDone

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	sawToolCallIndex0 := false
	sawToolCallsFinish := false
	var leakedContent strings.Builder

	scanner := bufio.NewScanner(bytes.NewReader(rec.Body.Bytes()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE JSON: %v", err)
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		c0, _ := choices[0].(map[string]any)
		if delta, ok := c0["delta"].(map[string]any); ok {
			if content, ok := delta["content"].(string); ok {
				leakedContent.WriteString(content)
			}
			if tcs, ok := delta["tool_calls"].([]any); ok && len(tcs) == 1 {
				tc0, _ := tcs[0].(map[string]any)
				if idx, ok := tc0["index"].(float64); ok && idx == 0 {
					sawToolCallIndex0 = true
				}
			}
		}
		if fr, ok := c0["finish_reason"].(string); ok && fr == "tool_calls" {
			sawToolCallsFinish = true
		}
	}

	if leakedContent.String() != "" {
		t.Errorf("unexpected leaked content before tool call: %q", leakedContent.String())
	}
	if !sawToolCallIndex0 {
		t.Errorf("expected tool_calls delta with index=0")
	}
	if !sawToolCallsFinish {
		t.Errorf("expected finish_reason=tool_calls")
	}
}
