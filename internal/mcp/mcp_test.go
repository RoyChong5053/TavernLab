package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearchRejectsToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"rerank failed"}]}}`)
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := client.Search(context.Background(), "query", "collection", 10, -1)
	if err == nil || !strings.Contains(err.Error(), "rerank failed") {
		t.Fatalf("expected tool error, got %v", err)
	}
}

func TestSearchRejectsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "mcp unavailable")
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := client.Search(context.Background(), "query", "", 10, -1)
	if err == nil || !strings.Contains(err.Error(), "mcp unavailable") {
		t.Fatalf("expected HTTP error, got %v", err)
	}
}

func TestSearchPropagatesContextCancellation(t *testing.T) {
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Search(ctx, "query", "", 10, -1)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("search did not honor context deadline: %s", elapsed)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("test server handler did not finish")
	}
}

// A single id goes out as collection_id; a CSV list becomes collection_ids.
func TestSearchSendsCollectionIDs(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"No results found."}]}}`)
	}))
	defer srv.Close()

	client := NewWithTimeout(srv.URL, 120*time.Second)
	if _, err := client.Search(context.Background(), "q", "col", 5, -1); err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(gotBody, `"collection_id":"col"`) || strings.Contains(gotBody, "collection_ids") {
		t.Fatalf("single id should send collection_id only: %s", gotBody)
	}

	if _, err := client.Search(context.Background(), "q", " a , b ,a", 5, -1); err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(gotBody, `"collection_ids":["a","b"]`) || strings.Contains(gotBody, `"collection_id"`) {
		t.Fatalf("CSV should send deduped collection_ids only: %s", gotBody)
	}
}

func TestStoreSendsCollectionAndMetadata(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"stored"}]}}`)
	}))
	defer srv.Close()

	client := NewWithTimeout(srv.URL, 120*time.Second)
	out, err := client.Store(context.Background(), "hello", "col", map[string]string{"type": "distill-auto"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if !strings.Contains(out, "stored") {
		t.Fatalf("expected stored text, got %q", out)
	}
	for _, want := range []string{`"store_memory"`, `"collection_id":"col"`, `"type":"distill-auto"`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("missing %s in %s", want, gotBody)
		}
	}
}

func TestStoreRejectsEmptyText(t *testing.T) {
	client := NewWithTimeout("http://127.0.0.1:0", time.Second)
	if _, err := client.Store(context.Background(), "  ", "col", nil); err == nil {
		t.Fatal("expected empty text error")
	}
}
