package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

type Hub struct {
	mu            sync.Mutex
	tabName       string
	lastHeartbeat time.Time
	pending       []*Request
	inFlight      map[string]*Request
	waiters       map[string]chan Chunk
	pollNotify    chan struct{}
}

func NewHub() *Hub {
	return &Hub{
		inFlight:   make(map[string]*Request),
		waiters:    make(map[string]chan Chunk),
		pollNotify: make(chan struct{}, 1),
	}
}

// IsConnected returns true if a browser tab sent a poll/heartbeat within the last 45 seconds.
func (h *Hub) IsConnected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Since(h.lastHeartbeat) < 45*time.Second && h.tabName != ""
}

func (h *Hub) TabName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tabName
}

func (h *Hub) Touch(tabName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if tabName != "" {
		h.tabName = tabName
	}
	h.lastHeartbeat = time.Now()
}

// WaitForNextRequest blocks until a generation request is queued or timeout occurs.
func (h *Hub) WaitForNextRequest(ctx context.Context, tabName string, timeout time.Duration) (*Request, error) {
	h.Touch(tabName)

	h.mu.Lock()
	if len(h.pending) > 0 {
		req := h.pending[0]
		h.pending = h.pending[1:]
		h.inFlight[req.ID] = req
		h.mu.Unlock()
		return req, nil
	}
	// If a previous request was dispatched but the tab reconnected/reloaded before completing it, re-dispatch it
	for id, req := range h.inFlight {
		if _, exists := h.waiters[id]; exists && time.Since(req.CreatedAt) > 4*time.Second {
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
			h.inFlight[req.ID] = req
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
	if c.Done || c.Error != "" {
		delete(h.inFlight, c.ID)
	}
	ch, exists := h.waiters[c.ID]
	h.mu.Unlock()

	if !exists {
		return fmt.Errorf("no active generation request for ID %s", c.ID)
	}

	select {
	case ch <- c:
		return nil
	default:
		return fmt.Errorf("channel buffer full for ID %s", c.ID)
	}
}

// GenerateStream queues a prompt to the browser extension and yields text chunks as they arrive.
func (h *Hub) GenerateStream(ctx context.Context, prompt, model string, emit func(string) error) error {
	if !h.IsConnected() {
		return errors.New("no browser tab connected to the bridge")
	}

	reqID := fmt.Sprintf("req_%d", time.Now().UnixNano())
	req := &Request{
		ID:        reqID,
		Prompt:    prompt,
		Model:     model,
		CreatedAt: time.Now(),
	}

	ch := make(chan Chunk, 200)

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
