package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RoyChong5053/TavernLab/internal/settings"
)

// The card view must be a faithful parse of the exact payload that gets
// injected: every header lands on the right field and the chunk body comes
// through byte for byte (including lines that merely look like headers).
func TestParseMemoryHits(t *testing.T) {
	payload := "Found 2 results:\n\n" +
		"--- Result 1 (score: 0.931) ---\n" +
		"ID: 9811905136838912890\n" +
		"Hash: 2065509242\n" +
		"Collection: global_memory [vectra]\n" +
		"Source: opencode - all - 20260323-20261004.md#12528\n" +
		"Tags: note, work\n" +
		"第一行\n第二行\nID: 这不是头部\n\n" +
		"--- Result 2 (score: 0.512) ---\n" +
		"没有头部的正文\n\n"

	hits := parseMemoryHits(payload)
	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d: %+v", len(hits), hits)
	}

	h := hits[0]
	if h.Score != 0.931 {
		t.Errorf("score = %v, want 0.931", h.Score)
	}
	if h.ID != "9811905136838912890" || h.Hash != "2065509242" {
		t.Errorf("id/hash = %q/%q", h.ID, h.Hash)
	}
	if h.Collection != "global_memory" || h.Backend != "vectra" {
		t.Errorf("collection/backend = %q/%q", h.Collection, h.Backend)
	}
	if h.Source != "opencode - all - 20260323-20261004.md#12528" {
		t.Errorf("source = %q", h.Source)
	}
	if len(h.Tags) != 2 || h.Tags[0] != "note" || h.Tags[1] != "work" {
		t.Errorf("tags = %v", h.Tags)
	}
	const wantBody = "第一行\n第二行\nID: 这不是头部"
	if h.Text != wantBody {
		t.Errorf("body altered:\n got %q\nwant %q", h.Text, wantBody)
	}

	if hits[1].Score != 0.512 || hits[1].Text != "没有头部的正文" {
		t.Errorf("headerless hit = %+v", hits[1])
	}
	if hits[1].Tags != nil || hits[1].ID != "" {
		t.Errorf("headerless hit leaked fields: %+v", hits[1])
	}
}

// Anything that is not the known envelope yields nil so the page falls back to
// the raw text instead of inventing structure.
func TestParseMemoryHitsRejectsUnknown(t *testing.T) {
	for _, in := range []string{"", "No results found.", "Some other tool text", "Found"} {
		if got := parseMemoryHits(in); got != nil {
			t.Errorf("parseMemoryHits(%q) = %+v, want nil", in, got)
		}
	}
}

// End-to-end through the MCP client: the raw text is untouched while the parsed
// hits mirror it.
func TestSearchMCPParsesHitsWithRawTextIntact(t *testing.T) {
	payload := "Found 1 results:\n\n--- Result 1 (score: 0.900) ---\nID: 42\nCollection: c [vectra]\n正文原文\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": payload}}},
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	s := settings.Settings{MCPEnabled: true, MCPURL: srv.URL, MCPTimeout: 5}
	text, err := searchMCP(context.Background(), s, t.TempDir(), "test", "查询")
	if err != nil {
		t.Fatal(err)
	}
	if text != payload {
		t.Fatalf("raw text altered:\n got %q\nwant %q", text, payload)
	}
	hits := parseMemoryHits(text)
	if len(hits) != 1 || hits[0].ID != "42" || hits[0].Text != "正文原文" {
		t.Fatalf("parsed hits = %+v", hits)
	}
}

func TestSplitCollectionBackend(t *testing.T) {
	cases := []struct {
		in           string
		col, backend string
	}{
		{"global_memory [vectra]", "global_memory", "vectra"},
		{"leer-chat-jina-v5", "leer-chat-jina-v5", ""},
		{"  spaced  [qdrant] ", "spaced", "qdrant"},
	}
	for _, tc := range cases {
		c, b := splitCollectionBackend(tc.in)
		if c != tc.col || b != tc.backend {
			t.Errorf("splitCollectionBackend(%q) = %q/%q, want %q/%q", tc.in, c, b, tc.col, tc.backend)
		}
	}
}
