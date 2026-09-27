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

// TestForward_UpstreamCutEvent verifies that an explicit upstream_cut error
// event marks the turn as error (never a clean stop), flags upstream_cut in
// the rebuilt body, and records transport counters.
func TestForward_UpstreamCutEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for _, s := range []string{
			`data: {"choices":[{"delta":{"content":"half"}}],"usage":{"prompt_tokens":12950,"completion_tokens":12,"total_tokens":12962}}`,
			`data: {"error":{"message":"upstream stream ended without usage metadata (suspected provider cut)","type":"upstream_cut","code":"upstream_cut"}}`,
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
	_, rebuilt, usage := Forward(&http.Client{}, srv.URL, "", []byte(`{"stream":true}`), true, rec, rec)
	ch := firstChoice(t, rebuilt)
	if got := ch["finish_reason"]; got != "error" {
		t.Fatalf("finish_reason = %v, want error (%s)", got, rebuilt)
	}
	var body map[string]any
	_ = json.Unmarshal(rebuilt, &body)
	if body["upstream_cut"] != true {
		t.Fatalf("missing upstream_cut flag: %s", rebuilt)
	}
	tr, _ := body["transport"].(map[string]any)
	if tr == nil || tr["saw_usage"] != true {
		t.Fatalf("missing transport/saw_usage: %s", rebuilt)
	}
	if usage == nil {
		t.Fatalf("usage lost: %s", rebuilt)
	}
	msg, _ := ch["message"].(map[string]any)
	if msg["content"] != "half" {
		t.Fatalf("partial text lost: content = %v", msg["content"])
	}
}

// TestForward_TransportCounters verifies clean streams carry transport
// counters and no cut flags.
func TestForward_TransportCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for _, s := range []string{
			`data: {"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
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
	_, rebuilt, _ := Forward(&http.Client{}, srv.URL, "", []byte(`{"stream":true}`), true, rec, rec)
	ch := firstChoice(t, rebuilt)
	if got := ch["finish_reason"]; got != "stop" {
		t.Fatalf("finish_reason = %v, want stop (%s)", got, rebuilt)
	}
	var body map[string]any
	_ = json.Unmarshal(rebuilt, &body)
	if _, ok := body["upstream_cut"]; ok {
		t.Fatalf("unexpected upstream_cut flag: %s", rebuilt)
	}
	if strings.Contains(string(rebuilt), "stream_error") {
		t.Fatalf("unexpected stream_error: %s", rebuilt)
	}
	tr, _ := body["transport"].(map[string]any)
	if tr == nil || tr["chunks_received"] == nil || tr["chunks_parsed"] == nil {
		t.Fatalf("missing transport counters: %s", rebuilt)
	}
}
