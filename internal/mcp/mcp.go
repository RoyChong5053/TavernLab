// Package mcp is a minimal Streamable-HTTP client for rag-mcp-server.
//
// Only tools/call search_memory is needed: TavernLab passes the latest user
// utterance as query and injects the server's response, verbatim, into
// mcp-source blocks. Retrieval parameters (top-k, threshold, chunk size) live
// on the server, so there is nothing to parse or re-format here.
// Fail-open by design: the caller records the error in audit and chats on.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// CallTool invokes any tool on the server and returns the concatenated text
// content blocks verbatim. This is the generic escape hatch: a second MCP
// server (reitti-mcp) exposes tools whose text is meant to be read as-is, so
// the raw form has to be reachable without re-implementing the JSON-RPC /
// error plumbing.
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

// Search calls search_memory and returns the server's response text exactly as
// it arrives. Empty collection = server default. collection may be a single id
// or a CSV list ("a,b"); multiple distinct ids are sent as collection_ids so the
// server searches them in one call.
//
// Nothing is parsed or re-formatted: the same bytes the rag-mcp-server
// dashboard shows for a query are the bytes that reach the prompt.
func (c *Client) Search(ctx context.Context, query, collection string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("empty query")
	}
	args := map[string]any{"query": query}
	if ids := splitCollectionCSV(collection); len(ids) > 1 {
		args["collection_ids"] = ids
	} else if len(ids) == 1 {
		args["collection_id"] = ids[0]
	}
	return c.CallTool(ctx, "search_memory", args)
}

// Store calls store_memory. Empty collection = server default (the caller
// passes the recall collection so distilled lines share the vector space).
// metadata may be nil. Thin client: the error is returned verbatim and the
// caller decides (fail-open). Never retries or health-checks here; one-api
// owns channel failover.
func (c *Client) Store(ctx context.Context, text, collection string, metadata map[string]string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("empty text")
	}
	args := map[string]any{"text": text}
	if id := strings.TrimSpace(collection); id != "" {
		args["collection_id"] = id
	}
	if len(metadata) > 0 {
		m := make(map[string]any, len(metadata))
		for k, v := range metadata {
			m[k] = v
		}
		args["metadata"] = m
	}
	return c.CallTool(ctx, "store_memory", args)
}

// splitCollectionCSV turns "a, b, a" into ["a", "b"] (trim + dedupe).
func splitCollectionCSV(s string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		if id := strings.TrimSpace(part); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}
