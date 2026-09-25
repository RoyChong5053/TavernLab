package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/distill"
	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/settings"
	"github.com/RoyChong5053/TavernLab/internal/store"
)

func TestNormalizeBlocksSplitsDistilled(t *testing.T) {
	in := []engine.Block{{
		ID: "distilled", Role: "system", Order: 40, Enabled: true, Level: engine.LevelLocked,
		Source:   engine.Source{Type: "distilled", Collection: "default"},
		Template: "<User State>\n{{distilled}}\n</User State>",
	}}
	out := normalizeBlocks(in)
	if len(out) != 2 {
		t.Fatalf("want distilled split into 2 blocks, got %d", len(out))
	}
	if out[0].ID != "distilled_state" || out[0].Source.Type != "distilled_state" || out[0].Level != engine.LevelLocked {
		t.Fatalf("bad state block: %+v", out[0])
	}
	if out[1].ID != "distilled_log" || out[1].Source.Type != "distilled_log" || out[1].Level != engine.LevelTrim {
		t.Fatalf("bad log block: %+v", out[1])
	}
	if again := normalizeBlocks(out); len(again) != 2 {
		t.Fatalf("migration must be idempotent, got %d blocks", len(again))
	}
}

func TestListInputsBuildsDistilledLog(t *testing.T) {
	root := t.TempDir()
	sheet := "[USER STATE]\n[25-09-2026] ok\n\n[LOG]\n[25-09-2026 10:00] a\n[26-09-2026 11:00] b"
	if err := distill.Save(root, "阿离", sheet); err != nil {
		t.Fatal(err)
	}
	blocks := []engine.Block{{
		ID: "distilled_log", Role: "system", Order: 40, Enabled: true, Level: engine.LevelTrim,
		Source: engine.Source{Type: "distilled_log"}, Template: "<M>{{items}}</M>",
	}}
	lists := listInputs(blocks, nil, root, "阿离", 1)
	if len(lists) != 1 || len(lists[0].Items) != 2 {
		t.Fatalf("want 2 day items, got %+v", lists)
	}
	if lists[0].Floor != 1 || lists[0].FloorFromHead {
		t.Fatalf("diary floor must protect the newest 1 day from the tail: %+v", lists[0])
	}
	if lists[0].Items[0].EvictRank >= lists[0].Items[1].EvictRank {
		t.Fatalf("oldest day must have the lowest evict rank: %+v", lists[0].Items)
	}
}

func TestRenderBlocksInjectsTimeAndCharacter(t *testing.T) {
	root := t.TempDir()
	base := charBase(root, "阿离")
	if err := saveCharCard(base, CharCard{Name: "阿离", Description: "温柔的角色卡"}); err != nil {
		t.Fatal(err)
	}
	blocks := []engine.Block{
		{ID: "time", Source: engine.Source{Type: "static"}, Template: "今天是 {{isodate}} {{weekday}}"},
		{ID: "character", Source: engine.Source{Type: "character"}, Template: "{{character_card}}"},
	}
	out := renderBlocks(root, "阿离", "Roy", blocks)
	if strings.Contains(out[0].Content, "{{") || !strings.Contains(out[0].Content, "今天是") {
		t.Fatalf("time macros not resolved: %q", out[0].Content)
	}
	if out[1].Content != "温柔的角色卡" {
		t.Fatalf("character card not injected: %q", out[1].Content)
	}
}

func TestSaveImagesPersistsMedia(t *testing.T) {
	root := t.TempDir()
	// 1x1 transparent png
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	paths, urls, _ := saveImages(root, "阿离", []string{"data:image/png;base64," + png})
	if len(paths) != 1 || len(urls) != 1 {
		t.Fatalf("want 1 path/url, got %v %v", paths, urls)
	}
	if _, err := os.Stat(filepath.Join(charBase(root, "阿离"), paths[0])); err != nil {
		t.Fatalf("media not persisted: %v", err)
	}
	if !strings.HasPrefix(urls[0], "data:image/png;base64,") {
		t.Fatalf("bad data url: %s", urls[0])
	}
}

func TestStoreArchiveAndLoad(t *testing.T) {
	root := t.TempDir()
	st := store.New(root)
	if _, err := st.AppendChat("阿离", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	name, err := st.Archive("阿离")
	if err != nil || name == "" {
		t.Fatalf("archive failed: %v %q", err, name)
	}
	msgs, err := st.LoadArchive("阿离", name)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Fatalf("load archive failed: %v %v", err, msgs)
	}
	cur, _ := st.LoadAll("阿离")
	if len(cur) != 0 {
		t.Fatalf("chat should be fresh after archive, got %d", len(cur))
	}
}

func TestMigrateLegacyChats(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "chats"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "chats", "旧角色.jsonl"), []byte(`{"role":"user","text":"老数据","time":"2026-01-01T00:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := store.New(root)
	st.MigrateLegacyChats()
	msgs, _ := st.LoadAll("旧角色")
	if len(msgs) != 1 || msgs[0].Text != "老数据" {
		t.Fatalf("legacy migration failed: %v", msgs)
	}
}

func TestResolveMCPTimeoutCancelsRequest(t *testing.T) {
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	s := settings.Settings{
		MCPEnabled:   true,
		MCPURL:       srv.URL,
		MCPTimeout:   1,
		MCPTopK:      10,
		MCPThreshold: -1,
	}
	started := time.Now()
	_, info := resolveMCP(context.Background(), s, []engine.Message{{Role: "user", Content: "query"}})
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("MCP timeout was not honored: %s", elapsed)
	}

	errText, _ := info["error"].(string)
	if !strings.Contains(errText, "mcp search timeout") {
		t.Fatalf("expected timeout error, got %v", info)
	}
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("test server handler did not finish")
	}
}
