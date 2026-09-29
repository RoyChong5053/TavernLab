// Package reitti is a fail-open client for the reitti-mcp server, which turns
// Reitti's staypoint detection into a readable movement narrative.
//
// Role in the stack (see reitti-mcp-server/README.md):
//
//	Mate10+Colota -> Reitti (collect) -> reitti-mcp (parse) -> TavernLab (consume)
//
// TavernLab treats this as an OPTIONAL, independently switchable enrichment.
// Every failure mode here is non-fatal by design: if reitti-mcp is down, the
// token is wrong, the device stopped uploading, or the window is empty, the
// caller gets ("", nil) and distillation proceeds exactly as it did before the
// feature existed. A location experiment must never be able to break the memory
// system it is bolted onto.
//
// This is deliberately a SEPARATE line from the GT20 gps-logger feed that
// backs the {{location}} macro in the time_anchor block. That one is the cheap,
// always-on "roughly where am I" for the character card; this one is rich
// staypoint evidence for the distiller. Neither writes to the other.
package reitti

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/mcp"
)

// DefaultWindowHours is used when the caller has no better bound (first run, or
// meta.LastRun is unset). Not a hard policy — the caller normally passes
// "since the last distillation" so consecutive windows tile without a gap.
const DefaultWindowHours = 3

// Client talks to one reitti-mcp endpoint.
type Client struct {
	MCP *mcp.Client
	// Timezone is passed through to the server so the narrative renders in local
	// time. Empty = server default. The server errors on an unknown zone rather
	// than silently falling back to UTC, which is what we want.
	Timezone string
}

// New builds a client. timeout bounds one call: distillation runs in the
// background but still holds a slot, so a wedged server must not pile up.
func New(baseURL, timezone string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{MCP: mcp.NewWithTimeout(baseURL, timeout), Timezone: timezone}
}

// MovementWindow returns the narrative for [since, now], or ("", nil) when
// there is nothing usable. An error is returned only for transport/protocol
// failures so the caller can log them; even then the text is empty so the
// caller can choose to fail open.
//
// since is exclusive of nothing — it is passed straight through as the window
// start. Pass meta.LastRun to get "everything since the last extraction"; pass
// empty to use the server's default window.
func (c *Client) MovementWindow(ctx context.Context, since string, windowHours int) (string, error) {
	if c == nil || c.MCP == nil {
		return "", nil
	}
	args := map[string]any{}
	if strings.TrimSpace(since) != "" {
		args["since"] = since
	} else {
		if windowHours <= 0 {
			windowHours = DefaultWindowHours
		}
		args["hours"] = windowHours
	}
	if strings.TrimSpace(c.Timezone) != "" {
		args["timezone"] = c.Timezone
	}
	// Detach from the request context: a parent cancel (e.g. the HTTP request
	// that triggered distillation finishing) must not abort this and log a
	// spurious failure. Bounded only by the client timeout.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.MCP.HTTP.Timeout)
	defer cancel()
	text, err := c.MCP.CallTool(callCtx, "get_movement_window", args)
	if err != nil {
		return "", fmt.Errorf("reitti movement window: %w", err)
	}
	return text, nil
}

// HasMovement reports whether the narrative contains actual staypoint data.
// reitti-mcp returns a well-formed "[Recent Movement]" header even for an empty
// window, so callers that only want to inject when there is real data must
// check this rather than testing for an empty string.
func HasMovement(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	// The renderer emits one "- HH:MM–HH:MM @place" line per stay.
	return strings.Contains(text, " @")
}
