package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Request struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Chunk struct {
	ID    string `json:"id"`
	Text  string `json:"text,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}

type inFlightEntry struct {
	req        *Request
	tabName    string
	clientID   string
	assignedAt time.Time
	hasChunks  bool
}

type Hub struct {
	mu               sync.Mutex
	seq              atomic.Uint64
	tabName          string
	lastHeartbeat    time.Time
	clientHeartbeats map[string]time.Time
	pending          []*Request
	inFlight         map[string]*inFlightEntry
	waiters          map[string]chan Chunk
	pollNotify       chan struct{}
}

func NewHub() *Hub {
	return &Hub{
		clientHeartbeats: make(map[string]time.Time),
		inFlight:         make(map[string]*inFlightEntry),
		waiters:          make(map[string]chan Chunk),
		pollNotify:       make(chan struct{}, 1),
	}
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
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if tabName != "" {
		h.tabName = tabName
	}
	if clientID != "" {
		h.clientHeartbeats[clientID] = now
	}
	h.lastHeartbeat = now
}

// WaitForNextRequest blocks until a generation request is queued or timeout occurs.
func (h *Hub) WaitForNextRequest(ctx context.Context, tabName string, timeout time.Duration) (*Request, error) {
	return h.WaitForNextRequestForClient(ctx, tabName, "", timeout)
}

// WaitForNextRequestForClient blocks until a generation request is queued or timeout occurs,
// tracking the polling client ID to avoid duplicate dispatches across tabs.
func (h *Hub) WaitForNextRequestForClient(ctx context.Context, tabName, clientID string, timeout time.Duration) (*Request, error) {
	h.Heartbeat(tabName, clientID)

	h.mu.Lock()
	if len(h.pending) > 0 {
		req := h.pending[0]
		h.pending = h.pending[1:]
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
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.pending) > 0 {
			req := h.pending[0]
			h.pending = h.pending[1:]
			h.inFlight[req.ID] = &inFlightEntry{
				req:        req,
				tabName:    tabName,
				clientID:   clientID,
				assignedAt: time.Now(),
			}
			return req, nil
		}
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, nil
	}
}

// SubmitChunk routes an incoming chunk from the browser extension to the waiting HTTP request.
func (h *Hub) SubmitChunk(c Chunk) error {
	h.Touch("")

	h.mu.Lock()
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

// GenerateStream queues a prompt to the browser extension and yields text chunks as they arrive.
func (h *Hub) GenerateStream(ctx context.Context, prompt, model string, emit func(string) error) error {
	if !h.IsConnected() {
		return errors.New("no browser tab connected to the bridge")
	}

	reqID := fmt.Sprintf("req_%d_%d", time.Now().UnixNano(), h.seq.Add(1))
	req := &Request{
		ID:        reqID,
		Prompt:    prompt,
		Model:     model,
		CreatedAt: time.Now(),
	}

	ch := make(chan Chunk, 512)

	h.mu.Lock()
	h.waiters[reqID] = ch
	h.pending = append(h.pending, req)
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
	var sb strings.Builder
	err := h.GenerateStream(ctx, prompt, model, func(chunk string) error {
		sb.WriteString(chunk)
		return nil
	})
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}
