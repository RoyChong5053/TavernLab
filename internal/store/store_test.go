package store

import (
	"os"
	"path/filepath"
	"testing"
)

// writeChat writes raw JSONL rows for a session and returns the file path.
func writeChat(t *testing.T, s *Store, session string, rows ...string) string {
	t.Helper()
	p := filepath.Join(s.CharDir(session), "chat.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	content := ""
	for _, r := range rows {
		content += r + "\n"
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDropTrailingAssistant(t *testing.T) {
	s := New(t.TempDir())
	p := writeChat(t, s, "test",
		`{"id":"u1","role":"user","text":"hi","time":"t"}`,
		`{"id":"a1","role":"assistant","text":"hello","time":"t"}`,
		`{"id":"u2","role":"user","text":"again","time":"t"}`,
		`{"id":"a2","role":"assistant","text":"partial","time":"t"}`,
	)

	lastUser, removed, hasUser, err := s.DropTrailingAssistant("test")
	if err != nil {
		t.Fatal(err)
	}
	if !hasUser {
		t.Fatal("expected hasUser=true")
	}
	if lastUser.ID != "u2" || lastUser.Text != "again" {
		t.Fatalf("lastUser = %+v", lastUser)
	}
	if len(removed) != 1 || removed[0].ID != "a2" {
		t.Fatalf("removed = %+v", removed)
	}
	all, _ := s.LoadAll("test")
	if len(all) != 3 || all[len(all)-1].ID != "u2" {
		t.Fatalf("after drop: %+v", all)
	}

	// Nothing trailing to drop: the file must be left byte-for-byte untouched
	// (no point rewriting, and it keeps the running server's readers happy).
	before, _ := os.ReadFile(p)
	lastUser2, removed2, hasUser2, err := s.DropTrailingAssistant("test")
	if err != nil || !hasUser2 || lastUser2.ID != "u2" || len(removed2) != 0 {
		t.Fatalf("second drop: user=%+v removed=%+v err=%v", lastUser2, removed2, err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("file changed when there was nothing to drop")
	}
}

func TestDropTrailingAssistantMultipleAndEmpty(t *testing.T) {
	s := New(t.TempDir())
	writeChat(t, s, "test",
		`{"id":"u1","role":"user","text":"hi","time":"t"}`,
		`{"id":"a1","role":"assistant","text":"","time":"t"}`,
		`{"id":"a2","role":"assistant","text":"partial","time":"t"}`,
	)
	lastUser, removed, hasUser, err := s.DropTrailingAssistant("test")
	if err != nil || !hasUser || lastUser.ID != "u1" {
		t.Fatalf("user=%+v err=%v", lastUser, err)
	}
	if len(removed) != 2 {
		t.Fatalf("expected both trailing assistants removed, got %+v", removed)
	}
	all, _ := s.LoadAll("test")
	if len(all) != 1 || all[0].ID != "u1" {
		t.Fatalf("after drop: %+v", all)
	}
}

func TestDeleteMessage(t *testing.T) {
	s := New(t.TempDir())
	writeChat(t, s, "test",
		`{"id":"u1","role":"user","text":"hi","time":"t"}`,
		`{"id":"a1","role":"assistant","text":"hello","time":"t"}`,
		`{"id":"u2","role":"user","text":"again","time":"t"}`,
	)
	ok, err := s.DeleteMessage("test", "a1")
	if err != nil || !ok {
		t.Fatalf("delete a1: ok=%v err=%v", ok, err)
	}
	all, _ := s.LoadAll("test")
	if len(all) != 2 || all[0].ID != "u1" || all[1].ID != "u2" {
		t.Fatalf("after delete: %+v", all)
	}
	// Missing id and bad id shapes report false without touching the file.
	if ok, err := s.DeleteMessage("test", "nope"); err != nil || ok {
		t.Fatalf("missing id: ok=%v err=%v", ok, err)
	}
	if ok, err := s.DeleteMessage("test", "../x"); err != nil || ok {
		t.Fatalf("bad id: ok=%v err=%v", ok, err)
	}
	if ok, err := s.DeleteMessage("missing", "u1"); err != nil || ok {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	all, _ = s.LoadAll("test")
	if len(all) != 2 {
		t.Fatalf("file changed on no-op delete: %+v", all)
	}
}

func TestDropTrailingAssistantNoUser(t *testing.T) {
	s := New(t.TempDir())
	if _, _, hasUser, err := s.DropTrailingAssistant("missing"); err != nil || hasUser {
		t.Fatalf("missing file: hasUser=%v err=%v", hasUser, err)
	}
	writeChat(t, s, "onlyai", `{"id":"a1","role":"assistant","text":"x","time":"t"}`)
	if _, _, hasUser, err := s.DropTrailingAssistant("onlyai"); err != nil || hasUser {
		t.Fatalf("assistant-only: hasUser=%v err=%v", hasUser, err)
	}
}
