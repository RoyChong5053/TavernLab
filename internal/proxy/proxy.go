// Package proxy forwards assembled prompts to an OpenAI-compatible
// upstream (one-api with embedding/reranker fan-out). P0 supports
// non-stream + SSE stream passthrough with audit capture of reply text.
package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/obs"
)

// truncate caps a string for log/error output.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

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
	w.Header().Set("X-Accel-Buffering", "no")
	// Upstream rejected the request (auth/quota/bad model): surface it as an
	// SSE error event instead of an empty stream the UI can't explain.
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		obs.Warn("upstream stream error", map[string]any{"status": resp.StatusCode, "body": truncate(string(b), 300)})
		payload, _ := json.Marshal(map[string]any{"error": map[string]any{"status": resp.StatusCode, "message": truncate(string(b), 500)}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
		return resp.StatusCode, payload, nil
	}
	var full strings.Builder
	var usage map[string]any
	finishReason := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue // keep exactly one blank separator per event
		}
		_, _ = w.Write([]byte(line + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			if payload == "[DONE]" {
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage map[string]any `json:"usage"`
			}
			if json.Unmarshal([]byte(payload), &chunk) == nil {
				for _, c := range chunk.Choices {
					full.WriteString(c.Delta.Content)
					if c.FinishReason != "" {
						finishReason = c.FinishReason
					}
				}
				if chunk.Usage != nil {
					usage = chunk.Usage
				}
			}
		}
	}

	// A scanner error means the upstream stream was cut mid-flight (network
	// reset, one-api timeout, client abort). Never treat that as a clean stop:
	// surface it to the client and mark the persisted reply as incomplete.
	broken := ""
	if err := sc.Err(); err != nil {
		broken = err.Error()
		obs.Warn("upstream stream interrupted", map[string]any{
			"finish_reason": finishReason,
			"reply_len":     full.Len(),
			"error":         broken,
		})
	}
	gotFinish := finishReason != ""
	if !gotFinish {
		finishReason = "stop"
	}
	if broken != "" {
		// A cut stream is not a clean stop.
		finishReason = "error"
	}
	if broken != "" {
		payload, _ := json.Marshal(map[string]any{"error": map[string]any{
			"message": "upstream stream interrupted: " + broken,
			"type":    "stream_interrupted",
		}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	} else if !gotFinish {
		// Ensure clients always get a terminal finish_reason even when the
		// provider omitted it (OpenAI-compatible streams normally send [DONE]).
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"%s\"}]}\n\n", finishReason)
		if flusher != nil {
			flusher.Flush()
		}
	}

	rebuiltBody := map[string]any{
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": full.String()},
			"finish_reason": finishReason,
		}},
		"stream_rebuilt": true, "time": time.Now().Format(time.RFC3339),
	}
	if broken != "" {
		rebuiltBody["stream_error"] = broken
		rebuiltBody["incomplete"] = true
	}
	rebuilt, _ := json.Marshal(rebuiltBody)
	return resp.StatusCode, rebuilt, usage
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
