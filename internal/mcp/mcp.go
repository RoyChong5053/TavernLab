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
}

// New builds a client with the default timeout.
func New(baseURL string) *Client {
	return NewWithTimeout(baseURL, DefaultTimeout)
}

func NewWithTimeout(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: timeout}}
}

var resultHead = regexp.MustCompile(`(?m)^--- Result \d+ \(score: ([\d.]+)\) ---\n?`)

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
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "search_memory", "arguments": args},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return nil, fmt.Errorf("mcp http error: %s", detail)
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
		return nil, fmt.Errorf("decode mcp response: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("mcp error: %s", rpc.Error.Message)
	}
	if rpc.Result == nil {
		return nil, fmt.Errorf("empty mcp result")
	}
	var text strings.Builder
	for _, c := range rpc.Result.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	if rpc.Result.IsError {
		detail := strings.TrimSpace(text.String())
		if detail == "" {
			detail = "tool call failed"
		}
		return nil, fmt.Errorf("mcp tool error: %s", detail)
	}
	return splitResults(text.String()), nil
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
