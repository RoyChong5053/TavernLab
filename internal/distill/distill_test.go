package distill

import (
	"strings"
	"testing"
)

const prevSheet = `[USER STATE]
[20-09-2026] mood: tired; location: home
[21-09-2026] mood: ok; location: out
[22-09-2026] mood: good

[LOG]
[20-09-2026 08:00] ate breakfast
[21-09-2026 09:00] coded the engine
[22-09-2026 10:00] deployed to m64`

// nextSheet mimics the buggy model output: it dropped day 20 entirely, dropped
// day 21's LOG entry, kept 22, and added 23.
const nextSheet = `[USER STATE]
[22-09-2026] mood: good
[23-09-2026] mood: great

[LOG]
[22-09-2026 10:00] deployed to m64
[23-09-2026 11:00] shipped the fix`

func TestMergeRestoresDroppedDays(t *testing.T) {
	got := Merge(prevSheet, nextSheet)
	for _, want := range []string{
		"[20-09-2026] mood: tired", "[21-09-2026] mood: ok", "[23-09-2026] mood: great",
		"[20-09-2026 08:00] ate breakfast",
		"[21-09-2026 09:00] coded the engine",
		"[22-09-2026 10:00] deployed to m64",
		"[23-09-2026 11:00] shipped the fix",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Merge lost %q\n---\n%s", want, got)
		}
	}
	if n := DayCount(got); n != 4 {
		t.Fatalf("want 4 days after merge, got %d\n%s", n, got)
	}
}

func TestMergeNewerTimestampWins(t *testing.T) {
	prev := "[LOG]\n[22-09-2026 10:00] old text\n"
	next := "[LOG]\n[22-09-2026 10:00] corrected text\n"
	got := Merge(prev, next)
	if !strings.Contains(got, "corrected text") || strings.Contains(got, "old text") {
		t.Fatalf("same-timestamp entry should be replaced, got:\n%s", got)
	}
}

func TestMergeChronological(t *testing.T) {
	got := Merge(prevSheet, nextSheet)
	i20 := strings.Index(got, "20-09-2026 08:00")
	i23 := strings.Index(got, "23-09-2026 11:00")
	if i20 < 0 || i23 < 0 || i20 > i23 {
		t.Fatalf("logs out of order:\n%s", got)
	}
}

func TestEnforceWindowKeepsFloor(t *testing.T) {
	var b strings.Builder
	b.WriteString("[USER STATE]\n")
	for _, d := range []string{"18-09-2026", "19-09-2026", "20-09-2026", "21-09-2026", "22-09-2026"} {
		b.WriteString("[" + d + "] " + strings.Repeat("x", 200) + "\n")
	}
	b.WriteString("\n[LOG]\n")
	for _, d := range []string{"18-09-2026", "19-09-2026", "20-09-2026", "21-09-2026", "22-09-2026"} {
		b.WriteString("[" + d + " 10:00] " + strings.Repeat("y", 200) + "\n")
	}
	got := EnforceWindow(b.String(), 100, 3)
	if n := DayCount(got); n != 3 {
		t.Fatalf("want floor of 3 days, got %d\n%s", n, got)
	}
	if strings.Contains(got, "18-09-2026") || strings.Contains(got, "19-09-2026") {
		t.Fatalf("oldest days should be evicted:\n%s", got)
	}
	if !strings.Contains(got, "22-09-2026") || !strings.Contains(got, "20-09-2026") {
		t.Fatalf("newest 3 days must survive:\n%s", got)
	}
}

func TestEnforceWindowKeepsAllWhenUnderBudget(t *testing.T) {
	got := EnforceWindow(prevSheet, 100000, 3)
	if n := DayCount(got); n != 3 {
		t.Fatalf("want 3 days preserved, got %d", n)
	}
}

func TestIsValid(t *testing.T) {
	if IsValid("Sure! Here is a summary of the chat.") {
		t.Fatal("prose must be invalid")
	}
	if IsValid("") {
		t.Fatal("empty must be invalid")
	}
	if !IsValid(nextSheet) {
		t.Fatal("proper sheet must be valid")
	}
}
