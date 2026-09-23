package main

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/settings"
	"github.com/RoyChong5053/TavernLab/internal/store"
)

// hub fans out appended messages to live SSE subscribers per session.
type hub struct {
	mu   sync.Mutex
	subs map[string]map[chan store.ChatMessage]struct{}
}

func newHub() *hub {
	return &hub{subs: map[string]map[chan store.ChatMessage]struct{}{}}
}

func (h *hub) subscribe(session string) chan store.ChatMessage {
	ch := make(chan store.ChatMessage, 32)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[session] == nil {
		h.subs[session] = map[chan store.ChatMessage]struct{}{}
	}
	h.subs[session][ch] = struct{}{}
	return ch
}

func (h *hub) unsubscribe(session string, ch chan store.ChatMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.subs[session]; ok {
		delete(set, ch)
		if len(set) == 0 {
			delete(h.subs, session)
		}
	}
}

// publish delivers msg to every live subscriber of session (non-blocking:
// a slow client drops rather than stalling the chat turn).
func (h *hub) publish(session string, msg store.ChatMessage) {
	if msg.ID == "" {
		return
	}
	h.mu.Lock()
	chans := make([]chan store.ChatMessage, 0, len(h.subs[session]))
	for ch := range h.subs[session] {
		chans = append(chans, ch)
	}
	h.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- msg:
		default:
		}
	}
}

// publishNtfy pushes a notification to a self-hosted ntfy topic (background
// delivery for the phone app). Fire-and-forget, fail-open.
func publishNtfy(s settings.Settings, msg store.ChatMessage) {
	if strings.TrimSpace(s.NtfyURL) == "" || strings.TrimSpace(s.NtfyTopic) == "" {
		return
	}
	if msg.Role != "assistant" || strings.TrimSpace(msg.Text) == "" {
		return
	}
	url := strings.TrimRight(s.NtfyURL, "/") + "/" + strings.TrimLeft(s.NtfyTopic, "/")
	go func() {
		req, err := http.NewRequest("POST", url, bytes.NewReader([]byte(msg.Text)))
		if err != nil {
			return
		}
		req.Header.Set("Title", "TavernLab")
		req.Header.Set("Tags", "speech_balloon")
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		client := &http.Client{Timeout: 8 * time.Second}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
}
