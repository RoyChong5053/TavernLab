package vectorize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/store"
)

func testMsgs() []store.ChatMessage {
	return []store.ChatMessage{
		{Role: "user", Text: "今晚吃什么", Time: "2026-10-06T19:00:00+08:00"},
		{Role: "assistant", Text: "吃面吧", Time: "2026-10-06T19:01:00+08:00"},
		{Role: "distilled_memory", Text: "[LOG]\n[06-10-2026 19:00] something"},
		{Role: "user", Text: "  ", Time: "2026-10-06T19:02:00+08:00"},
	}
}

func TestBuildIncrementalSkipsNonDialogue(t *testing.T) {
	out := BuildIncremental("Leer", "Roy", testMsgs(), 120, 124,
		"[LOG]\n[06-10-2026 19:00] 吃面", "ok", "[Recent Movement]\n在家", "PJU 8", time.Now())
	if strings.Contains(out, "distilled_memory") && strings.Contains(out, "**distilled_memory**") {
		t.Fatalf("distilled rows must not render as dialogue:\n%s", out)
	}
	for _, want := range []string{"## raw", "## distilled", "## movement", "吃面", "PJU 8", "msgs: 120-124", "distill: ok"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestBuildIncrementalKeepsFailedDelta(t *testing.T) {
	out := BuildIncremental("Leer", "Roy", testMsgs(), 1, 2, "prose without dates", "parse-failed", "", "", time.Now())
	if !strings.Contains(out, "parse-failed") || !strings.Contains(out, "prose without dates") {
		t.Fatalf("failed delta must stay auditable:\n%s", out)
	}
}

func TestWriteAndPruneTmp(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		name := IncrementalName(time.Date(2026, 10, 6, 19, i, 0, 0, time.UTC), i, i+1)
		if _, err := WriteTmp(root, "Leer", name, "# body"); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneTmp(root, "Leer", 3); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(TmpDir(root, "Leer")))
	if len(entries) != 3 {
		t.Fatalf("expected 3 files kept, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Name(), "190200") {
		t.Fatalf("oldest should be pruned first, kept %v", entries[0].Name())
	}
}
