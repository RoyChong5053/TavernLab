// Package proxy forwards assembled prompts to an OpenAI-compatible
// upstream (one-api with embedding/reranker fan-out). P0 supports
// non-stream + SSE stream passthrough with audit capture of reply text.
package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatRequest is the minimal OpenAI chat body we accept.
type ChatRequest struct {
	Model    any              `json:"model"`
	Messages []map[string]any `json:"messages"`
	Stream   bool             `json:"stream"`
	Extra    map[string]any   `json:"-"`
}

// Forward sends body to upstream and returns (status, respBody, usage).
// If stream=true and w!=nil, SSE chunks are relayed live and the full
// text is reconstructed for audit.
func Forward(client *http.Client, upstream, apiKey string, body []byte, stream bool, w http.ResponseWriter, flusher http.Flusher) (int, []byte, map[string]any) {
	req, err := http.NewRequest("POST", strings.TrimRight(upstream, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 500, []byte(`{"error":"build upstream request failed"}`), nil
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 502, []byte(`{"error":"upstream unreachable"}`), nil
	}
	defer resp.Body.Close()

	if !stream {
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, extractUsage(b)
	}

	// stream relay
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	var full strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		_, _ = w.Write([]byte(line + "\n\n"))
		flusher.Flush()
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(payload), &chunk) == nil {
				for _, c := range chunk.Choices {
					full.WriteString(c.Delta.Content)
				}
			}
		}
	}
	rebuilt, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": full.String()}}},
		"stream_rebuilt": true, "time": time.Now().Format(time.RFC3339),
	})
	return resp.StatusCode, rebuilt, nil
}

func extractUsage(b []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	if u, ok := m["usage"].(map[string]any); ok {
		return u
	}
	return nil
}
