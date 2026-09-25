package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestForward_StreamFinishReasonPropagated verifies that a normal SSE stream
// keeps finish_reason in the rebuilt audit body (stop / length / ...).
func TestForward_StreamFinishReasonPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for _, s := range []string{
			`data: {"choices":[{"delta":{"content":"Hello "}}]}`,
			`data: {"choices":[{"delta":{"content":"world"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
			"data: [DONE]",
		} {
			_, _ = w.Write([]byte(s + "\n\n"))
			if f != nil {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	status, rebuilt, _ := Forward(&http.Client{}, srv.URL, "", []byte(`{"stream":true}`), true, rec, rec)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	ch := firstChoice(t, rebuilt)
	if got := ch["finish_reason"]; got != "length" {
		t.Fatalf("finish_reason = %v, want length (%s)", got, rebuilt)
	}
	msg, _ := ch["message"].(map[string]any)
	if msg["content"] != "Hello world" {
		t.Fatalf("content = %v, want %q", msg["content"], "Hello world")
	}
	if strings.Contains(string(rebuilt), "stream_error") {
		t.Fatalf("unexpected stream_error: %s", rebuilt)
	}
}

// TestForward_StreamCutMarksError verifies that an upstream stream cut mid-body
// is never recorded as a clean stop, and the partial text is preserved.
func TestForward_StreamCutMarksError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"))
		if f != nil {
			f.Flush()
		}
		// Abruptly close the connection mid-stream (no [DONE], no finish).
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	_, rebuilt, _ := Forward(&http.Client{}, srv.URL, "", []byte(`{"stream":true}`), true, rec, rec)
	ch := firstChoice(t, rebuilt)
	if got := ch["finish_reason"]; got != "error" {
		t.Fatalf("finish_reason = %v, want error (%s)", got, rebuilt)
	}
	var body map[string]any
	_ = json.Unmarshal(rebuilt, &body)
	if body["stream_error"] == nil {
		t.Fatalf("missing stream_error: %s", rebuilt)
	}
	msg, _ := ch["message"].(map[string]any)
	if msg["content"] != "partial" {
		t.Fatalf("partial text lost: content = %v", msg["content"])
	}
}

func firstChoice(t *testing.T, rebuilt []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rebuilt, &body); err != nil {
		t.Fatalf("unmarshal rebuilt: %v (%s)", err, rebuilt)
	}
	choices, _ := body["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in %s", rebuilt)
	}
	ch, _ := choices[0].(map[string]any)
	return ch
}
