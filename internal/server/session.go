package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

const (
	maxRollingSummaryChars = 16000
	minCompressionBudget   = 4000
	maxSessionIDLength     = 128
)

type sessionValidationError struct {
	msg string
}

func (e *sessionValidationError) Error() string {
	return e.msg
}

func isSessionValidationError(err error) bool {
	_, ok := err.(*sessionValidationError)
	return ok
}

type keyLockEntry struct {
	mu   sync.Mutex
	refs int
}

type ChatSession struct {
	Key            string
	Meta           gemini.SessionMeta
	SyncedCount    int
	TotalTokens    int
	RollingSummary string
	RolloverCount  int
	ToolsSent      bool
	ToolsHash      string
	UpdatedAt      time.Time
}

type SessionManager struct {
	mu       sync.Mutex
	cfg      config.Config
	sessions map[string]*ChatSession
	keyLocks map[string]*keyLockEntry
}

func NewSessionManager(cfg config.Config) *SessionManager {
	return &SessionManager{
		cfg:      cfg,
		sessions: make(map[string]*ChatSession),
		keyLocks: make(map[string]*keyLockEntry),
	}
}

type SessionPlan struct {
	Key             string
	Stateful        bool
	Meta            gemini.SessionMeta
	Prompt          string
	Images          []format.Image
	PromptTokens    int
	ConvMsgsLen     int
	HasTools        bool
	ToolsHash       string
	RolledOver      bool
	Compressed      bool
	RolloverCount   int
	DeltaMsgs       []models.OpenAIMessage
	PreviousSummary string

	systemMsgs []models.OpenAIMessage
	convMsgs   []models.OpenAIMessage
	chatReq    models.OpenAIChatRequest
	cfg        config.Config
}

func (p *SessionPlan) BuildRolloverPrompt() (string, []format.Image, int, error) {
	p.Meta = gemini.SessionMeta{}
	p.RolledOver = true
	p.RolloverCount++
	prompt, imgs, tokens, compressed, err := buildCompressedFullPrompt(
		p.systemMsgs,
		p.convMsgs,
		p.PreviousSummary,
		p.chatReq,
		p.cfg,
	)
	if err != nil {
		return "", nil, 0, err
	}
	if compressed {
		p.Compressed = true
	}
	p.Prompt = prompt
	p.Images = imgs
	p.PromptTokens = tokens
	return prompt, imgs, tokens, nil
}

func (m *SessionManager) cleanupExpiredLocked(now time.Time) {
	ttl := time.Duration(m.cfg.SessionTTLSec) * time.Second
	if ttl <= 0 {
		return
	}
	for k, s := range m.sessions {
		if now.Sub(s.UpdatedAt) > ttl {
			delete(m.sessions, k)
		}
	}
}

func (m *SessionManager) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, s := range m.sessions {
		if first || s.UpdatedAt.Before(oldestTime) {
			first = false
			oldestKey = k
			oldestTime = s.UpdatedAt
		}
	}
	if oldestKey != "" {
		delete(m.sessions, oldestKey)
	}
}

func extractCallerScope(r *http.Request) string {
	if r == nil {
		return ""
	}
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			if tok := strings.TrimSpace(auth[7:]); tok != "" {
				return "auth:" + hashValue(tok)
			}
		} else {
			return "auth:" + hashValue(auth)
		}
	}
	for _, h := range []string{"x-api-key", "x-goog-api-key"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return "auth:" + hashValue(v)
		}
	}
	if r.URL != nil {
		if q := strings.TrimSpace(r.URL.Query().Get("key")); q != "" {
			return "auth:" + hashValue(q)
		}
	}
	if cookie := strings.TrimSpace(r.Header.Get("Cookie")); cookie != "" {
		return "cookie:" + hashValue(cookie)
	}
	return ""
}

func validateSessionHeader(headerKey string) error {
	if len(headerKey) > maxSessionIDLength {
		return &sessionValidationError{
			msg: fmt.Sprintf("X-Session-Id exceeds maximum length of %d characters", maxSessionIDLength),
		}
	}
	for i := 0; i < len(headerKey); i++ {
		b := headerKey[i]
		if b < 0x20 || b == 0x7f {
			return &sessionValidationError{msg: "X-Session-Id contains invalid control characters"}
		}
	}
	return nil
}

func resolveSessionKey(r *http.Request, modelName string, convMsgs []models.OpenAIMessage, sysHash string, stateful bool) (string, string, error) {
	var headerKey string
	if r != nil {
		headerKey = strings.TrimSpace(r.Header.Get("X-Session-Id"))
		if headerKey == "" {
			headerKey = strings.TrimSpace(r.Header.Get("X-Conversation-Id"))
		}
	}
	if headerKey != "" {
		if err := validateSessionHeader(headerKey); err != nil {
			return "", "", err
		}
		if !stateful {
			return "", headerKey, nil
		}
		scope := extractCallerScope(r)
		if scope == "" {
			scope = "anon"
		}
		return "hdr:" + scope + ":" + headerKey, headerKey, nil
	}

	if !stateful || len(convMsgs) == 0 {
		return "", "", nil
	}
	// Require a caller credential/cookie scope for automatic multi-turn attachment
	// so independent unauthenticated callers with identical prompts never share threads.
	scope := extractCallerScope(r)
	if scope == "" {
		return "", "", nil
	}
	return "auto:" + hashValue([]any{scope, modelName, sysHash, convMsgs[0]}), "", nil
}

func (m *SessionManager) AcquireLock(r *http.Request, modelName string, chatReq models.OpenAIChatRequest, hasCookies bool) (func(), error) {
	systemMsgs, convMsgs := splitSystemAndConv(chatReq.Messages)
	sysHash := hashValue(systemMsgs)
	stateful := m.cfg.SessionEnabled && hasCookies

	key, _, err := resolveSessionKey(r, modelName, convMsgs, sysHash, stateful)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return func() {}, nil
	}

	m.mu.Lock()
	entry, ok := m.keyLocks[key]
	if !ok {
		entry = &keyLockEntry{}
		m.keyLocks[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(m.keyLocks, key)
		}
		m.mu.Unlock()
	}, nil
}

func (m *SessionManager) Prepare(r *http.Request, modelName string, chatReq models.OpenAIChatRequest, hasCookies bool) (*SessionPlan, error) {
	systemMsgs, convMsgs := splitSystemAndConv(chatReq.Messages)
	toolsHash := hashValue(chatReq.Tools)
	sysHash := hashValue(systemMsgs)

	stateful := m.cfg.SessionEnabled && hasCookies
	key, headerKey, err := resolveSessionKey(r, modelName, convMsgs, sysHash, stateful)
	if err != nil {
		return nil, err
	}

	var existing *ChatSession
	var delta []models.OpenAIMessage
	var meta gemini.SessionMeta
	var prevSummary string
	var rolloverCount int
	var rolledOver bool
	var compressed bool
	var toolsSent bool
	var prevToolsHash string
	var prevTokens int

	m.mu.Lock()
	m.cleanupExpiredLocked(time.Now())

	if stateful && key != "" {
		if headerKey != "" {
			if s, ok := m.sessions[key]; ok {
				existing = s
			}
		} else if len(convMsgs) > 1 {
			if s, ok := m.sessions[key]; ok {
				existing = s
			}
		}

		if existing != nil && !existing.Meta.IsZero() {
			if headerKey != "" && len(convMsgs) == 1 && existing.SyncedCount >= 1 {
				// Explicit X-Session-Id sending only the newest turn.
				delta = convMsgs
				meta = existing.Meta
			} else if len(convMsgs) > existing.SyncedCount {
				delta = convMsgs[existing.SyncedCount:]
				if len(delta) > 0 && delta[0].Role == "assistant" {
					delta = delta[1:]
				}
				if len(delta) > 0 {
					meta = existing.Meta
				}
			}
			if !meta.IsZero() {
				prevSummary = existing.RollingSummary
				rolloverCount = existing.RolloverCount
				toolsSent = existing.ToolsSent
				prevToolsHash = existing.ToolsHash
				prevTokens = existing.TotalTokens
			}
		}
	}
	m.mu.Unlock()

	maxCtx := m.cfg.MaxContextTokens
	if maxCtx <= 0 {
		maxCtx = 1000000
	}
	rolloverLimit := int(float64(maxCtx) * m.cfg.SessionRolloverRatio)
	compressLimit := int(float64(maxCtx) * m.cfg.CompressThresholdRatio)
	if rolloverLimit <= 0 {
		rolloverLimit = int(float64(maxCtx) * 0.85)
	}
	if compressLimit <= 0 || compressLimit >= rolloverLimit {
		compressLimit = int(float64(rolloverLimit) * 0.75)
	}

	var prompt string
	var images []format.Image
	var promptTokens int
	var deltaMsgs []models.OpenAIMessage

	if !meta.IsZero() && len(delta) > 0 {
		deltaMsgs = delta
		var toolsForTurn []models.OpenAITool
		if len(chatReq.Tools) > 0 && (!toolsSent || prevToolsHash != toolsHash) {
			toolsForTurn = chatReq.Tools
		}

		deltaReq := models.OpenAIChatRequest{
			Messages:   delta,
			Tools:      toolsForTurn,
			ToolChoice: chatReq.ToolChoice,
		}
		var err error
		prompt, images, err = format.MessagesToPrompt(deltaReq)
		if err != nil {
			return nil, err
		}
		promptTokens = estimateTokens(prompt)

		if prevTokens+promptTokens >= rolloverLimit {
			// Context near max Gemini Web limit -> open a new Gemini session with compressed history.
			meta = gemini.SessionMeta{}
			rolledOver = true
			rolloverCount++
			prompt, images, promptTokens, compressed, err = buildCompressedFullPrompt(
				systemMsgs,
				convMsgs,
				prevSummary,
				chatReq,
				m.cfg,
			)
			if err != nil {
				return nil, err
			}
		} else if prevTokens+promptTokens >= compressLimit || promptTokens >= compressLimit/2 {
			// Still inside active Gemini session -> compress delta so the turn fits safely.
			budget := rolloverLimit - prevTokens - rolloverLimit/10
			minBudget := rolloverLimit / 4
			if minBudget > minCompressionBudget {
				minBudget = minCompressionBudget
			}
			if budget < minBudget {
				budget = minBudget
			}
			// Ensure compression actually reduces oversized deltas below compressLimit.
			if maxTarget := compressLimit - prevTokens; maxTarget > 0 && budget > maxTarget {
				budget = maxTarget
			}
			compDelta, changed := compressMessagesForBudget(delta, m.cfg.KeepRecentMessages, budget)
			if changed {
				compressed = true
				deltaMsgs = compDelta
				deltaReq.Messages = compDelta
				prompt, images, err = format.MessagesToPrompt(deltaReq)
				if err != nil {
					return nil, err
				}
				promptTokens = estimateTokens(prompt)
			}
		}
	} else {
		deltaMsgs = convMsgs
		var err error
		prompt, images, promptTokens, compressed, err = buildCompressedFullPrompt(
			systemMsgs,
			convMsgs,
			prevSummary,
			chatReq,
			m.cfg,
		)
		if err != nil {
			return nil, err
		}
	}

	return &SessionPlan{
		Key:             key,
		Stateful:        stateful,
		Meta:            meta,
		Prompt:          prompt,
		Images:          images,
		PromptTokens:    promptTokens,
		ConvMsgsLen:     len(convMsgs),
		HasTools:        len(chatReq.Tools) > 0,
		ToolsHash:       toolsHash,
		RolledOver:      rolledOver,
		Compressed:      compressed,
		RolloverCount:   rolloverCount,
		DeltaMsgs:       deltaMsgs,
		PreviousSummary: prevSummary,
		systemMsgs:      systemMsgs,
		convMsgs:        convMsgs,
		chatReq:         chatReq,
		cfg:             m.cfg,
	}, nil
}

func (m *SessionManager) Commit(plan *SessionPlan, newMeta gemini.SessionMeta, assistantText string) {
	if plan == nil || !plan.Stateful || plan.Key == "" {
		return
	}

	outTokens := estimateTokens(assistantText)
	turnDigest := buildTurnDigest(plan.DeltaMsgs, assistantText)

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.cleanupExpiredLocked(now)

	sess, ok := m.sessions[plan.Key]
	if !ok {
		if m.cfg.MaxSessions > 0 {
			for len(m.sessions) >= m.cfg.MaxSessions {
				m.evictOldestLocked()
			}
		}
		sess = &ChatSession{Key: plan.Key}
		m.sessions[plan.Key] = sess
	}

	if !newMeta.IsZero() {
		sess.Meta = newMeta
	}
	if plan.RolledOver || plan.Meta.IsZero() {
		sess.TotalTokens = plan.PromptTokens + outTokens
	} else {
		sess.TotalTokens += plan.PromptTokens + outTokens
	}
	sess.RolloverCount = plan.RolloverCount
	sess.SyncedCount = plan.ConvMsgsLen
	sess.ToolsSent = plan.HasTools
	sess.ToolsHash = plan.ToolsHash
	sess.UpdatedAt = now

	if turnDigest != "" {
		if sess.RollingSummary != "" {
			sess.RollingSummary += "\n" + turnDigest
		} else {
			sess.RollingSummary = turnDigest
		}
		if len(sess.RollingSummary) > maxRollingSummaryChars {
			sess.RollingSummary = compactRollingSummary(sess.RollingSummary, maxRollingSummaryChars)
		}
	}
}

func buildCompressedFullPrompt(
	systemMsgs []models.OpenAIMessage,
	convMsgs []models.OpenAIMessage,
	prevSummary string,
	chatReq models.OpenAIChatRequest,
	cfg config.Config,
) (string, []format.Image, int, bool, error) {
	maxCtx := cfg.MaxContextTokens
	if maxCtx <= 0 {
		maxCtx = 1000000
	}
	rolloverLimit := int(float64(maxCtx) * cfg.SessionRolloverRatio)
	compressLimit := int(float64(maxCtx) * cfg.CompressThresholdRatio)
	if rolloverLimit <= 0 {
		rolloverLimit = int(float64(maxCtx) * 0.85)
	}
	if compressLimit <= 0 || compressLimit >= rolloverLimit {
		compressLimit = int(float64(rolloverLimit) * 0.75)
	}

	assemble := func(msgs []models.OpenAIMessage) (string, []format.Image, error) {
		var combined []models.OpenAIMessage
		combined = append(combined, systemMsgs...)
		if prevSummary != "" {
			combined = append(combined, models.OpenAIMessage{
				Role:    "system",
				Content: "Earlier session summary (compressed to preserve continuity):\n" + prevSummary,
			})
		}
		combined = append(combined, msgs...)
		req := models.OpenAIChatRequest{
			Messages:   combined,
			Tools:      chatReq.Tools,
			ToolChoice: chatReq.ToolChoice,
		}
		return format.MessagesToPrompt(req)
	}

	prompt, images, err := assemble(convMsgs)
	if err != nil {
		return "", nil, 0, false, err
	}
	tokens := estimateTokens(prompt)
	if tokens < compressLimit {
		return prompt, images, tokens, false, nil
	}

	// Calculate overhead of system + tools + summary so compressed convMsgs fit inside rolloverLimit.
	overheadPrompt, _, _ := assemble(nil)
	overheadTokens := estimateTokens(overheadPrompt)
	budget := rolloverLimit - overheadTokens - rolloverLimit/10
	minBudget := rolloverLimit / 4
	if minBudget > minCompressionBudget {
		minBudget = minCompressionBudget
	}
	if budget < minBudget {
		budget = minBudget
	}

	compMsgs, changed := compressMessagesForBudget(convMsgs, cfg.KeepRecentMessages, budget)
	if !changed {
		return prompt, images, tokens, false, nil
	}

	newPrompt, newImages, err := assemble(compMsgs)
	if err != nil {
		return "", nil, 0, false, err
	}
	return newPrompt, newImages, estimateTokens(newPrompt), true, nil
}

func compressMessagesForBudget(msgs []models.OpenAIMessage, keepRecent int, maxTokens int) ([]models.OpenAIMessage, bool) {
	if len(msgs) == 0 {
		return msgs, false
	}
	if keepRecent <= 0 {
		keepRecent = 6
	}
	if keepRecent > len(msgs) {
		keepRecent = len(msgs)
	}

	splitIdx := len(msgs) - keepRecent
	older := msgs[:splitIdx]
	recent := msgs[splitIdx:]

	changed := false
	var result []models.OpenAIMessage

	if len(older) > 0 {
		digest := summarizeMessagesList(older)
		if digest != "" {
			result = append(result, models.OpenAIMessage{
				Role:    "system",
				Content: "Compressed earlier turns in this session:\n" + digest,
			})
			changed = true
		}
	}

	// Reserve roughly 15% of budget for digest, distribute the rest across recent messages.
	recentBudgetChars := maxTokens * 4
	if len(result) > 0 {
		recentBudgetChars = (recentBudgetChars * 85) / 100
	}
	if recentBudgetChars < 400 {
		recentBudgetChars = 400
	}
	perMsgMaxChars := recentBudgetChars / len(recent)
	if perMsgMaxChars < 400 {
		perMsgMaxChars = 400
	}

	for _, msg := range recent {
		compMsg, msgChanged := truncateMessageContent(msg, perMsgMaxChars)
		if msgChanged {
			changed = true
		}
		result = append(result, compMsg)
	}

	return result, changed
}

func summarizeMessagesList(msgs []models.OpenAIMessage) string {
	var lines []string
	for _, m := range msgs {
		txt := extractMessagePreview(m, 220)
		if txt == "" && len(m.ToolCalls) == 0 {
			continue
		}
		role := m.Role
		if role == "" {
			role = "user"
		}
		if role == "tool" && m.Name != "" {
			role = "tool:" + m.Name
		}
		if len(m.ToolCalls) > 0 {
			var names []string
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			if txt != "" {
				txt += " "
			}
			txt += "[called: " + strings.Join(names, ", ") + "]"
		}
		lines = append(lines, fmt.Sprintf("- [%s]: %s", role, txt))
	}
	joined := strings.Join(lines, "\n")
	if len(joined) > maxRollingSummaryChars {
		return compactRollingSummary(joined, maxRollingSummaryChars)
	}
	return joined
}

func truncateMessageContent(msg models.OpenAIMessage, maxChars int) (models.OpenAIMessage, bool) {
	if maxChars <= 0 {
		return msg, false
	}
	if s, ok := msg.Content.(string); ok {
		if len(s) <= maxChars {
			return msg, false
		}
		msg.Content = headTailTruncate(s, maxChars)
		return msg, true
	}
	if parts, ok := msg.Content.([]any); ok {
		totalLen := 0
		for _, p := range parts {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					totalLen += len(t)
				}
			}
		}
		if totalLen <= maxChars {
			return msg, false
		}
		newParts := make([]any, 0, len(parts))
		remaining := maxChars
		for _, p := range parts {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && len(t) > remaining && remaining > 200 {
					copyMap := make(map[string]any, len(m))
					for k, v := range m {
						copyMap[k] = v
					}
					copyMap["text"] = headTailTruncate(t, remaining)
					newParts = append(newParts, copyMap)
					remaining = 0
					continue
				} else if ok {
					if remaining > len(t) {
						remaining -= len(t)
					} else {
						remaining = 0
					}
				}
			}
			newParts = append(newParts, p)
		}
		msg.Content = newParts
		return msg, true
	}
	return msg, false
}

func headTailTruncate(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	if maxChars < 200 {
		return s[:maxChars]
	}
	headLen := (maxChars * 6) / 10
	tailLen := (maxChars * 3) / 10
	dropped := len(s) - headLen - tailLen
	return fmt.Sprintf("%s\n... [compressed %d chars to fit session context] ...\n%s", s[:headLen], dropped, s[len(s)-tailLen:])
}

func buildTurnDigest(delta []models.OpenAIMessage, assistantReply string) string {
	var parts []string
	for _, m := range delta {
		p := extractMessagePreview(m, 160)
		if p != "" {
			role := m.Role
			if role == "" {
				role = "user"
			}
			if role == "tool" && m.Name != "" {
				role = "tool:" + m.Name
			}
			parts = append(parts, fmt.Sprintf("[%s] %s", role, p))
		}
	}
	if replyPreview := compactWhitespace(assistantReply, 180); replyPreview != "" {
		parts = append(parts, fmt.Sprintf("[assistant] %s", replyPreview))
	}
	return strings.Join(parts, " | ")
}

func compactRollingSummary(summary string, maxChars int) string {
	if len(summary) <= maxChars {
		return summary
	}
	trimmed := summary[len(summary)-maxChars:]
	if idx := strings.Index(trimmed, "\n"); idx != -1 && idx+1 < len(trimmed) {
		trimmed = trimmed[idx+1:]
	}
	return "[earlier turns omitted]\n" + trimmed
}

func extractMessagePreview(msg models.OpenAIMessage, maxChars int) string {
	var raw string
	switch c := msg.Content.(type) {
	case string:
		raw = c
	case []any:
		var chunks []string
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && t != "" {
					chunks = append(chunks, t)
				}
			} else if s, ok := item.(string); ok && s != "" {
				chunks = append(chunks, s)
			}
		}
		raw = strings.Join(chunks, " ")
	}
	return compactWhitespace(raw, maxChars)
}

func compactWhitespace(s string, maxChars int) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	joined := strings.Join(fields, " ")
	if len(joined) > maxChars {
		return joined[:maxChars] + "..."
	}
	return joined
}

func splitSystemAndConv(msgs []models.OpenAIMessage) ([]models.OpenAIMessage, []models.OpenAIMessage) {
	var sys, conv []models.OpenAIMessage
	for _, m := range msgs {
		if m.Role == "system" {
			sys = append(sys, m)
		} else {
			conv = append(conv, m)
		}
	}
	return sys, conv
}

// estimateTokens returns a rough character-based token estimate (~4 chars/token).
// Used for proxy usage reporting, /v1/messages/count_tokens, and context-budget heuristics.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

func hashValue(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

func isSessionContextError(err error) bool {
	if err == nil {
		return false
	}
	ue, ok := err.(*gemini.UpstreamError)
	if !ok {
		return false
	}
	if ue.Kind == "bard" {
		return true
	}
	if ue.Kind == "http" {
		return ue.Status == http.StatusBadRequest ||
			ue.Status == http.StatusRequestEntityTooLarge ||
			ue.Status == http.StatusInternalServerError
	}
	return false
}

func setSessionHeaders(w http.ResponseWriter, plan *SessionPlan, newMeta gemini.SessionMeta) {
	if newMeta.CID != "" {
		w.Header().Set("X-Gemini-Session-Id", newMeta.CID)
	}
	if plan != nil {
		if plan.RolledOver {
			w.Header().Set("X-Gemini-Session-Rollover", "true")
		}
		if plan.Compressed {
			w.Header().Set("X-Gemini-Context-Compressed", "true")
		}
	}
}

func (a *App) hasActiveCookies() bool {
	if a.Gem == nil || a.Gem.Cookies == nil {
		return false
	}
	info, err := a.Gem.Cookies.Load()
	return err == nil && info.Cookie != ""
}

func (a *App) generateWithSession(w http.ResponseWriter, r *http.Request, resolved models.Resolved, chatReq models.OpenAIChatRequest) (string, *SessionPlan, error) {
	release, err := a.Sessions.AcquireLock(r, resolved.Name, chatReq, a.hasActiveCookies())
	if err != nil {
		return "", nil, err
	}
	defer release()

	plan, err := a.Sessions.Prepare(r, resolved.Name, chatReq, a.hasActiveCookies())
	if err != nil {
		return "", nil, err
	}

	fileRefs, err := a.uploadImages(plan.Images)
	if err != nil {
		return "", nil, err
	}

	gemOpt := gemini.ModelOptions{
		ModelName:   resolved.Name,
		ModelID:     resolved.Mode,
		ThinkMode:   resolved.Think,
		ModelHeader: resolved.Header,
		Extra:       resolved.Extra,
	}

	text, newMeta, err := a.Gem.GenerateWithMeta(plan.Prompt, gemOpt, fileRefs, plan.Meta)
	if !plan.Meta.IsZero() && (isSessionContextError(err) || (err == nil && strings.TrimSpace(text) == "")) {
		a.Logf("Gemini session %s triggered reactive rollover (err=%v); starting fresh session with compressed context", plan.Meta.CID, err)
		rollPrompt, rollImgs, _, rollErr := plan.BuildRolloverPrompt()
		if rollErr == nil {
			rollRefs := fileRefs
			if len(rollImgs) > 0 {
				if uploaded, upErr := a.uploadImages(rollImgs); upErr == nil {
					rollRefs = uploaded
				}
			}
			text, newMeta, err = a.Gem.GenerateWithMeta(rollPrompt, gemOpt, rollRefs, gemini.SessionMeta{})
		}
	}
	if err != nil {
		return "", nil, err
	}

	a.Sessions.Commit(plan, newMeta, text)
	setSessionHeaders(w, plan, newMeta)
	return text, plan, nil
}

func (a *App) streamWithSession(
	w http.ResponseWriter,
	r *http.Request,
	resolved models.Resolved,
	chatReq models.OpenAIChatRequest,
	onStart func(plan *SessionPlan) bool,
	onDelta func(delta string) error,
) (*SessionPlan, bool, error) {
	release, err := a.Sessions.AcquireLock(r, resolved.Name, chatReq, a.hasActiveCookies())
	if err != nil {
		return nil, false, err
	}
	defer release()

	plan, err := a.Sessions.Prepare(r, resolved.Name, chatReq, a.hasActiveCookies())
	if err != nil {
		return nil, false, err
	}

	fileRefs, err := a.uploadImages(plan.Images)
	if err != nil {
		return nil, false, err
	}

	gemOpt := gemini.ModelOptions{
		ModelName:   resolved.Name,
		ModelID:     resolved.Mode,
		ThinkMode:   resolved.Think,
		ModelHeader: resolved.Header,
		Extra:       resolved.Extra,
	}

	var started bool
	var fullBuf strings.Builder

	emit := func(delta string) error {
		if !started {
			setSessionHeaders(w, plan, plan.Meta)
			if !onStart(plan) {
				return fmt.Errorf("streaming unsupported")
			}
			started = true
		}
		fullBuf.WriteString(delta)
		return onDelta(delta)
	}

	newMeta, err := a.Gem.GenerateStreamWithMeta(plan.Prompt, gemOpt, fileRefs, plan.Meta, emit)
	if !started && !plan.Meta.IsZero() && (isSessionContextError(err) || (err == nil && fullBuf.Len() == 0)) {
		a.Logf("Gemini stream session %s triggered reactive rollover (err=%v); starting fresh session with compressed context", plan.Meta.CID, err)
		rollPrompt, rollImgs, _, rollErr := plan.BuildRolloverPrompt()
		if rollErr == nil {
			rollRefs := fileRefs
			if len(rollImgs) > 0 {
				if uploaded, upErr := a.uploadImages(rollImgs); upErr == nil {
					rollRefs = uploaded
				}
			}
			fullBuf.Reset()
			newMeta, err = a.Gem.GenerateStreamWithMeta(rollPrompt, gemOpt, rollRefs, gemini.SessionMeta{}, emit)
		}
	}
	if err != nil {
		return plan, started, err
	}

	if !started {
		setSessionHeaders(w, plan, newMeta)
		if !onStart(plan) {
			return plan, false, fmt.Errorf("streaming unsupported")
		}
		started = true
	}

	a.Sessions.Commit(plan, newMeta, fullBuf.String())
	return plan, started, nil
}
