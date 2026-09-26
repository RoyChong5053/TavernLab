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

func TestNormalizeBlocksCoercesListLevels(t *testing.T) {
	in := []engine.Block{
		{ID: "rag_mcp", Role: "system", Order: 55, Enabled: true, Level: engine.LevelLocked, Source: engine.Source{Type: "mcp"}},
		{ID: "chat", Role: "user", Order: 100, Enabled: true, Level: engine.LevelElastic, Source: engine.Source{Type: "chat"}},
		{ID: "distilled_state", Role: "system", Order: 40, Enabled: true, Level: engine.LevelTrim, Source: engine.Source{Type: "distilled_state"}},
	}
	out := normalizeBlocks(in)
	want := map[string]engine.Level{
		"rag_mcp":         engine.LevelTrim,
		"chat":            engine.LevelTrim,
		"distilled_state": engine.LevelLocked,
	}
	for _, b := range out {
		if w, ok := want[b.ID]; ok && b.Level != w {
			t.Fatalf("%s level = %d, want %d", b.ID, b.Level, w)
		}
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

func TestAppendChatIDDedupesRetriedTurn(t *testing.T) {
	root := t.TempDir()
	st := store.New(root)
	const id = "turn-abc123"
	first, err := st.AppendChatID("阿离", id, "user", "在吗")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != id {
		t.Fatalf("client id not honoured: %+v", first)
	}
	// The phone resends the same turn because it never saw the ack.
	again, err := st.AppendChatID("阿离", id, "user", "在吗")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Time != first.Time {
		t.Fatalf("retry did not return the original row: %+v vs %+v", first, again)
	}
	msgs, _ := st.LoadAll("阿离")
	if len(msgs) != 1 {
		t.Fatalf("retry duplicated the row: %d rows", len(msgs))
	}
}

func TestFindTurnReportsStoredReply(t *testing.T) {
	root := t.TempDir()
	st := store.New(root)
	// Unknown id: nothing found, so a fresh turn must be generated.
	if _, _, hasRow, _ := st.FindTurn("阿离", "nope"); hasRow {
		t.Fatal("empty session reported a stored turn")
	}
	u, _ := st.AppendChatID("阿离", "turn-1", "user", "hi")
	// User row stored but generation died: hasRow, no reply.
	_, _, hasRow, hasReply := st.FindTurn("阿离", "turn-1")
	if !hasRow || hasReply {
		t.Fatalf("want row without reply, got row=%v reply=%v", hasRow, hasReply)
	}
	if _, err := st.AppendChat("阿离", "assistant", "hello back"); err != nil {
		t.Fatal(err)
	}
	row, reply, hasRow, hasReply := st.FindTurn("阿离", "turn-1")
	if !hasRow || !hasReply {
		t.Fatalf("want row with reply, got row=%v reply=%v", hasRow, hasReply)
	}
	if row.ID != u.ID || reply.Text != "hello back" {
		t.Fatalf("bad turn pair: %+v %+v", row, reply)
	}
	// A junk id must never match, and must not break the append path.
	if _, err := st.AppendChatID("阿离", "bad id/with slash", "user", "x"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := st.LoadAll("阿离")
	if len(msgs) != 3 {
		t.Fatalf("junk id should append normally: %d rows", len(msgs))
	}
	if msgs[2].ID == "bad id/with slash" {
		t.Fatal("unsanitised client id was stored verbatim")
	}
}

func TestLoadMediaAsDataURLsRoundTrips(t *testing.T) {
	root := t.TempDir()
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	paths, _, _ := saveImages(root, "阿离", []string{"data:image/png;base64," + png})
	urls := loadMediaAsDataURLs(root, "阿离", paths)
	if len(urls) != 1 {
		t.Fatalf("want 1 rebuilt url, got %d", len(urls))
	}
	if !strings.HasPrefix(urls[0], "data:image/png;base64,") {
		t.Fatalf("bad rebuilt url: %s", urls[0])
	}
	if !strings.HasSuffix(urls[0], png) {
		t.Fatalf("rebuilt url lost the bytes: %s", urls[0])
	}
	// A missing file is skipped rather than poisoning the upstream request.
	if got := loadMediaAsDataURLs(root, "阿离", []string{"media/gone.png"}); len(got) != 0 {
		t.Fatalf("missing media should be skipped, got %v", got)
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
