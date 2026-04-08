package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// SSEBroker manages server-sent event subscriptions.
type SSEBroker struct {
	mu      sync.Mutex
	clients map[chan string]bool
}

var broker = &SSEBroker{clients: make(map[chan string]bool)}

// Subscribe returns a new channel that will receive SSE payloads.
func (b *SSEBroker) Subscribe() chan string {
	ch := make(chan string, 16)
	b.mu.Lock()
	b.clients[ch] = true
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a channel from the broker and closes it.
func (b *SSEBroker) Unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

// Publish sends a named SSE event with JSON-encoded data to all subscribers.
func (b *SSEBroker) Publish(event string, data interface{}) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(payload))

	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
			// Drop if client channel is full.
		}
	}
}

// PublishEvent is a package-level convenience wrapper around the global broker.
func PublishEvent(event string, data interface{}) {
	broker.Publish(event, data)
}

// handleSSE streams server-sent events to the client until it disconnects.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := broker.Subscribe()
	defer broker.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		}
	}
}
