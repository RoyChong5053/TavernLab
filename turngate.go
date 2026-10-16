package main

import (
	"context"
	"sync"
)

// turnGates serialises generation per (session, client turn id).
//
// The phone retries a turn it is not sure landed, reusing the same
// client_msg_id. Before this gate, a retry that arrived while the original was
// still generating started a SECOND generation: the store already held the user
// row but no reply yet, so the handler read that as "the previous attempt died"
// and ran the turn again. Both replies reached the phone, which shows one
// message answered twice — and because the reply row's id is derived from the
// turn key, only one of them was persisted, so the chat log looked normal
// afterwards and the doubling was invisible in storage.
//
// Holding a gate for the duration of the generation makes a retry genuinely
// idempotent: the retry waits, then replays the stored reply. When the original
// really did die without storing anything, the gate is already free by the time
// the retry gets there and the retry generates normally — so a genuine retry
// still works.
type turnGates struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func newTurnGates() *turnGates {
	return &turnGates{m: map[string]chan struct{}{}}
}

// enter blocks until the caller owns key. It reports false when ctx ended first
// (the caller hung up), so the caller can drop the request without generating.
// A successful enter must be paired with leave.
//
// There is deliberately no timeout of its own. The in-flight attempt is already
// bounded by the upstream HTTP client, so the wait is bounded too, and ending
// the wait early is precisely what re-opened the second generation.
func (g *turnGates) enter(ctx context.Context, key string) bool {
	for {
		g.mu.Lock()
		ch, busy := g.m[key]
		if !busy {
			g.m[key] = make(chan struct{})
			g.mu.Unlock()
			return true
		}
		g.mu.Unlock()
		select {
		case <-ch:
			// The holder finished: loop and try to take the gate ourselves.
		case <-ctx.Done():
			return false
		}
	}
}

// leave releases key and wakes every waiter so one of them can take it. It is
// idempotent and safe to call on the empty key, so callers can release early
// (as soon as the reply is stored) and still keep a deferred release as a
// safety net for the remaining returns.
func (g *turnGates) leave(key string) {
	if key == "" {
		return
	}
	g.mu.Lock()
	ch, ok := g.m[key]
	if ok {
		delete(g.m, key)
		close(ch)
	}
	g.mu.Unlock()
}

// busy reports whether a generation is currently in flight for key.
func (g *turnGates) busy(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.m[key]
	return ok
}
