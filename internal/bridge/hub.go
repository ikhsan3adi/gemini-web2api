package bridge

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Attachment struct {
	Filename   string `json:"filename"`
	MIME       string `json:"mime"`
	DataBase64 string `json:"data_base64"`
	Size       int    `json:"size,omitempty"`
	Source     string `json:"source,omitempty"`
}

type Request struct {
	ID          string       `json:"id"`
	Prompt      string       `json:"prompt"`
	Model       string       `json:"model,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

type GenerateOptions struct {
	Prompt      string
	Model       string
	Attachments []Attachment
}

type Chunk struct {
	ID          string `json:"id"`
	Text        string `json:"text,omitempty"`
	Done        bool   `json:"done,omitempty"`
	Error       string `json:"error,omitempty"`
	RateLimited bool   `json:"rate_limited,omitempty"`
}

// RateLimitError is returned when the bridge circuit breaker is cooling down
// after Google signaled HTTP 429 / quota / unusual traffic.
type RateLimitError struct {
	RetryAfter time.Duration
	Cause      string
}

func (e *RateLimitError) Error() string {
	secs := int(e.RetryAfter.Round(time.Second).Seconds())
	if secs < 1 {
		secs = 1
	}
	if e.Cause != "" {
		return fmt.Sprintf("bridge rate-limit cooldown active (%s); retry after %ds", e.Cause, secs)
	}
	return fmt.Sprintf("bridge rate-limit cooldown active; retry after %ds", secs)
}

func looksLikeRateLimitError(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "http 429") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "quota exceeded") ||
		strings.Contains(lower, "resource_exhausted") ||
		strings.Contains(lower, "unusual traffic")
}

type inFlightEntry struct {
	req        *Request
	tabName    string
	clientID   string
	assignedAt time.Time
	hasChunks  bool
}

type tabSession struct {
	convGen       string
	fingerprints  []string
	uploadedFiles map[string]string // canonical path or "sha256:..." -> signature
}

type Hub struct {
	mu                    sync.Mutex
	seq                   atomic.Uint64
	tabName               string
	lastHeartbeat         time.Time
	clientHeartbeats      map[string]time.Time
	pending               []*Request
	inFlight              map[string]*inFlightEntry
	waiters               map[string]chan Chunk
	pollNotify            chan struct{}
	minInterval           time.Duration
	jitterMax             time.Duration
	maxPer10Min           int
	recentDispatches      []time.Time
	lastCompletedAt       time.Time
	cooldownUntil         time.Time
	lastRateLimitCause    string
	consecutiveRateLimits int
	activeClientID        string
	activeConvGen         string
	sessions              map[string]*tabSession
}

func NewHub() *Hub {
	return &Hub{
		clientHeartbeats: make(map[string]time.Time),
		inFlight:         make(map[string]*inFlightEntry),
		waiters:          make(map[string]chan Chunk),
		pollNotify:       make(chan struct{}, 1),
		sessions:         make(map[string]*tabSession),
	}
}

func (h *Hub) ensureSessionLocked(clientID string) *tabSession {
	sess, ok := h.sessions[clientID]
	if !ok {
		sess = &tabSession{
			uploadedFiles: make(map[string]string),
		}
		h.sessions[clientID] = sess
	}
	return sess
}

// SetMinInterval configures the anti-ban minimum cooldown between consecutive
// browser-bridge generations to avoid rapid-fire automated request bursts.
func (h *Hub) SetMinInterval(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if d < 0 {
		d = 0
	}
	h.minInterval = d
}

// SetPacing configures anti-ban pacing: base cooldown, human-like random jitter,
// and maximum prompts allowed per rolling 10-minute window (0 disables cap).
func (h *Hub) SetPacing(minInterval, jitter time.Duration, maxPer10Min int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if minInterval < 0 {
		minInterval = 0
	}
	if jitter < 0 {
		jitter = 0
	}
	if maxPer10Min < 0 {
		maxPer10Min = 0
	}
	h.minInterval = minInterval
	h.jitterMax = jitter
	h.maxPer10Min = maxPer10Min
}

// RecordRateLimit trips the exponential cool-off circuit breaker (60s -> 180s -> 300s).
func (h *Hub) RecordRateLimit(cause string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.recordRateLimitLocked(cause)
}

func (h *Hub) recordRateLimitLocked(cause string) time.Duration {
	h.consecutiveRateLimits++
	var backoff time.Duration
	switch h.consecutiveRateLimits {
	case 1:
		backoff = 60 * time.Second
	case 2:
		backoff = 180 * time.Second
	default:
		backoff = 300 * time.Second
	}
	until := time.Now().Add(backoff)
	if until.After(h.cooldownUntil) {
		h.cooldownUntil = until
	}
	if cause != "" {
		h.lastRateLimitCause = cause
	}
	return time.Until(h.cooldownUntil)
}

// CooldownRemaining returns any active rate-limit circuit-breaker remaining duration.
func (h *Hub) CooldownRemaining() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Now().Before(h.cooldownUntil) {
		return time.Until(h.cooldownUntil)
	}
	return 0
}

// IsConnected returns true if a browser tab sent a poll/heartbeat within the last 45 seconds,
// or if an active generation is still in flight within the 180s window.
func (h *Hub) IsConnected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tabName == "" {
		return false
	}
	if time.Since(h.lastHeartbeat) < 45*time.Second {
		return true
	}
	return len(h.inFlight) > 0 && time.Since(h.lastHeartbeat) < 180*time.Second
}

func (h *Hub) TabName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tabName
}

func (h *Hub) Touch(tabName string) {
	h.Heartbeat(tabName, "")
}

// Heartbeat updates tab and client liveness timestamps.
func (h *Hub) Heartbeat(tabName, clientID string) {
	h.UpdateTabSession(tabName, clientID, "")
}

// UpdateTabSession records liveness and resets conversation delta state whenever
// a tab starts a fresh chat (new convGen), while preserving per-tab state so
// background heartbeats from other open tabs cannot wipe the active tab's uploads.
func (h *Hub) UpdateTabSession(tabName, clientID, convGen string) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if tabName != "" {
		h.tabName = tabName
	}

	key := clientID
	if key == "" {
		key = h.activeClientID
	}
	sess := h.ensureSessionLocked(key)

	if clientID != "" {
		h.clientHeartbeats[clientID] = now
	}
	if convGen != "" {
		if sess.convGen != "" && sess.convGen != convGen {
			sess.fingerprints = nil
			sess.uploadedFiles = make(map[string]string)
		}
		sess.convGen = convGen
	}

	// Do not let an idle background tab hijack activeClientID while another tab has an in-flight turn
	if clientID != "" {
		if h.activeClientID == "" || h.activeClientID == clientID || len(h.inFlight) == 0 {
			h.activeClientID = clientID
			h.activeConvGen = sess.convGen
		}
	} else if convGen != "" {
		h.activeConvGen = sess.convGen
	}
	h.lastHeartbeat = now
}

// ActiveSession snapshots the active tab identity and previously uploaded file signatures.
func (h *Hub) ActiveSession() (clientID, convGen string, uploaded map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.ensureSessionLocked(h.activeClientID)
	cp := make(map[string]string, len(sess.uploadedFiles))
	for k, v := range sess.uploadedFiles {
		cp[k] = v
	}
	return h.activeClientID, sess.convGen, cp
}

// MatchDeltaPrefix returns the number of leading messages already present in the
// active browser tab's live conversation if fingerprints strictly extend it.
func (h *Hub) MatchDeltaPrefix(fingerprints []string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.ensureSessionLocked(h.activeClientID)
	prev := sess.fingerprints
	if len(prev) == 0 || len(fingerprints) <= len(prev) {
		return 0
	}
	for i := range prev {
		if prev[i] != fingerprints[i] {
			return 0
		}
	}
	return len(prev)
}

// RecordTurnSuccess saves the conversation prefix and uploaded file signatures for the
// tab session so subsequent follow-up turns can send compact deltas and avoid
// re-uploading unchanged files while automatically refreshing edited code files.
func (h *Hub) RecordTurnSuccess(clientID, convGen string, fingerprints []string, uploadedSigs map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.ensureSessionLocked(clientID)
	if convGen != "" && sess.convGen != "" && convGen != sess.convGen {
		return
	}
	if convGen != "" && sess.convGen == "" {
		sess.convGen = convGen
	}
	cp := make([]string, len(fingerprints))
	copy(cp, fingerprints)
	sess.fingerprints = cp
	for path, sig := range uploadedSigs {
		if path != "" {
			sess.uploadedFiles[path] = sig
		}
	}
}

// WaitForNextRequest blocks until a generation request is queued or timeout occurs.
func (h *Hub) WaitForNextRequest(ctx context.Context, tabName string, timeout time.Duration) (*Request, error) {
	return h.WaitForNextRequestForClient(ctx, tabName, "", timeout)
}

// WaitForNextRequestForClient blocks until a generation request is queued or timeout occurs,
// tracking the polling client ID to avoid duplicate dispatches across tabs.
func (h *Hub) WaitForNextRequestForClient(ctx context.Context, tabName, clientID string, timeout time.Duration) (*Request, error) {
	return h.WaitForNextRequestForSession(ctx, tabName, clientID, "", timeout)
}

// WaitForNextRequestForSession blocks until a generation request is queued or timeout occurs,
// tracking both clientID and conversation generation.
func (h *Hub) WaitForNextRequestForSession(ctx context.Context, tabName, clientID, convGen string, timeout time.Duration) (*Request, error) {
	h.UpdateTabSession(tabName, clientID, convGen)

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		h.mu.Lock()
		if len(h.pending) > 0 {
			req := h.pending[0]
			h.pending = h.pending[1:]
			if len(h.pending) == 0 {
				select {
				case <-h.pollNotify:
				default:
				}
			}
			h.inFlight[req.ID] = &inFlightEntry{
				req:        req,
				tabName:    tabName,
				clientID:   clientID,
				assignedAt: time.Now(),
			}
			h.mu.Unlock()
			return req, nil
		}

		// Only re-dispatch an orphaned in-flight request if zero chunks have been streamed yet
		// AND its assigned tab appears to have reloaded or stopped heartbeating.
		for id, entry := range h.inFlight {
			if _, exists := h.waiters[id]; !exists {
				delete(h.inFlight, id)
				continue
			}
			if entry.hasChunks {
				continue
			}
			orphaned := false
			if clientID != "" && entry.clientID != "" {
				if entry.clientID != clientID {
					lastSeen := h.clientHeartbeats[entry.clientID]
					if time.Since(lastSeen) > 15*time.Second && time.Since(entry.assignedAt) > 10*time.Second {
						orphaned = true
					}
				}
			} else if time.Since(entry.assignedAt) > 20*time.Second {
				orphaned = true
			}
			if orphaned {
				entry.tabName = tabName
				entry.clientID = clientID
				entry.assignedAt = time.Now()
				req := entry.req
				h.mu.Unlock()
				return req, nil
			}
		}

		notify := h.pollNotify
		h.mu.Unlock()

		select {
		case <-notify:
			// Re-check h.pending under lock; ignores stale or spurious wakeups.
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, nil
		}
	}
}

// SubmitChunk routes an incoming chunk from the browser extension to the waiting HTTP request.
func (h *Hub) SubmitChunk(c Chunk) error {
	h.Touch("")

	h.mu.Lock()
	if c.RateLimited || looksLikeRateLimitError(c.Error) {
		h.recordRateLimitLocked(c.Error)
		c.RateLimited = true
	} else if c.Done && c.Error == "" {
		h.consecutiveRateLimits = 0
	}

	if entry, ok := h.inFlight[c.ID]; ok {
		if c.Text != "" {
			entry.hasChunks = true
		}
		if c.Done || c.Error != "" {
			delete(h.inFlight, c.ID)
		}
	}
	ch, exists := h.waiters[c.ID]
	h.mu.Unlock()

	if !exists {
		return fmt.Errorf("no active generation request for ID %s", c.ID)
	}

	select {
	case ch <- c:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("channel buffer full for ID %s", c.ID)
	}
}

func (h *Hub) pruneRecentLocked(now time.Time) {
	cutoff := now.Add(-10 * time.Minute)
	keep := 0
	for _, ts := range h.recentDispatches {
		if ts.After(cutoff) {
			h.recentDispatches[keep] = ts
			keep++
		}
	}
	h.recentDispatches = h.recentDispatches[:keep]
}

// ComputePacingDelay calculates the adaptive anti-ban delay (base + progressive burst
// scaling + human-like jitter + rolling 10-minute window pacing) or returns *RateLimitError
// if the circuit breaker is active.
func (h *Hub) ComputePacingDelay() (time.Duration, error) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()

	if now.Before(h.cooldownUntil) {
		return 0, &RateLimitError{
			RetryAfter: time.Until(h.cooldownUntil),
			Cause:      h.lastRateLimitCause,
		}
	}

	h.pruneRecentLocked(now)

	var waitDur time.Duration
	if h.minInterval > 0 && !h.lastCompletedAt.IsZero() {
		recent5m := 0
		fiveMinAgo := now.Add(-5 * time.Minute)
		for _, ts := range h.recentDispatches {
			if ts.After(fiveMinAgo) {
				recent5m++
			}
		}

		// Progressive burst backoff during long coding loops
		target := h.minInterval
		jitterCap := h.jitterMax
		switch {
		case recent5m >= 10:
			target = h.minInterval * 4
			jitterCap = h.jitterMax * 2
		case recent5m >= 5:
			target = h.minInterval * 2
		}

		if jitterCap > 0 {
			target += time.Duration(rand.Int64N(int64(jitterCap) + 1))
		}

		elapsed := now.Sub(h.lastCompletedAt)
		if elapsed < target {
			waitDur = target - elapsed
		}
	}

	// Smooth rolling 10-minute ceiling so heavy coding sessions pace naturally
	// rather than triggering Google's hard frequency ban.
	if h.maxPer10Min > 0 && len(h.recentDispatches) >= h.maxPer10Min {
		oldestExpiry := h.recentDispatches[0].Add(10 * time.Minute)
		if oldestExpiry.After(now) {
			windowWait := oldestExpiry.Sub(now)
			if windowWait > waitDur {
				waitDur = windowWait
			}
		}
	}

	return waitDur, nil
}

// GenerateStream queues a prompt to the browser extension and yields text chunks as they arrive.
func (h *Hub) GenerateStream(ctx context.Context, prompt, model string, emit func(string) error) error {
	return h.GenerateStreamWithOptions(ctx, GenerateOptions{
		Prompt: prompt,
		Model:  model,
	}, emit)
}

// GenerateStreamWithOptions queues a prompt (with optional direct file attachments) to the
// browser extension, applying adaptive anti-ban pacing and 429 circuit-breaker protection.
func (h *Hub) GenerateStreamWithOptions(ctx context.Context, opts GenerateOptions, emit func(string) error) error {
	if !h.IsConnected() {
		return errors.New("no browser tab connected to the bridge")
	}

	waitDur, rlErr := h.ComputePacingDelay()
	if rlErr != nil {
		return rlErr
	}
	if waitDur > 0 {
		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		// Re-check circuit breaker after pacing sleep in case another tab/request tripped it
		if rem := h.CooldownRemaining(); rem > 0 {
			return &RateLimitError{RetryAfter: rem}
		}
	}

	reqID := fmt.Sprintf("req_%d_%d", time.Now().UnixNano(), h.seq.Add(1))
	req := &Request{
		ID:          reqID,
		Prompt:      opts.Prompt,
		Model:       opts.Model,
		Attachments: opts.Attachments,
		CreatedAt:   time.Now(),
	}

	ch := make(chan Chunk, 512)

	h.mu.Lock()
	h.waiters[reqID] = ch
	h.pending = append(h.pending, req)
	h.recentDispatches = append(h.recentDispatches, time.Now())
	h.pruneRecentLocked(time.Now())
	select {
	case h.pollNotify <- struct{}{}:
	default:
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.waiters, reqID)
		delete(h.inFlight, reqID)
		for i, r := range h.pending {
			if r.ID == reqID {
				h.pending = append(h.pending[:i], h.pending[i+1:]...)
				break
			}
		}
		h.lastCompletedAt = time.Now()
		h.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-ch:
			if !ok {
				return nil
			}
			if chunk.RateLimited {
				rem := h.CooldownRemaining()
				if rem <= 0 {
					rem = 60 * time.Second
				}
				return &RateLimitError{RetryAfter: rem, Cause: chunk.Error}
			}
			if chunk.Error != "" {
				return errors.New(chunk.Error)
			}
			if chunk.Text != "" {
				if err := emit(chunk.Text); err != nil {
					return err
				}
			}
			if chunk.Done {
				return nil
			}
		case <-time.After(180 * time.Second):
			return errors.New("browser bridge generation timed out (180s)")
		}
	}
}

// Generate collects all stream chunks and returns the complete text.
func (h *Hub) Generate(ctx context.Context, prompt, model string) (string, error) {
	return h.GenerateWithOptions(ctx, GenerateOptions{
		Prompt: prompt,
		Model:  model,
	})
}

// GenerateWithOptions collects all stream chunks for opts and returns the complete text.
func (h *Hub) GenerateWithOptions(ctx context.Context, opts GenerateOptions) (string, error) {
	var sb strings.Builder
	err := h.GenerateStreamWithOptions(ctx, opts, func(chunk string) error {
		sb.WriteString(chunk)
		return nil
	})
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}
