package distill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildUserSectionOrderAndOmission locks the input shape the new prompt
// depends on. Order matters (state -> recent -> movement -> new) and, more
// subtly, an EMPTY optional section must be omitted entirely: a literal
// "[Recent Entries] (none)" invites the model to narrate its own emptiness, and
// an empty "[Movement]" block still invites trace transcription.
func TestBuildUserSectionOrderAndOmission(t *testing.T) {
	sheet := "[USER STATE]\n[30-09-2026] state line\n"
	got := BuildUser(
		"state line",
		"[29-09-2026 20:00] earlier entry",
		"[Recent Movement] (Asia/Kuala_Lumpur, ...)\n- 10:00–11:00 @Somewhere (x) — 停留 1 小时",
		"**user** [30-09-2026 21:00]: hello",
	)
	for _, want := range []string{
		"[Current State]", "state line",
		"[Recent Entries]", "[29-09-2026 20:00] earlier entry",
		"[Recent Movement]", "@Somewhere",
		"[New Messages]", "hello",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Ordering: state before recent before movement before new.
	idx := func(s string) int { return strings.Index(got, s) }
	if !(idx("[Current State]") < idx("[Recent Entries]") &&
		idx("[Recent Entries]") < idx("[Recent Movement]") &&
		idx("[Recent Movement]") < idx("[New Messages]")) {
		t.Errorf("sections out of order:\n%s", got)
	}
	_ = sheet
}

// TestBuildUserOmitsEmptyOptionalSections is the guard for the "empty section
// invites noise" rule.
func TestBuildUserOmitsEmptyOptionalSections(t *testing.T) {
	got := BuildUser("", "", "", "**user**: hi")
	if strings.Contains(got, "[Recent Entries]") {
		t.Errorf("empty recent log should be omitted:\n%s", got)
	}
	if strings.Contains(got, "[Recent Movement]") {
		t.Errorf("empty movement should be omitted:\n%s", got)
	}
	// The bare (none yet) placeholder for state is pre-existing behaviour and
	// must survive.
	if !strings.Contains(got, "(none yet)") {
		t.Errorf("empty state should keep the (none yet) placeholder:\n%s", got)
	}
}

// TestRecentLogEntriesBoundedTail covers both the continuity purpose and the
// safety property: the extractor must never see the whole diary, or its output
// could grow with history.
func TestRecentLogEntriesBoundedTail(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "characters", "C")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("[USER STATE]\n[01-09-2026] s\n\n[LOG]\n")
	for i := 1; i <= 10; i++ {
		// fmt, not rune arithmetic: 10 must be "10:00", not "010:00" (an
		// invalid hour is silently dropped by the tolerant parser, which would
		// quietly make this test assert the wrong tail).
		fmt.Fprintf(&sb, "[01-09-2026 %02d:00] entry %d\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "distilled.md"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tail := RecentLogEntries(root, "C", 3)
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), tail)
	}
	// Newest last, and it must be the newest entry (#10).
	if lines[2] != "[01-09-2026 10:00] entry 10" {
		t.Errorf("tail should end at the newest entry #10, got %q", lines[2])
	}
	if RecentLogEntries(root, "C", 0) != "" {
		t.Error("n<=0 must return empty (no unbounded tail)")
	}
}
