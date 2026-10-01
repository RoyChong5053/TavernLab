// Package mcp is a minimal Streamable-HTTP client for rag-mcp-server.
//
// Only tools/call search_memory is needed: TavernLab passes the latest user
// utterance as query and injects the returned chunks into mcp-source blocks.
// Fail-open by design: the caller records the error in audit and chats on.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/memory"
)

const DefaultTimeout = 120 * time.Second

// Client talks to http(s)://host:port/mcp.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// WaitSeconds is sent as wait_seconds so rag-mcp-server blocks for a result
	// instead of returning a pollable job. It is derived from the HTTP timeout
	// minus a safety margin, keeping this synchronous, non-agent client on the
	// direct-result path.
	WaitSeconds int
}

// New builds a client with the default timeout.
func New(baseURL string) *Client {
	return NewWithTimeout(baseURL, DefaultTimeout)
}

func NewWithTimeout(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	wait := int(timeout.Seconds()) - 5
	if wait < 1 {
		wait = 1
	}
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		HTTP:        &http.Client{Timeout: timeout},
		WaitSeconds: wait,
	}
}

var resultHead = regexp.MustCompile(`(?m)^--- Result \d+ \(score: ([\d.]+)\) ---\n?`)

// CallTool invokes any tool on the server and returns the concatenated text
// content blocks verbatim. This is the generic escape hatch: Search() is a
// specialised wrapper that also parses the RAG "--- Result N ---" format, but
// a second MCP server (reitti-mcp) exposes tools whose text is meant to be read
// as-is, so the raw form has to be reachable without re-implementing the
// JSON-RPC / error plumbing.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("empty tool name")
	}
	if args == nil {
		args = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(b))
		if detail == "" {
			detail = resp.Status
		}
		return "", fmt.Errorf("mcp http error: %s", detail)
	}
	var rpc struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return "", fmt.Errorf("decode mcp response: %w", err)
	}
	if rpc.Error != nil {
		return "", fmt.Errorf("mcp error: %s", rpc.Error.Message)
	}
	if rpc.Result == nil {
		return "", fmt.Errorf("empty mcp result")
	}
	var text strings.Builder
	for _, c := range rpc.Result.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	out := text.String()
	if rpc.Result.IsError {
		detail := strings.TrimSpace(out)
		if detail == "" {
			detail = "tool call failed"
		}
		return "", fmt.Errorf("mcp tool error: %s", detail)
	}
	return out, nil
}

// Search calls search_memory. Empty collection = server default (global_memory).
// topK<=0 omits top_k (server default); threshold<0 omits threshold.
func (c *Client) Search(ctx context.Context, query, collection string, topK int, threshold float64) ([]memory.Hit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	args := map[string]any{"query": query}
	if collection != "" {
		args["collection_id"] = collection
	}
	if topK > 0 {
		args["top_k"] = topK
	}
	if threshold >= 0 {
		args["threshold"] = threshold
	}
	// Keep this synchronous caller on the direct-result path: ask the server to
	// wait up to (HTTP timeout - margin) instead of handing back a job at 10s.
	if c.WaitSeconds > 0 {
		args["wait_seconds"] = c.WaitSeconds
	}
	text, err := c.CallTool(ctx, "search_memory", args)
	if err != nil {
		return nil, err
	}
	if jobID, ok := pendingJobID(text); ok {
		return nil, fmt.Errorf("rag-mcp-server returned a pending job %s (search exceeded wait_seconds); result not ready", jobID)
	}
	return splitResults(text), nil
}

// pendingJobID detects the {status:pending, job_id:...} payload rag-mcp-server
// returns when a tool call exceeds wait_seconds. It must be caught before
// splitResults, which would otherwise misread the JSON as a memory chunk.
func pendingJobID(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "{") {
		return "", false
	}
	var p struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(t), &p); err != nil {
		return "", false
	}
	if p.Status == "pending" {
		return p.JobID, true
	}
	return "", false
}

// splitResults chops the "--- Result N (score: X) ---" formatted text into hits.
func splitResults(text string) []memory.Hit {
	if strings.TrimSpace(text) == "" || strings.HasPrefix(strings.TrimSpace(text), "No results found") {
		return nil
	}
	locs := resultHead.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return []memory.Hit{{ID: "mcp-1", Text: strings.TrimSpace(text), Source: "mcp"}}
	}
	var hits []memory.Hit
	for i, loc := range locs {
		start := loc[0]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		score := 0.0
		if s, err := strconv.ParseFloat(text[loc[2]:loc[3]], 64); err == nil {
			score = s
		}
		body := strings.TrimSpace(text[loc[1]:end])
		hit := memory.Hit{ID: fmt.Sprintf("mcp-%d", i+1), Text: body, Score: score, Source: "mcp"}
		// Peel "Collection: x" / "Source: y" header lines when present.
		lines := strings.SplitN(body, "\n", 4)
		rest := body
		for _, ln := range lines {
			if v, ok := strings.CutPrefix(ln, "Collection: "); ok {
				hit.Source = "mcp:" + strings.TrimSpace(v)
				rest = strings.TrimPrefix(rest, ln+"\n")
			} else if v, ok := strings.CutPrefix(ln, "Source: "); ok {
				rest = strings.TrimPrefix(rest, ln+"\n")
				_ = v
			} else {
				break
			}
		}
		hit.Text = strings.TrimSpace(rest)
		hits = append(hits, hit)
		_ = start
	}
	return hits
}

// Format renders hits for injection into an mcp block template.
func Format(hits []memory.Hit) string {
	var sb strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&sb, "--- [memory %d · score %.3f · %s] ---\n%s\n", i+1, h.Score, h.Source, h.Text)
	}
	return strings.TrimSpace(sb.String())
}
