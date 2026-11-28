package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

type stubGeminiRequester struct {
	reply string
}

func (s *stubGeminiRequester) Do(req *http.Request) (*http.Response, error) {
	innerJSON, _ := json.Marshal([]any{
		nil, nil, nil, nil,
		[]any{
			[]any{"rc_123", []any{s.reply}},
		},
		nil, nil, nil, nil, nil,
		"padding_to_exceed_50_chars_for_parser_threshold_check",
	})
	outerJSON, _ := json.Marshal([]any{
		[]any{"wrb.fr", nil, string(innerJSON), nil, nil, nil, "generic"},
	})
	body := ")]}'\n\n" + string(outerJSON) + strings.Repeat(" ", 200) + "\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func newTestMessagesApp(reply string) *App {
	cfg := config.Default()
	cfg.APIKeys = []string{"sk-test"}
	cfg.RetryAttempts = 1
	app := New(cfg, "test")
	app.Gem.HTTP = &stubGeminiRequester{reply: reply}
	return app
}

func TestMessagesNonStreamingTextAndToolUse(t *testing.T) {
	// 1. Plain text response
	app := newTestMessagesApp("Hello from Gemini!")
	handler := app.Handler()

	reqBody := `{"model":"gemini-3.8-flash","max_tokens":100,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("x-api-key", "sk-test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["type"] != "message" || resp["role"] != "assistant" || resp["stop_reason"] != "end_turn" {
		t.Errorf("unexpected message envelope: %v", resp)
	}
	content := resp["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "Hello from Gemini!" {
		t.Errorf("unexpected content blocks: %v", content)
	}

	// 2. Tool use response
	toolReply := "Checking now.\n```tool_call\n{\"name\": \"Read\", \"arguments\": {\"file_path\": \"main.go\"}}\n```"
	appTool := newTestMessagesApp(toolReply)
	toolBody := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"tools": [{"name": "Read", "description": "Read file", "input_schema": {"type": "object"}}],
		"messages": [{"role": "user", "content": "Read main.go"}]
	}`
	req2 := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(toolBody))
	req2.Header.Set("x-api-key", "sk-test")
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	appTool.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2["stop_reason"] != "tool_use" {
		t.Errorf("expected stop_reason=tool_use, got %v", resp2["stop_reason"])
	}
	blocks := resp2["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (text + tool_use), got %d: %v", len(blocks), blocks)
	}
	tu := blocks[1].(map[string]any)
	if tu["type"] != "tool_use" || tu["name"] != "Read" {
		t.Errorf("unexpected tool_use block: %v", tu)
	}
	if !strings.HasPrefix(tu["id"].(string), "toolu_") {
		t.Errorf("expected toolu_ prefix id, got %v", tu["id"])
	}
	inputMap := tu["input"].(map[string]any)
	if inputMap["file_path"] != "main.go" {
		t.Errorf("unexpected tool_use input: %v", inputMap)
	}
}

func TestMessagesStreamingSSEAndCountTokens(t *testing.T) {
	// Live text stream
	app := newTestMessagesApp("Streamed hello")
	reqBody := `{"model":"gemini-3.8-flash","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	sse := rec.Body.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		`"text_delta"`,
		"Streamed hello",
		"event: content_block_stop",
		"event: message_delta",
		`"end_turn"`,
		"event: message_stop",
	} {
		if !strings.Contains(sse, want) {
			t.Errorf("SSE missing %q\nSSE:\n%s", want, sse)
		}
	}

	// Tool stream
	toolReply := "```tool_call\n{\"name\": \"Bash\", \"arguments\": {\"command\": \"ls\"}}\n```"
	appTool := newTestMessagesApp(toolReply)
	toolBody := `{"model":"gemini-3.8-flash","stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"list"}]}`
	reqTool := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(toolBody))
	reqTool.Header.Set("x-api-key", "sk-test")
	recTool := httptest.NewRecorder()
	appTool.Handler().ServeHTTP(recTool, reqTool)

	if recTool.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recTool.Code)
	}
	sseTool := recTool.Body.String()
	for _, want := range []string{
		`"type":"tool_use"`,
		`"input_json_delta"`,
		`"stop_reason":"tool_use"`,
		"event: message_stop",
	} {
		if !strings.Contains(sseTool, want) {
			t.Errorf("Tool SSE missing %q\nSSE:\n%s", want, sseTool)
		}
	}

	// Count tokens endpoint
	reqCount := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(reqBody))
	reqCount.Header.Set("x-api-key", "sk-test")
	recCount := httptest.NewRecorder()
	app.Handler().ServeHTTP(recCount, reqCount)
	if recCount.Code != http.StatusOK {
		t.Fatalf("count_tokens expected 200, got %d", recCount.Code)
	}
	var countResp map[string]any
	_ = json.Unmarshal(recCount.Body.Bytes(), &countResp)
	if _, ok := countResp["input_tokens"]; !ok {
		t.Errorf("count_tokens missing input_tokens: %v", countResp)
	}
}

func TestMessagesSessionContinuation(t *testing.T) {
	app, mock := newSessionTestApp(t, nil)
	handler := app.Handler()

	req1 := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"gemini-3.8-flash","system":"Be concise","messages":[{"role":"user","content":"Hello"}]}`))
	req1.Header.Set("x-api-key", "sk-test")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("turn 1 failed: %d", rec1.Code)
	}
	cid1 := rec1.Header().Get("X-Gemini-Session-Id")

	req2 := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{
		"model":"gemini-3.8-flash",
		"system":"Be concise",
		"messages":[
			{"role":"user","content":"Hello"},
			{"role":"assistant","content":"Reply #1"},
			{"role":"user","content":"Follow up"}
		]
	}`))
	req2.Header.Set("x-api-key", "sk-test")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("turn 2 failed: %d", rec2.Code)
	}
	if len(mock.calls) != 2 || mock.calls[1].cid != cid1 {
		t.Errorf("expected turn 2 to reuse cid %q, got calls=%+v", cid1, mock.calls)
	}
}
