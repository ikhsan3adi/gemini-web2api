package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
)

type recordedCall struct {
	prompt string
	cid    string
	rid    string
	rcid   string
}

type sessionMockRequester struct {
	mu            sync.Mutex
	calls         []recordedCall
	failNextWith  int
	nextCIDPrefix string
	turnCounter   int
	delay         time.Duration
}

func (m *sessionMockRequester) Do(req *http.Request) (*http.Response, error) {
	if !strings.Contains(req.URL.Path, "StreamGenerate") {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	}

	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
	}
	form, _ := url.ParseQuery(string(bodyBytes))
	fReq := form.Get("f.req")

	var outer []any
	_ = json.Unmarshal([]byte(fReq), &outer)
	var inner []any
	if len(outer) > 1 {
		if s, ok := outer[1].(string); ok {
			_ = json.Unmarshal([]byte(s), &inner)
		}
	}

	var prompt string
	if len(inner) > 0 {
		if arr, ok := inner[0].([]any); ok && len(arr) > 0 {
			prompt, _ = arr[0].(string)
		}
	}

	var cid, rid, rcid string
	if len(inner) > 2 {
		if arr, ok := inner[2].([]any); ok && len(arr) >= 3 {
			cid, _ = arr[0].(string)
			rid, _ = arr[1].(string)
			rcid, _ = arr[2].(string)
		}
	}

	m.mu.Lock()
	delay := m.delay
	m.calls = append(m.calls, recordedCall{
		prompt: prompt,
		cid:    cid,
		rid:    rid,
		rcid:   rcid,
	})

	if m.failNextWith != 0 {
		status := m.failNextWith
		m.failNextWith = 0
		m.mu.Unlock()
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader("error")),
			Header:     make(http.Header),
		}, nil
	}

	m.turnCounter++
	turnNum := m.turnCounter
	prefix := m.nextCIDPrefix
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	outCID := cid
	if outCID == "" {
		if prefix == "" {
			prefix = "c_sess"
		}
		outCID = prefix + "_" + strconv.Itoa(turnNum)
	}
	outRID := "r_" + strconv.Itoa(turnNum)
	outRCID := "rc_" + strconv.Itoa(turnNum)

	innerResp, _ := json.Marshal([]any{
		nil,
		[]any{outCID, outRID},
		nil, nil,
		[]any{
			[]any{outRCID, []any{"Reply #" + strconv.Itoa(turnNum)}},
		},
		nil, nil, nil, nil, nil,
		"padding_to_exceed_50_chars_for_parser_threshold_check",
	})
	outerResp, _ := json.Marshal([]any{
		[]any{"wrb.fr", nil, string(innerResp), nil, nil, nil, "generic"},
	})
	respBody := ")]}'\n\n" + string(outerResp) + strings.Repeat(" ", 200) + "\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(respBody)),
		Header:     make(http.Header),
	}, nil
}

func newSessionTestApp(t *testing.T, mutator func(*config.Config)) (*App, *sessionMockRequester) {
	t.Helper()
	tmpDir := t.TempDir()
	cookiePath := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookiePath, []byte(`[{"domain":".google.com","name":"SAPISID","value":"sap123"}]`), 0600); err != nil {
		t.Fatalf("write temp cookie: %v", err)
	}

	cfg := config.Default()
	cfg.CookieFile = cookiePath
	cfg.RetryAttempts = 1
	cfg.APIKeys = []string{"sk-test"}
	if mutator != nil {
		mutator(&cfg)
	}

	app := New(cfg, "test")
	mock := &sessionMockRequester{}
	app.Gem.HTTP = mock
	app.Gem.Cookies = gemini.NewCookieCache(cookiePath)
	return app, mock
}

func TestSessionContinuation(t *testing.T) {
	app, mock := newSessionTestApp(t, nil)
	handler := app.Handler()

	// Turn 1: starts new Gemini session
	body1 := `{"model":"gemini-3.8-flash","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"My name is Huy"}]}`
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body1))
	req1.Header.Set("Authorization", "Bearer sk-test")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("Turn 1 expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}
	cid1 := rec1.Header().Get("X-Gemini-Session-Id")
	if cid1 == "" {
		t.Fatal("Turn 1 missing X-Gemini-Session-Id header")
	}

	// Turn 2: full history sent by client -> should reuse cid1 and send ONLY delta
	body2 := `{
		"model":"gemini-3.8-flash",
		"messages":[
			{"role":"system","content":"Be concise"},
			{"role":"user","content":"My name is Huy"},
			{"role":"assistant","content":"Reply #1"},
			{"role":"user","content":"What is my name?"}
		]
	}`
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body2))
	req2.Header.Set("Authorization", "Bearer sk-test")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("Turn 2 expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", len(mock.calls))
	}
	if mock.calls[1].cid != cid1 {
		t.Errorf("Turn 2 cid = %q, want %q", mock.calls[1].cid, cid1)
	}
	if strings.Contains(mock.calls[1].prompt, "My name is Huy") {
		t.Errorf("Turn 2 should send delta only, but prompt included old turn: %q", mock.calls[1].prompt)
	}
	if !strings.Contains(mock.calls[1].prompt, "What is my name?") {
		t.Errorf("Turn 2 prompt missing new message: %q", mock.calls[1].prompt)
	}
}

func TestInSessionCompressionAndProactiveRollover(t *testing.T) {
	// MaxContextTokens = 2000, Compress = 0.50 (1000 tokens), Rollover = 0.85 (1700 tokens)
	app, mock := newSessionTestApp(t, func(cfg *config.Config) {
		cfg.MaxContextTokens = 2000
		cfg.CompressThresholdRatio = 0.50
		cfg.SessionRolloverRatio = 0.85
		cfg.KeepRecentMessages = 2
	})
	handler := app.Handler()

	// Turn 1: small turn (~50 tokens)
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{
		"model":"gemini-3.8-flash",
		"messages":[{"role":"user","content":"Start session"}]
	}`))
	req1.Header.Set("Authorization", "Bearer sk-test")
	req1.Header.Set("X-Session-Id", "work-1")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("Turn 1 failed: %d %s", rec1.Code, rec1.Body.String())
	}
	cid1 := rec1.Header().Get("X-Gemini-Session-Id")

	// Turn 2: large delta (~1200 tokens = 4800 chars) -> triggers IN-SESSION compression without rollover!
	bigPayload := strings.Repeat("alpha ", 800) // ~4800 chars -> ~1200 tokens > compressLimit (1000), < rolloverLimit (1700)
	req2Body, _ := json.Marshal(map[string]any{
		"model": "gemini-3.8-flash",
		"messages": []map[string]any{
			{"role": "user", "content": bigPayload},
		},
	})
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(req2Body)))
	req2.Header.Set("Authorization", "Bearer sk-test")
	req2.Header.Set("X-Session-Id", "work-1")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("Turn 2 failed: %d %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("X-Gemini-Context-Compressed") != "true" {
		t.Errorf("expected X-Gemini-Context-Compressed=true on Turn 2")
	}
	if rec2.Header().Get("X-Gemini-Session-Rollover") != "" {
		t.Errorf("did not expect rollover yet on Turn 2")
	}
	if mock.calls[1].cid != cid1 {
		t.Errorf("Turn 2 should stay in cid %q, got %q", cid1, mock.calls[1].cid)
	}

	// Turn 3: pushes cumulative tokens over rolloverLimit (1700) -> proactive rollover to NEW session!
	req3Body, _ := json.Marshal(map[string]any{
		"model": "gemini-3.8-flash",
		"messages": []map[string]any{
			{"role": "user", "content": strings.Repeat("beta ", 800)},
		},
	})
	req3 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(req3Body)))
	req3.Header.Set("Authorization", "Bearer sk-test")
	req3.Header.Set("X-Session-Id", "work-1")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("Turn 3 failed: %d %s", rec3.Code, rec3.Body.String())
	}
	if rec3.Header().Get("X-Gemini-Session-Rollover") != "true" {
		t.Errorf("expected X-Gemini-Session-Rollover=true on Turn 3")
	}
	if mock.calls[2].cid != "" {
		t.Errorf("Turn 3 rollover call should send empty cid to start new Gemini thread, got %q", mock.calls[2].cid)
	}
	if !strings.Contains(mock.calls[2].prompt, "Earlier session summary") {
		t.Errorf("Turn 3 rollover prompt should carry earlier session summary, got: %q", mock.calls[2].prompt)
	}
}

func TestReactiveSessionRolloverOnUpstream400(t *testing.T) {
	app, mock := newSessionTestApp(t, nil)
	handler := app.Handler()

	// Turn 1 succeeds and establishes cid
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{
		"model":"gemini-3.8-flash",
		"messages":[{"role":"user","content":"Hello"}]
	}`))
	req1.Header.Set("Authorization", "Bearer sk-test")
	req1.Header.Set("X-Session-Id", "reactive-1")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("Turn 1 failed: %d", rec1.Code)
	}

	// Simulate Gemini upstream returning 400 on continuing the old session
	mock.failNextWith = http.StatusBadRequest

	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{
		"model":"gemini-3.8-flash",
		"messages":[{"role":"user","content":"Continue work"}]
	}`))
	req2.Header.Set("Authorization", "Bearer sk-test")
	req2.Header.Set("X-Session-Id", "reactive-1")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("Turn 2 expected 200 after reactive rollover, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("X-Gemini-Session-Rollover") != "true" {
		t.Errorf("expected X-Gemini-Session-Rollover=true after reactive rollover")
	}
	// Call 0 = turn 1, Call 1 = failed turn 2 with old cid, Call 2 = retried turn 2 with empty cid
	if len(mock.calls) != 3 {
		t.Fatalf("expected 3 upstream calls, got %d", len(mock.calls))
	}
	if mock.calls[1].cid == "" || mock.calls[2].cid != "" {
		t.Errorf("expected call 1 with cid and call 2 with empty cid; got call1=%q call2=%q", mock.calls[1].cid, mock.calls[2].cid)
	}
}

func TestAutoSessionKeyIsolatedByCallerScope(t *testing.T) {
	app, mock := newSessionTestApp(t, func(cfg *config.Config) {
		cfg.APIKeys = []string{"sk-caller-a", "sk-caller-b"}
	})
	handler := app.Handler()

	// Caller A starts a conversation with a common system + first message.
	turn1Body := `{"model":"gemini-3.8-flash","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"Hello"}]}`
	reqA1 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(turn1Body))
	reqA1.Header.Set("Authorization", "Bearer sk-caller-a")
	recA1 := httptest.NewRecorder()
	handler.ServeHTTP(recA1, reqA1)
	if recA1.Code != http.StatusOK {
		t.Fatalf("Caller A turn 1 failed: %d", recA1.Code)
	}
	cidA := recA1.Header().Get("X-Gemini-Session-Id")

	// Caller B sends a 2-turn payload with identical system + first user message.
	// It MUST NOT attach to Caller A's upstream session.
	turn2Body := `{
		"model":"gemini-3.8-flash",
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":"Hello"},
			{"role":"assistant","content":"Reply #1"},
			{"role":"user","content":"Secret for B"}
		]
	}`
	reqB2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(turn2Body))
	reqB2.Header.Set("Authorization", "Bearer sk-caller-b")
	recB2 := httptest.NewRecorder()
	handler.ServeHTTP(recB2, reqB2)
	if recB2.Code != http.StatusOK {
		t.Fatalf("Caller B turn 2 failed: %d", recB2.Code)
	}
	cidB := recB2.Header().Get("X-Gemini-Session-Id")

	if cidA == "" || cidB == "" || cidA == cidB {
		t.Fatalf("expected distinct sessions for different callers; got cidA=%q cidB=%q", cidA, cidB)
	}
	if mock.calls[1].cid != "" {
		t.Errorf("Caller B should start a fresh session (empty cid), got %q", mock.calls[1].cid)
	}

	// Unauthenticated caller (api_keys empty, no credentials) must stay stateless for auto-attach.
	appAnon, mockAnon := newSessionTestApp(t, func(cfg *config.Config) {
		cfg.APIKeys = nil
	})
	recAnon1 := httptest.NewRecorder()
	appAnon.Handler().ServeHTTP(recAnon1, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(turn1Body)))
	recAnon2 := httptest.NewRecorder()
	appAnon.Handler().ServeHTTP(recAnon2, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(turn2Body)))
	if len(appAnon.Sessions.sessions) != 0 {
		t.Errorf("expected 0 stored sessions for unauthenticated auto requests, got %d", len(appAnon.Sessions.sessions))
	}
	if len(mockAnon.calls) != 2 || mockAnon.calls[1].cid != "" {
		t.Errorf("expected unauthenticated second call to remain stateless, got cid=%q", mockAnon.calls[1].cid)
	}
}

func TestSessionMapEvictsOldestAtMaxSessions(t *testing.T) {
	app, _ := newSessionTestApp(t, func(cfg *config.Config) {
		cfg.MaxSessions = 2
	})
	handler := app.Handler()

	sendWithID := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{
			"model":"gemini-3.8-flash",
			"messages":[{"role":"user","content":"Hi"}]
		}`))
		req.Header.Set("Authorization", "Bearer sk-test")
		req.Header.Set("X-Session-Id", id)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := sendWithID("s1"); rec.Code != http.StatusOK {
		t.Fatalf("s1 failed: %d", rec.Code)
	}
	time.Sleep(2 * time.Millisecond)
	if rec := sendWithID("s2"); rec.Code != http.StatusOK {
		t.Fatalf("s2 failed: %d", rec.Code)
	}
	time.Sleep(2 * time.Millisecond)
	if rec := sendWithID("s3"); rec.Code != http.StatusOK {
		t.Fatalf("s3 failed: %d", rec.Code)
	}

	app.Sessions.mu.Lock()
	count := len(app.Sessions.sessions)
	hasS1 := false
	hasS2 := false
	hasS3 := false
	for k := range app.Sessions.sessions {
		switch {
		case strings.HasSuffix(k, ":s1"):
			hasS1 = true
		case strings.HasSuffix(k, ":s2"):
			hasS2 = true
		case strings.HasSuffix(k, ":s3"):
			hasS3 = true
		}
	}
	app.Sessions.mu.Unlock()

	if count != 2 {
		t.Fatalf("expected session map size capped at 2, got %d", count)
	}
	if hasS1 || !hasS2 || !hasS3 {
		t.Errorf("expected oldest session s1 evicted and s2/s3 kept; hasS1=%v hasS2=%v hasS3=%v", hasS1, hasS2, hasS3)
	}

	// Reject oversized X-Session-Id with 400 Bad Request.
	oversized := strings.Repeat("x", maxSessionIDLength+1)
	if rec := sendWithID(oversized); rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for oversized X-Session-Id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestConcurrentRequestsSerializePerSessionKey(t *testing.T) {
	app, mock := newSessionTestApp(t, nil)
	mock.delay = 20 * time.Millisecond
	handler := app.Handler()

	// Seed session with turn 1
	seedReq := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{
		"model":"gemini-3.8-flash",
		"messages":[{"role":"user","content":"Turn 1"}]
	}`))
	seedReq.Header.Set("Authorization", "Bearer sk-test")
	seedReq.Header.Set("X-Session-Id", "concurrent-1")
	seedRec := httptest.NewRecorder()
	handler.ServeHTTP(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed failed: %d", seedRec.Code)
	}

	var wg sync.WaitGroup
	errs := make(chan string, 2)
	for _, msg := range []string{"Turn 2A", "Turn 2B"} {
		wg.Add(1)
		go func(content string) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{
				"model":"gemini-3.8-flash",
				"messages":[{"role":"user","content":"`+content+`"}]
			}`))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Session-Id", "concurrent-1")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				errs <- rec.Body.String()
			}
		}(msg)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent request failed: %s", e)
	}

	mock.mu.Lock()
	calls := append([]recordedCall(nil), mock.calls...)
	mock.mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	// Because requests on the same key serialize, call 2 must observe the updated RID from call 1 (r_2, rc_2), not r_1!
	if calls[1].rid != "r_1" || calls[2].rid != "r_2" {
		t.Errorf("expected serialized RID progression r_1 -> r_2, got call[1].rid=%q call[2].rid=%q", calls[1].rid, calls[2].rid)
	}
}
