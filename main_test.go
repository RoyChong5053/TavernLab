package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/store"
)

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
