package distill

import (
	"fmt"
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

// TestMergeAcceptsISODates is the regression for the original bug: the model
// emitted ISO (YYYY-MM-DD) log dates copied from the raw message timestamps and
// the parser silently discarded them.
func TestMergeAcceptsISODates(t *testing.T) {
	prev := "[USER STATE]\n[24-09-2026] busy\n\n[LOG]\n[24-09-2026 10:00] did a thing"
	next := "[USER STATE]\n[2026-09-25] calmer\n\n[LOG]\n[2026-09-25 08:00] ate breakfast"
	got := Merge(prev, next)
	for _, want := range []string{
		"[24-09-2026] busy", "[25-09-2026] calmer",
		"[24-09-2026 10:00] did a thing",
		"[25-09-2026 08:00] ate breakfast",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ISO entry lost %q\n---\n%s", want, got)
		}
	}
	if DayCount(got) != 2 {
		t.Fatalf("want 2 days, got %d\n%s", DayCount(got), got)
	}
}

func TestApplyDeltaAppendsAndReplacesState(t *testing.T) {
	prev := "[USER STATE]\n[25-09-2026] old state\n\n[LOG]\n[25-09-2026 08:00] old entry"
	delta := "[STATE]\n[25-09-2026] new state\n\n[LOG]\n[2026-09-25 09:00] new entry"
	got := ApplyDelta(prev, delta)
	if !strings.Contains(got, "[25-09-2026] new state") || strings.Contains(got, "old state") {
		t.Fatalf("state must be replaced in place:\n%s", got)
	}
	if !strings.Contains(got, "[25-09-2026 08:00] old entry") {
		t.Fatalf("previous log must survive:\n%s", got)
	}
	if !strings.Contains(got, "[25-09-2026 09:00] new entry") {
		t.Fatalf("delta log must be appended and normalised:\n%s", got)
	}
}

func TestRetainKeepsFloor(t *testing.T) {
	var b strings.Builder
	b.WriteString("[USER STATE]\n")
	for _, d := range []string{"18-09-2026", "19-09-2026", "20-09-2026", "21-09-2026", "22-09-2026"} {
		b.WriteString("[" + d + "] " + strings.Repeat("x", 200) + "\n")
	}
	b.WriteString("\n[LOG]\n")
	for _, d := range []string{"18-09-2026", "19-09-2026", "20-09-2026", "21-09-2026", "22-09-2026"} {
		b.WriteString("[" + d + " 10:00] " + strings.Repeat("y", 200) + "\n")
	}
	got := Retain(b.String(), 100, 3, 0, 0, 0)
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

func TestRetainKeepsAllWhenUnderBudget(t *testing.T) {
	got := Retain(prevSheet, 100000, 3, 0, 0, 0)
	if n := DayCount(got); n != 3 {
		t.Fatalf("want 3 days preserved, got %d", n)
	}
}

func TestRetainStateCapKeepsLogs(t *testing.T) {
	sheet := "[USER STATE]\n[01-09-2026] a\n[02-09-2026] b\n[03-09-2026] c\n\n[LOG]\n[01-09-2026 10:00] x"
	got := Retain(sheet, 0, 3, 2, 0, 0)
	if strings.Contains(got, "[01-09-2026] a") {
		t.Fatalf("oldest state must be dropped:\n%s", got)
	}
	if !strings.Contains(got, "[02-09-2026] b") || !strings.Contains(got, "[03-09-2026] c") {
		t.Fatalf("newest 2 states must survive:\n%s", got)
	}
	if !strings.Contains(got, "[01-09-2026 10:00] x") {
		t.Fatalf("log of a state-capped day must survive:\n%s", got)
	}
}

func TestRetainPerDayCaps(t *testing.T) {
	var b strings.Builder
	b.WriteString("[LOG]\n")
	for i := 0; i < 5; i++ {
		b.WriteString(fmt.Sprintf("[25-09-2026 0%d:00] %s\n", i+1, strings.Repeat("z", 20)))
	}
	got := Retain(b.String(), 0, 3, 0, 2, 5)
	if n := strings.Count(got, "[25-09-2026 "); n != 2 {
		t.Fatalf("want 2 newest entries per day, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "[25-09-2026 05:00]") || strings.Contains(got, "[25-09-2026 01:00]") {
		t.Fatalf("per-day cap must keep the newest:\n%s", got)
	}
	if strings.Contains(got, strings.Repeat("z", 6)) {
		t.Fatalf("entry text must be capped:\n%s", got)
	}
}

func TestRebuildRecoversDroppedDays(t *testing.T) {
	a := "[USER STATE]\n[20-09-2026] s20\n\n[LOG]\n[20-09-2026 08:00] e20"
	c := "[USER STATE]\n[22-09-2026] s22\n\n[LOG]\n[22-09-2026 09:00] e22"
	got := Rebuild([]string{a, c})
	if DayCount(got) != 2 || !strings.Contains(got, "e20") || !strings.Contains(got, "e22") {
		t.Fatalf("rebuild must union all sheets:\n%s", got)
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
	// The delta output shape must also be accepted.
	if !IsValid("[STATE]\n[25-09-2026] ok\n\n[LOG]\n[25-09-2026 10:00] did a thing") {
		t.Fatal("delta sheet must be valid")
	}
}

func TestUnparsedDatedLines(t *testing.T) {
	sheet := "[LOG]\n[25/09/2026 10:00] slash date"
	got := UnparsedDatedLines(sheet)
	if len(got) != 1 {
		t.Fatalf("want 1 unparsed dated line, got %v", got)
	}
	// A properly parsed ISO line must not be reported.
	if got := UnparsedDatedLines("[LOG]\n[2026-09-25 10:00] fine"); len(got) != 0 {
		t.Fatalf("parsed ISO line flagged as unparsed: %v", got)
	}
}
