package main

import (
	"strings"
	"testing"

	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/settings"
	"github.com/RoyChong5053/TavernLab/internal/websearch"
)

// The /web_search command switches the web collection in and out of the
// character's recall set, and it must not touch the character's own
// collections: a character that recalls its chat memory keeps doing so.
func TestSetWebRecallKeepsOtherCollections(t *testing.T) {
	root := t.TempDir()
	base := charBase(root, "阿离")
	prof := DefaultProfile()
	prof.RAG.Collections = []string{"ali_chat", "ali_knowledge"}
	if err := saveCharProfile(base, prof); err != nil {
		t.Fatal(err)
	}
	s := settings.Settings{}

	if err := setWebRecall(root, s, "阿离", true); err != nil {
		t.Fatal(err)
	}
	if got := recallCSV(root, s, "阿离"); got != "ali_chat,ali_knowledge,web_cache" {
		t.Fatalf("after on: recall = %q", got)
	}

	// Turning it on twice must not double it.
	if err := setWebRecall(root, s, "阿离", true); err != nil {
		t.Fatal(err)
	}
	if got := recallCSV(root, s, "阿离"); got != "ali_chat,ali_knowledge,web_cache" {
		t.Fatalf("after on twice: recall = %q", got)
	}

	if err := setWebRecall(root, s, "阿离", false); err != nil {
		t.Fatal(err)
	}
	if got := recallCSV(root, s, "阿离"); got != "ali_chat,ali_knowledge" {
		t.Fatalf("after off: recall = %q", got)
	}
}

// A preset saved before the web_search module existed must still get it: the
// module is compiled in, and only the note kind can be deleted by hand.
func TestSeedMissingKinds(t *testing.T) {
	old := []engine.Module{
		{ID: "system", Kind: engine.KindSystem, Order: 0},
		{ID: "rag", Kind: engine.KindRAG, Order: 55},
	}
	defaults := engine.DefaultModules()
	got := seedMissingKinds(old, defaults)

	hasWS := false
	for _, m := range got {
		if m.Kind == engine.KindWebSearch {
			hasWS = true
		}
	}
	if !hasWS {
		t.Fatalf("the web_search module was not seeded: %+v", got)
	}
	// The existing modules are untouched.
	if got[0].ID != "system" || got[1].ID != "rag" {
		t.Fatalf("the stored preset was disturbed: %+v", got)
	}
	// Seeding twice must not double it.
	again := seedMissingKinds(got, defaults)
	n := 0
	for _, m := range again {
		if m.Kind == engine.KindWebSearch {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the web_search module was seeded %d times", n)
	}
}

// keep_turns is read from the module, and defaults to "this turn only".
func TestWebSearchKeepTurns(t *testing.T) {
	if got := webSearchKeepTurns(nil); got != 1 {
		t.Fatalf("no module: keep_turns = %d, want 1", got)
	}
	mods := []engine.Module{{Kind: engine.KindWebSearch, KeepTurns: 4}}
	if got := webSearchKeepTurns(mods); got != 4 {
		t.Fatalf("keep_turns = %d, want 4", got)
	}
	mods = []engine.Module{{Kind: engine.KindWebSearch}}
	if got := webSearchKeepTurns(mods); got != 1 {
		t.Fatalf("unset keep_turns = %d, want 1", got)
	}
}

// The turn number a search belongs to is the number of user messages.
func TestUserTurnCount(t *testing.T) {
	turns := []engine.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "..."},
		{Role: "web_search", Content: "note"},
		{Role: "user", Content: "two"},
	}
	if got := userTurnCount(turns); got != 2 {
		t.Fatalf("userTurnCount = %d, want 2", got)
	}
}

// The record is for the human reading the transcript; it says what was
// searched, or that the web was switched off.
func TestWebSearchRecordText(t *testing.T) {
	on := websearch.Command{Mode: websearch.ModeOn, Query: "RTX spark laptop"}
	if got := webSearchRecordText(on, on.Query); !strings.Contains(got, "RTX spark laptop") {
		t.Fatalf("the record must name the query: %q", got)
	}
	off := websearch.Command{Mode: websearch.ModeOff}
	if got := webSearchRecordText(off, ""); !strings.Contains(got, "off") {
		t.Fatalf("the record must say the web was switched off: %q", got)
	}
}
