package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestBridgeLocalPDFAutoUploadAndDedup(t *testing.T) {
	tmpDir := t.TempDir()
	pdfPath := filepath.Join(tmpDir, "annual report.pdf")
	goPath := filepath.Join(tmpDir, "main.go")
	secretPath := filepath.Join(tmpDir, "cookies_secret.pdf")

	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4 sample pdf content"), 0600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	if err := os.WriteFile(goPath, []byte("package main"), 0600); err != nil {
		t.Fatalf("write go: %v", err)
	}
	if err := os.WriteFile(secretPath, []byte("%PDF-1.4 secret"), 0600); err != nil {
		t.Fatalf("write secret pdf: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_pdf", "1")

	// 1. Remote (non-loopback) request must NOT auto-attach local files
	remoteDone := make(chan *bridge.Request, 1)
	go func() {
		req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_pdf", "1", 2*time.Second)
		remoteDone <- req
		if req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "remote ok", Done: true})
		}
	}()

	bodyObj := map[string]any{
		"model":  "gemini-4-hydrogen",
		"stream": false,
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": `Summarize "` + pdfPath + `" and ` + goPath + ` and ` + secretPath,
			},
		},
	}
	bodyBytes, _ := json.Marshal(bodyObj)

	remoteReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(bodyBytes))
	remoteReq.RemoteAddr = "203.0.113.10:44321"
	remoteReq.Header.Set("Content-Type", "application/json")
	recRemote := httptest.NewRecorder()
	handler.ServeHTTP(recRemote, remoteReq)

	gotRemote := <-remoteDone
	if gotRemote == nil {
		t.Fatal("expected bridge request for remote call")
	}
	if len(gotRemote.Attachments) != 0 {
		t.Fatalf("remote caller must not auto-upload local files, got %d", len(gotRemote.Attachments))
	}

	// 2. Localhost request auto-attaches ONLY "annual report.pdf" (skipping .go and sensitive secret filename)
	localDone1 := make(chan *bridge.Request, 1)
	go func() {
		req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_pdf", "1", 2*time.Second)
		localDone1 <- req
		if req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "PDF summarized!", Done: true})
		}
	}()

	localReq1 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(bodyBytes))
	localReq1.RemoteAddr = "127.0.0.1:54321"
	localReq1.Header.Set("Content-Type", "application/json")
	recLocal1 := httptest.NewRecorder()
	handler.ServeHTTP(recLocal1, localReq1)

	gotLocal1 := <-localDone1
	if gotLocal1 == nil {
		t.Fatal("expected bridge request for localhost turn 1")
	}
	if len(gotLocal1.Attachments) != 1 {
		t.Fatalf("expected 1 safe PDF attachment, got %d: %+v", len(gotLocal1.Attachments), gotLocal1.Attachments)
	}
	if gotLocal1.Attachments[0].Filename != "annual report.pdf" || gotLocal1.Attachments[0].MIME != "application/pdf" {
		t.Errorf("unexpected attachment metadata: %+v", gotLocal1.Attachments[0])
	}

	// 3. Follow-up turn referencing the same PDF in the same tab session must NOT re-upload it
	localDone2 := make(chan *bridge.Request, 1)
	go func() {
		req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_pdf", "1", 2*time.Second)
		localDone2 <- req
		if req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "Follow up answer", Done: true})
		}
	}()

	followUpBytes, _ := json.Marshal(map[string]any{
		"model":  "gemini-4-hydrogen",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "user", "content": `Summarize "` + pdfPath + `"`},
			map[string]any{"role": "assistant", "content": "PDF summarized!"},
			map[string]any{"role": "user", "content": `Check page 2 of "` + pdfPath + `"`},
		},
	})
	localReq2 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(followUpBytes))
	localReq2.RemoteAddr = "127.0.0.1:54322"
	localReq2.Header.Set("Content-Type", "application/json")
	recLocal2 := httptest.NewRecorder()
	handler.ServeHTTP(recLocal2, localReq2)

	gotLocal2 := <-localDone2
	if gotLocal2 == nil {
		t.Fatal("expected bridge request for localhost turn 2")
	}
	if len(gotLocal2.Attachments) != 0 {
		t.Errorf("expected 0 duplicate attachments on follow-up turn, got %d", len(gotLocal2.Attachments))
	}
}

func TestBridgeDeltaContextAndConvReset(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeDeltaContext = true
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_delta", "1")

	tools := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read a file from disk",
			},
		},
	}

	runTurn := func(convGen string, msgs []any, replyText string) *bridge.Request {
		ch := make(chan *bridge.Request, 1)
		go func() {
			req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_delta", convGen, 2*time.Second)
			ch <- req
			if req != nil {
				_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: replyText, Done: true})
			}
		}()

		body, _ := json.Marshal(map[string]any{
			"model":    "gemini-4-hydrogen",
			"stream":   false,
			"tools":    tools,
			"messages": msgs,
		})
		httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httpReq)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return <-ch
	}

	// Turn 1: full prompt with # Tool Use and system prompt
	msgs1 := []any{
		map[string]any{"role": "system", "content": "You are WorkBuddy."},
		map[string]any{"role": "user", "content": "Check main.go"},
	}
	req1 := runTurn("1", msgs1, "```tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"main.go\"}}\n```")
	if req1 == nil || !strings.Contains(req1.Prompt, "# Tool Use") || !strings.Contains(req1.Prompt, "You are WorkBuddy.") {
		t.Fatalf("turn 1 should receive full prompt, got %+v", req1)
	}

	// Turn 2 (same conv_gen=1): compact delta prompt with ONLY the tool result
	msgs2 := append(append([]any{}, msgs1...),
		map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id":   "call_1",
					"type": "function",
					"function": map[string]any{
						"name":      "read_file",
						"arguments": `{"path":"main.go"}`,
					},
				},
			},
		},
		map[string]any{"role": "tool", "name": "read_file", "content": "package main // ok"},
	)
	req2 := runTurn("1", msgs2, "main.go looks good!")
	if req2 == nil {
		t.Fatal("expected turn 2 bridge request")
	}
	if strings.Contains(req2.Prompt, "# Tool Use") || strings.Contains(req2.Prompt, "You are WorkBuddy.") {
		t.Errorf("turn 2 should use compact delta prompt without repeating system/tools, got %q", req2.Prompt)
	}
	if !strings.Contains(req2.Prompt, "[Tool result for read_file]: package main // ok") {
		t.Errorf("turn 2 delta prompt missing tool result, got %q", req2.Prompt)
	}

	// Turn 3 (after browser started New Chat -> conv_gen=2): must resend full prompt!
	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_delta", "2")
	msgs3 := append(append([]any{}, msgs2...),
		map[string]any{"role": "assistant", "content": "main.go looks good!"},
		map[string]any{"role": "user", "content": "Now explain it"},
	)
	req3 := runTurn("2", msgs3, "Here is the explanation.")
	if req3 == nil || !strings.Contains(req3.Prompt, "# Tool Use") || !strings.Contains(req3.Prompt, "You are WorkBuddy.") {
		t.Errorf("turn 3 after conv_gen change should resend full prompt, got %+v", req3)
	}
}

func TestBridgeAntiBanCooldown(t *testing.T) {
	hub := bridge.NewHub()
	hub.SetMinInterval(80 * time.Millisecond)
	hub.UpdateTabSession("gemini.google.com", "tab_cool", "1")

	dispatchTimes := make([]time.Time, 0, 2)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for i := 0; i < 2; i++ {
			req, err := hub.WaitForNextRequestForSession(context.Background(), "gemini.google.com", "tab_cool", "1", 2*time.Second)
			if err != nil || req == nil {
				return
			}
			dispatchTimes = append(dispatchTimes, time.Now())
			_ = hub.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
		}
	}()

	if _, err := hub.Generate(context.Background(), "first", "gemini-4-hydrogen"); err != nil {
		t.Fatalf("first generate failed: %v", err)
	}
	if _, err := hub.Generate(context.Background(), "second", "gemini-4-hydrogen"); err != nil {
		t.Fatalf("second generate failed: %v", err)
	}
	<-workerDone

	if len(dispatchTimes) != 2 {
		t.Fatalf("expected 2 dispatches, got %d", len(dispatchTimes))
	}
	gap := dispatchTimes[1].Sub(dispatchTimes[0])
	if gap < 65*time.Millisecond {
		t.Errorf("anti-ban cooldown not respected: gap = %v, want >= 65ms", gap)
	}
}

func TestBridgeOpenAIFileAttachmentForwardedWithoutCookies(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("gemini.google.com", "tab_file", "1")

	pollResult := make(chan map[string]any, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		pollReq := httptest.NewRequest("GET", "/bridge/poll?tab=gemini.google.com&client=tab_file&conv_gen=1", nil)
		pollRec := httptest.NewRecorder()
		handler.ServeHTTP(pollRec, pollReq)
		var parsed map[string]any
		_ = json.Unmarshal(pollRec.Body.Bytes(), &parsed)
		pollResult <- parsed
		if id, ok := parsed["id"].(string); ok && id != "" {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: id, Text: "Read your attached PDF directly!", Done: true})
		}
	}()

	// Give poll goroutine a moment to register before sending chat request
	time.Sleep(25 * time.Millisecond)

	chatBody := `{
		"model": "gemini-4-hydrogen",
		"stream": false,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Summarize this PDF"},
				{"type": "file", "file": {"filename": "deck.pdf", "file_data": "data:application/pdf;base64,JVBERi0xLjQK"}}
			]
		}]
	}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	<-workerDone

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without cookies in bridge mode, got %d: %s", rec.Code, rec.Body.String())
	}

	polled := <-pollResult
	atts, ok := polled["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("expected 1 attachment in /bridge/poll JSON, got %#v", polled["attachments"])
	}
	att0, _ := atts[0].(map[string]any)
	if att0["filename"] != "deck.pdf" || att0["mime"] != "application/pdf" {
		t.Errorf("unexpected polled attachment: %#v", att0)
	}
}

func TestBridgeLargeCodeAutoUploadAndRefreshOnEdit(t *testing.T) {
	tmpDir := t.TempDir()
	smallGo := filepath.Join(tmpDir, "tiny.go")
	largeTS := filepath.Join(tmpDir, "big_service.ts")

	if err := os.WriteFile(smallGo, []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatalf("write small go: %v", err)
	}
	v1Code := "// version 1\n" + strings.Repeat("export const line = 'hello world';\n", 450) // ~15.7 KB
	if err := os.WriteFile(largeTS, []byte(v1Code), 0600); err != nil {
		t.Fatalf("write large ts: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeJitterMs = 0
	cfg.BridgeMaxPer10Min = 0
	cfg.BridgeLargeCodeUploadBytes = 12288
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_code", "1")

	var history []any
	sendTurn := func(userPrompt string) *bridge.Request {
		history = append(history, map[string]any{"role": "user", "content": userPrompt})
		ch := make(chan *bridge.Request, 1)
		go func() {
			req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_code", "1", 2*time.Second)
			ch <- req
			if req != nil {
				_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
			}
		}()

		body, _ := json.Marshal(map[string]any{
			"model":    "gemini-4-hydrogen",
			"stream":   false,
			"messages": history,
		})
		httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		httpReq.RemoteAddr = "127.0.0.1:54321"
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httpReq)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		history = append(history, map[string]any{"role": "assistant", "content": "ok"})
		return <-ch
	}

	// Turn 1: auto-uploads large big_service.ts verbatim, skips tiny.go (< 12 KB)
	req1 := sendTurn("Refactor " + largeTS + " and check " + smallGo)
	if req1 == nil || len(req1.Attachments) != 1 {
		t.Fatalf("expected 1 large code attachment, got %+v", req1)
	}
	if req1.Attachments[0].Filename != "big_service.ts" || req1.Attachments[0].Source != "local_large_code" {
		t.Errorf("unexpected attachment: %+v", req1.Attachments[0])
	}
	decoded1, _ := base64.StdEncoding.DecodeString(req1.Attachments[0].DataBase64)
	if string(decoded1) != v1Code {
		t.Errorf("uploaded code content mismatch (len %d vs %d)", len(decoded1), len(v1Code))
	}

	// Turn 2: unchanged file on disk must NOT be re-uploaded
	req2 := sendTurn("Explain function 2 in " + largeTS)
	if req2 == nil {
		t.Fatal("expected turn 2 request")
	}
	if len(req2.Attachments) != 0 {
		t.Errorf("expected 0 duplicate uploads for unchanged large code file, got %d", len(req2.Attachments))
	}

	// Turn 3: after editing big_service.ts on disk (new size/mtime), it MUST re-upload fresh version!
	v2Code := "// version 2 edited\n" + v1Code + "export const added = true;\n"
	if err := os.WriteFile(largeTS, []byte(v2Code), 0600); err != nil {
		t.Fatalf("rewrite large ts: %v", err)
	}
	req3 := sendTurn("Now review updated " + largeTS)
	if req3 == nil || len(req3.Attachments) != 1 {
		t.Fatalf("expected edited large code file to re-upload fresh version, got %+v", req3)
	}
	decoded3, _ := base64.StdEncoding.DecodeString(req3.Attachments[0].DataBase64)
	if string(decoded3) != v2Code {
		t.Errorf("expected updated v2 code in re-upload")
	}
}

func TestBridgeOversizedToolResultSpillover(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeJitterMs = 0
	cfg.BridgeMaxPer10Min = 0
	cfg.BridgeSpilloverChars = 1000
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_spill", "1")

	ch := make(chan *bridge.Request, 1)
	go func() {
		req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_spill", "1", 2*time.Second)
		ch <- req
		if req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "Reviewed attached code!", Done: true})
		}
	}()

	hugeCode := strings.Repeat("func compute(x int) int { return x * 2 }\n", 100) // ~4100 chars > 1000 threshold
	body, _ := json.Marshal(map[string]any{
		"model":  "gemini-4-hydrogen",
		"stream": false,
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "edit_file", "description": "Edit file"}},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "Review this code:\n" + hugeCode},
		},
	})
	httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got := <-ch
	if got == nil {
		t.Fatal("expected bridge request")
	}
	if len(got.Prompt) > 1400 {
		t.Errorf("expected compact prompt after spillover, got %d chars", len(got.Prompt))
	}
	if !strings.Contains(got.Prompt, "# Tool Use") {
		t.Errorf("expected # Tool Use header to remain inline, got %q", got.Prompt)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Source != "spillover" {
		t.Fatalf("expected 1 spillover attachment, got %+v", got.Attachments)
	}
	decoded, _ := base64.StdEncoding.DecodeString(got.Attachments[0].DataBase64)
	if !strings.Contains(string(decoded), "func compute(x int) int") {
		t.Errorf("spillover attachment missing verbatim code body")
	}
}

func TestBridgeRateLimitCircuitBreakerAnd429(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeJitterMs = 0
	cfg.BridgeMaxPer10Min = 0
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_rl", "1")

	// 1. First request receives HTTP 429 rate-limited chunk from extension -> returns 429 + Retry-After
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_rl", "1", 2*time.Second)
		if req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{
				ID:          req.ID,
				Done:        true,
				Error:       "HTTP 429 Too Many Requests",
				RateLimited: true,
			})
		}
	}()

	chatBody := `{"model":"gemini-4-hydrogen","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	<-workerDone

	if rec1.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on rate-limited bridge error, got %d: %s", rec1.Code, rec1.Body.String())
	}
	if rec1.Header().Get("Retry-After") == "" {
		t.Errorf("expected Retry-After header on 429 response")
	}

	// 2. Immediate second request must fail fast with 429 WITHOUT hitting the browser tab
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected immediate circuit-breaker 429, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// 3. /bridge/status exposes active cooldown_sec > 0
	statusReq := httptest.NewRequest("GET", "/bridge/status", nil)
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)
	var statusResp map[string]any
	_ = json.Unmarshal(statusRec.Body.Bytes(), &statusResp)
	if sec, ok := statusResp["cooldown_sec"].(float64); !ok || sec <= 0 {
		t.Errorf("expected cooldown_sec > 0 in /bridge/status, got %#v", statusResp)
	}
}

func TestCanonicalPathKeyAndDeduplicateBridgeAttachments(t *testing.T) {
	k1 := canonicalPathKey(`C:\Users\Hau\Desktop\Report.PDF`)
	k2 := canonicalPathKey(`c:/users/hau/desktop/report.pdf`)
	if k1 != k2 {
		t.Errorf("expected case/slash-normalized Windows keys to match, got %q vs %q", k1, k2)
	}

	b64A := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 same content"))
	b64B := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 different content"))
	deduped := DeduplicateBridgeAttachments([]bridge.Attachment{
		{Filename: "report.pdf", MIME: "application/pdf", DataBase64: b64A, Size: 21},
		{Filename: "report_alias.pdf", MIME: "application/pdf", DataBase64: b64A, Size: 21},
		{Filename: "second.pdf", MIME: "application/pdf", DataBase64: b64B, Size: 26},
		{Filename: "SECOND.PDF", MIME: "application/pdf", DataBase64: b64B, Size: 26},
	})
	if len(deduped) != 2 {
		t.Fatalf("expected 2 unique attachments, got %d: %+v", len(deduped), deduped)
	}
	if deduped[0].Filename != "report.pdf" || deduped[1].Filename != "second.pdf" {
		t.Errorf("unexpected deduped filenames: %+v", deduped)
	}
}

func TestBridgeDeduplicatesSameFileFromAPIAndLocalPath(t *testing.T) {
	tmpDir := t.TempDir()
	pdfPath := filepath.Join(tmpDir, "quarterly.pdf")
	pdfBytes := []byte("%PDF-1.4\n%dedup-test-content\n%%EOF\n")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		t.Fatalf("failed to write temp pdf: %v", err)
	}
	pdfB64 := base64.StdEncoding.EncodeToString(pdfBytes)

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeJitterMs = 0
	cfg.BridgeMaxPer10Min = 0
	cfg.BridgeAutoUploadLocalFiles = true
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_dedup", "1")

	var received *bridge.Request
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		req, err := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_dedup", "1", 2*time.Second)
		if err != nil || req == nil {
			return
		}
		received = req
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "Read cleanly once.", Done: true})
	}()

	// Send the SAME PDF twice via OpenAI `file` parts AND reference its local path twice in text!
	bodyMap := map[string]any{
		"model":  "gemini-4-hydrogen",
		"stream": false,
		"messages": []map[string]any{
			{"role": "system", "content": "Working directory: " + tmpDir},
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "Analyze " + pdfPath + " and quarterly.pdf"},
					{"type": "file", "file": map[string]any{"filename": "quarterly.pdf", "file_data": "data:application/pdf;base64," + pdfB64}},
					{"type": "file", "file": map[string]any{"filename": "quarterly.pdf", "file_data": "data:application/pdf;base64," + pdfB64}},
				},
			},
		},
	}
	rawBody, _ := json.Marshal(bodyMap)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(rawBody))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	<-workerDone

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if received == nil {
		t.Fatalf("bridge did not receive request")
	}
	if len(received.Attachments) != 1 {
		t.Fatalf("expected EXACTLY 1 deduplicated attachment, got %d: %+v", len(received.Attachments), received.Attachments)
	}
	if received.Attachments[0].Filename != "quarterly.pdf" {
		t.Errorf("expected quarterly.pdf, got %q", received.Attachments[0].Filename)
	}
}

func TestBridgeHistoricalAPIAttachmentNotReuploadedWhenSystemPromptChanges(t *testing.T) {
	pdfBytes := []byte("%PDF-1.4\n%historical-attachment\n%%EOF\n")
	pdfB64 := base64.StdEncoding.EncodeToString(pdfBytes)

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeJitterMs = 0
	cfg.BridgeMaxPer10Min = 0
	cfg.BridgeDeltaContext = true
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_main", "1")

	runTurn := func(bodyMap map[string]any, sendBgHeartbeat bool) *bridge.Request {
		var got *bridge.Request
		done := make(chan struct{})
		go func() {
			defer close(done)
			req, err := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_main", "1", 2*time.Second)
			if err != nil || req == nil {
				return
			}
			got = req
			if sendBgHeartbeat {
				// Simulate a second open browser tab sending a heartbeat mid-turn
				app.Bridge.UpdateTabSession("gemini.google.com", "tab_bg", "1")
			}
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "OK", Done: true})
		}()
		raw, _ := json.Marshal(bodyMap)
		httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(raw))
		httpReq.RemoteAddr = "127.0.0.1:54322"
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httpReq)
		<-done
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return got
	}

	// Turn 1: attaches report.pdf (while a background tab heartbeats mid-turn)
	req1 := runTurn(map[string]any{
		"model":  "gemini-4-hydrogen",
		"stream": false,
		"messages": []map[string]any{
			{"role": "system", "content": "Current time: 10:00"},
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "Summarize this PDF"},
					{"type": "file", "file": map[string]any{"filename": "report.pdf", "file_data": "data:application/pdf;base64," + pdfB64}},
				},
			},
		},
	}, true)
	if req1 == nil || len(req1.Attachments) != 1 {
		t.Fatalf("turn 1 should upload 1 attachment, got %+v", req1)
	}

	// Turn 2: system prompt timestamp changed (so delta context misses), history still contains Turn 1's PDF
	req2 := runTurn(map[string]any{
		"model":  "gemini-4-hydrogen",
		"stream": false,
		"messages": []map[string]any{
			{"role": "system", "content": "Current time: 10:05"},
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "Summarize this PDF"},
					{"type": "file", "file": map[string]any{"filename": "report.pdf", "file_data": "data:application/pdf;base64," + pdfB64}},
				},
			},
			{"role": "assistant", "content": "OK"},
			{"role": "user", "content": "Now explain page 2"},
		},
	}, false)
	if req2 == nil {
		t.Fatalf("turn 2 did not reach bridge")
	}
	if len(req2.Attachments) != 0 {
		t.Fatalf("turn 2 must NOT re-upload historical report.pdf, got %d attachment(s): %+v", len(req2.Attachments), req2.Attachments)
	}
}

func TestSpilloverNeverBuriesToolUseHeaderWhenToolsAreLarge(t *testing.T) {
	// Even when # Tool Use + Available tools exceeds maxChars, a short user prompt
	// must NEVER be dumped into full_prompt_context.txt (which caused Vertex AI Search
	// to route the prompt into its passive file-reader sub-agent and break tool calls).
	largeToolHeader := "# Tool Use\n\nAvailable tools:\n[" + strings.Repeat(`{"name":"tool","desc":"desc"},`, 120) + "{}]"
	prompt := largeToolHeader + "\n\nlook into my sidebar here, retain the local files that store these 3 existing chats, others dlt"

	outPrompt, att := SpilloverOversizedPrompt(prompt, 2000, false)
	if att != nil {
		t.Fatalf("expected NO spillover attachment when only # Tool Use header is large and user body is short, got %+v", att)
	}
	if outPrompt != prompt || !strings.Contains(outPrompt, "# Tool Use") {
		t.Errorf("expected prompt with # Tool Use header to remain completely inline")
	}
}

func TestBridgeDeltaUserFollowUpIncludesLocalBridgeToolReminder(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeJitterMs = 0
	cfg.BridgeMaxPer10Min = 0
	cfg.BridgeDeltaContext = true
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_agent", "1")

	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "list_dir", "description": "List directory"}},
		map[string]any{"type": "function", "function": map[string]any{"name": "delete_file", "description": "Delete a file"}},
	}

	runTurn := func(msgs []any, reply string) *bridge.Request {
		ch := make(chan *bridge.Request, 1)
		go func() {
			req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_agent", "1", 2*time.Second)
			ch <- req
			if req != nil {
				_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: reply, Done: true})
			}
		}()
		raw, _ := json.Marshal(map[string]any{
			"model":    "gemini-4-hydrogen",
			"stream":   false,
			"tools":    tools,
			"messages": msgs,
		})
		httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(raw))
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httpReq)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return <-ch
	}

	msgs1 := []any{
		map[string]any{"role": "system", "content": "You are WorkBuddy."},
		map[string]any{"role": "user", "content": "check my chats"},
	}
	_ = runTurn(msgs1, "Which folder?")

	// Turn 2: user follow-up ("use local tools in workbuddy") -> delta prompt includes compact bridge reminder
	msgs2 := append(append([]any{}, msgs1...),
		map[string]any{"role": "assistant", "content": "Which folder?"},
		map[string]any{"role": "user", "content": "use local tools in workbuddy"},
	)
	req2 := runTurn(msgs2, "```tool_call\n{\"name\":\"list_dir\",\"arguments\":{}}\n```")
	if req2 == nil {
		t.Fatal("expected turn 2 bridge request")
	}
	if strings.Contains(req2.Prompt, "# Tool Use") {
		t.Errorf("turn 2 delta prompt should NOT repeat full # Tool Use block, got %q", req2.Prompt)
	}
	if !strings.Contains(req2.Prompt, "Local client bridge active") || !strings.Contains(req2.Prompt, "list_dir") {
		t.Errorf("turn 2 follow-up user turn must include compact local bridge tool reminder, got %q", req2.Prompt)
	}
}

func TestBridgeStreamingParsesJSONToolCallsAndFlushesNormalJSON(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeJitterMs = 0
	cfg.BridgeMaxPer10Min = 0
	app := New(cfg, "test")
	handler := app.Handler()

	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "list_dir", "description": "List directory"}},
	}

	runStream := func(tabID string, chunks []string) string {
		app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", tabID, "1")
		done := make(chan struct{})
		go func() {
			defer close(done)
			req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", tabID, "1", 2*time.Second)
			if req == nil {
				return
			}
			for i, c := range chunks {
				_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: c, Done: i == len(chunks)-1})
			}
		}()

		raw, _ := json.Marshal(map[string]any{
			"model":    "gemini-4-hydrogen",
			"stream":   true,
			"tools":    tools,
			"messages": []any{map[string]any{"role": "user", "content": "list files"}},
		})
		httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(raw))
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httpReq)
		<-done
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Case 1: Model uses ```json``` for a declared tool -> must emit finish_reason: tool_calls
	sseTool := runStream("tab_stream_tc", []string{
		"Checking folder.\n```js",
		"on\n{\"name\":\"list_dir\",\"arguments\":{\"path\":\".\"}}\n```",
	})
	if !strings.Contains(sseTool, `"finish_reason":"tool_calls"`) || !strings.Contains(sseTool, `"name":"list_dir"`) {
		t.Errorf("expected streaming SSE to parse declared ```json``` tool call, got: %s", sseTool)
	}

	// Case 2: Model outputs ordinary ```json``` config snippet -> must flush verbatim without dropping text
	ssePlain := runStream("tab_stream_json", []string{
		"Here is the config:\n```json\n",
		"{\"name\":\"my_project\",\"count\":3}\n```",
	})
	if strings.Contains(ssePlain, `"finish_reason":"tool_calls"`) {
		t.Errorf("ordinary ```json``` snippet must NOT emit tool_calls, got: %s", ssePlain)
	}
	if !strings.Contains(ssePlain, "my_project") || !strings.Contains(ssePlain, `"finish_reason":"stop"`) {
		t.Errorf("ordinary ```json``` snippet must be flushed intact with stop finish_reason, got: %s", ssePlain)
	}
}

func TestBridgeAutoNewChatSyncWhenClientStartsFreshConversation(t *testing.T) {
	tmpDir := t.TempDir()
	pdfPath := filepath.Join(tmpDir, "spec.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\n%fresh-chat-sync\n%%EOF\n"), 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeJitterMs = 0
	cfg.BridgeMaxPer10Min = 0
	cfg.BridgeDeltaContext = true
	cfg.BridgeAutoNewChat = true
	cfg.BridgeAutoUploadLocalFiles = true
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_newchat", "1")

	run := func(msgs []any) *bridge.Request {
		ch := make(chan *bridge.Request, 1)
		go func() {
			req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_newchat", "1", 2*time.Second)
			ch <- req
			if req != nil {
				_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "Done.", Done: true})
			}
		}()
		raw, _ := json.Marshal(map[string]any{
			"model":    "gemini-4-hydrogen",
			"stream":   false,
			"messages": msgs,
		})
		httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(raw))
		httpReq.RemoteAddr = "127.0.0.1:54321"
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httpReq)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return <-ch
	}

	// Chat 1 Turn 1: fresh tab -> NewChat=false, attaches spec.pdf
	c1t1 := run([]any{
		map[string]any{"role": "system", "content": "You are WorkBuddy."},
		map[string]any{"role": "user", "content": "Summarize " + pdfPath},
	})
	if c1t1.NewChat {
		t.Errorf("first turn on a clean tab should have NewChat=false")
	}
	if len(c1t1.Attachments) != 1 {
		t.Fatalf("chat 1 turn 1 expected 1 attachment, got %d", len(c1t1.Attachments))
	}

	// Chat 1 Turn 2: continuation -> NewChat=false, delta context, 0 duplicate attachments
	c1t2 := run([]any{
		map[string]any{"role": "system", "content": "You are WorkBuddy."},
		map[string]any{"role": "user", "content": "Summarize " + pdfPath},
		map[string]any{"role": "assistant", "content": "Done."},
		map[string]any{"role": "user", "content": "Check section 2 of " + pdfPath},
	})
	if c1t2.NewChat {
		t.Errorf("continuation turn in same chat must have NewChat=false")
	}
	if len(c1t2.Attachments) != 0 {
		t.Errorf("continuation turn must not re-upload spec.pdf, got %d", len(c1t2.Attachments))
	}

	// Chat 2 Turn 1 (User clicked "New Chat" in WorkBuddy!):
	// Must set NewChat=true, send full prompt, AND attach spec.pdf cleanly into the new Gemini chat!
	c2t1 := run([]any{
		map[string]any{"role": "system", "content": "You are WorkBuddy."},
		map[string]any{"role": "user", "content": "Brand new task using " + pdfPath},
	})
	if !c2t1.NewChat {
		t.Errorf("expected NewChat=true when WorkBuddy starts a brand-new conversation")
	}
	if !strings.Contains(c2t1.Prompt, "You are WorkBuddy.") {
		t.Errorf("expected full prompt on new conversation, got %q", c2t1.Prompt)
	}
	if len(c2t1.Attachments) != 1 {
		t.Fatalf("expected spec.pdf to be uploaded into the fresh Gemini chat, got %d attachments", len(c2t1.Attachments))
	}

	// Chat 2 Turn 2: continuation of Chat 2 -> NewChat=false, delta context works again!
	c2t2 := run([]any{
		map[string]any{"role": "system", "content": "You are WorkBuddy."},
		map[string]any{"role": "user", "content": "Brand new task using " + pdfPath},
		map[string]any{"role": "assistant", "content": "Done."},
		map[string]any{"role": "user", "content": "Follow up on Chat 2"},
	})
	if c2t2.NewChat {
		t.Errorf("turn 2 of chat 2 must have NewChat=false")
	}
	if strings.Contains(c2t2.Prompt, "You are WorkBuddy.") {
		t.Errorf("turn 2 of chat 2 should use delta context, got %q", c2t2.Prompt)
	}

	// Chat 3 Turn 1: Even if user types the EXACT same first prompt after a multi-turn chat,
	// dropping from 2 user turns back to 1 triggers NewChat=true!
	c3t1 := run([]any{
		map[string]any{"role": "system", "content": "You are WorkBuddy."},
		map[string]any{"role": "user", "content": "Brand new task using " + pdfPath},
	})
	if !c3t1.NewChat {
		t.Errorf("expected NewChat=true when dropping from multi-turn chat back to Turn 1")
	}
}



