// Package distill implements the Distilled Memory feature (ported from the
// SillyTavern "Distilled-Memory" extension): recent chat is periodically
// distilled by the LLM into a compact, dated personal fact sheet that is
// injected into the prompt as the `distilled` block.
//
// Storage is per-character and file-based so data/ stays rclone-friendly:
//
//	characters/<name>/distilled.md        the current fact sheet
//	characters/<name>/distilled.meta.json last-run bookkeeping
package distill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/store"
)

// DefaultPrompt is the Memory Custodian prompt. {{user}} / {{isodate}} /
// {{weekday}} / {{time}} are filled at run time; {{maxchars}} is the output
// character budget.
const DefaultPrompt = `You are {{user}}'s automatic diary. Maintain a factual, dated record of what {{user}} explicitly states or does, in two sections: [USER STATE] (current snapshot by date) and [LOG] (timestamped event entries). This record is later used for long-term memory, so accuracy and date structure matter.

## Input
- [Previous Fact Sheet]: existing record. Treat it as the base state.
- [New Messages]: recent conversation.
- LIVE TIMESTAMP: {{isodate}} ({{weekday}}) {{time}} UTC+8 — current date/time.

## Core principles
- Incremental update: preserve valid existing entries unless explicitly corrected. Do not rebuild from scratch.
- Only record facts explicitly stated by {{user}}. Ignore assistant/character guesses, roleplay, hypotheticals, filler, and weak confirmations.
- Do not invent or infer anything: no causes, plans, preferences, emotions unless {{user}} stated them.
- Distinguish certainty: "I don't use X anymore" = state changed; "I used to" = historical; "I might" = intention, not fact.
- If a conflict arises: {{user}}'s explicit correction > newer explicit statement about the same fact > preserve old fact.

## [USER STATE]
- Snapshot of current status (mood, location, active project, health, current activity) true for a given day.
- Format: each line starts with [DD-MM-YYYY] and no time.
- Update today's line in place; add a new line when a new day begins. NEVER remove a previous day's line.

## [LOG]
- Detailed chronological event log, the diary part.
- Format: every entry starts with full date+time: [DD-MM-YYYY HH:MM] <event in your own words>.
- Use the message timestamp when known; otherwise infer from LIVE TIMESTAMP.
- Cumulative and append-only: add new entries, NEVER delete or rewrite an earlier entry. One line per distinct event. Do not invent causation.
- Correct a past entry only if {{user}} explicitly corrects it.

## Retention (read carefully)
- Do NOT trim, summarise away, or delete any day on your own. Always reproduce every day you were given, plus the new events.
- The application enforces the budget afterwards by dropping the oldest whole days in code; it always keeps at least {{retain_days}} days.
- Your only job is to append the new information accurately and keep the exact date format. The {{maxchars}} character limit is handled by the app, not by you.

## Output
Output ONLY the updated fact sheet in this exact format:

[USER STATE]
[DD-MM-YYYY] <state...>

[LOG]
[DD-MM-YYYY HH:MM] <event...>

Omit empty sections. No explanation, no commentary.`

// Meta is distillation bookkeeping for one character.
type Meta struct {
	LastRun    string `json:"last_run,omitempty"`
	LastIndex  int    `json:"last_index"` // messages already distilled
	UserTurns  int    `json:"user_turns"` // user turns since last run
	Runs       int    `json:"runs"`
	LastStatus string `json:"last_status,omitempty"`
}

func dir(root, char string) string {
	return filepath.Join(root, "characters", store.CleanSession(char))
}
func sheetPath(root, char string) string { return filepath.Join(dir(root, char), "distilled.md") }
func metaPath(root, char string) string  { return filepath.Join(dir(root, char), "distilled.meta.json") }

// Load returns the current fact sheet ("" when none yet).
func Load(root, char string) string {
	b, err := os.ReadFile(sheetPath(root, char))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Save writes the fact sheet.
func Save(root, char, sheet string) error {
	if err := os.MkdirAll(dir(root, char), 0o755); err != nil {
		return err
	}
	return os.WriteFile(sheetPath(root, char), []byte(strings.TrimSpace(sheet)+"\n"), 0o644)
}

// LoadMeta reads bookkeeping (zero value when absent).
func LoadMeta(root, char string) Meta {
	var m Meta
	b, err := os.ReadFile(metaPath(root, char))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// SaveMeta writes bookkeeping.
func SaveMeta(root, char string, m Meta) error {
	if err := os.MkdirAll(dir(root, char), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(metaPath(root, char), b, 0o644)
}

// FormatTimeline renders messages in the extension's raw shape:
// `**Speaker** [YYYY-MM-DD HH:MM]: text`.
func FormatTimeline(char, userName string, msgs []store.ChatMessage) string {
	if userName == "" {
		userName = "user"
	}
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		speaker := userName
		if m.Role == "assistant" {
			speaker = char
		}
		ts := m.Time
		if t, err := time.Parse(time.RFC3339, m.Time); err == nil {
			ts = t.Format("2006-01-02 15:04")
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		sb.WriteString("**" + speaker + "** [" + ts + "]: " + text + "\n")
	}
	return strings.TrimSpace(sb.String())
}

// ---- retention guards -----------------------------------------------------
//
// The LLM is told to append-only, but models will still drop whole days
// (especially [LOG] entries) when they feel the sheet is long. These guards
// make retention deterministic in code: Merge restores anything the model
// dropped, and EnforceWindow trims by whole oldest days but never below a
// floor, so the character never gets "goldfish brain".

var (
	stateRe = regexp.MustCompile(`^\[(\d{2}-\d{2}-\d{4})\]\s*(.*)$`)
	logRe   = regexp.MustCompile(`^\[(\d{2}-\d{2}-\d{4})\s+(\d{2}:\d{2})\]\s*(.*)$`)
)

type dayBlock struct {
	date  string
	state string   // full "[DD-MM-YYYY] ..." line
	logs  []string // full "[DD-MM-YYYY HH:MM] ..." lines
}

func parseSheet(s string) map[string]*dayBlock {
	days := map[string]*dayBlock{}
	get := func(d string) *dayBlock {
		if b := days[d]; b != nil {
			return b
		}
		b := &dayBlock{date: d}
		days[d] = b
		return b
	}
	for _, raw := range strings.Split(s, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || t == "[USER STATE]" || t == "[LOG]" || strings.HasPrefix(t, "[End of") {
			continue
		}
		if m := logRe.FindStringSubmatch(t); m != nil {
			b := get(m[1])
			b.logs = append(b.logs, t)
			continue
		}
		if m := stateRe.FindStringSubmatch(t); m != nil {
			get(m[1]).state = t
		}
	}
	return days
}

func dateOrder(d string) string {
	if t, err := time.Parse("02-01-2006", d); err == nil {
		return t.Format("2006-01-02")
	}
	return d
}

func logOrder(line string) string {
	if m := logRe.FindStringSubmatch(line); m != nil {
		if t, err := time.Parse("02-01-2006 15:04", m[1]+" "+m[2]); err == nil {
			return t.Format("2006-01-02 15:04")
		}
		return m[1] + " " + m[2]
	}
	return line
}

func sortedDates(days map[string]*dayBlock) []string {
	out := make([]string, 0, len(days))
	for d := range days {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return dateOrder(out[i]) < dateOrder(out[j]) })
	return out
}

func renderSheet(days map[string]*dayBlock, order []string) string {
	var sb strings.Builder
	wroteState := false
	for _, d := range order {
		if b := days[d]; b != nil && b.state != "" {
			if !wroteState {
				sb.WriteString("[USER STATE]\n")
				wroteState = true
			}
			sb.WriteString(b.state + "\n")
		}
	}
	if wroteState {
		sb.WriteString("\n")
	}
	wroteLog := false
	for _, d := range order {
		b := days[d]
		if b == nil {
			continue
		}
		for _, l := range b.logs {
			if !wroteLog {
				sb.WriteString("[LOG]\n")
				wroteLog = true
			}
			sb.WriteString(l + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func cloneBlock(b *dayBlock) *dayBlock {
	nb := &dayBlock{date: b.date, state: b.state}
	nb.logs = append(nb.logs, b.logs...)
	return nb
}

// Merge unions the previous sheet with the freshly distilled one so a model
// that drops whole days (or individual entries) cannot lose history. For the
// same day a newer entry wins on an identical timestamp; otherwise both are
// kept. The result is re-rendered in canonical chronological order.
func Merge(prev, next string) string {
	pd, nd := parseSheet(prev), parseSheet(next)
	out := map[string]*dayBlock{}
	for d, b := range pd {
		out[d] = cloneBlock(b)
	}
	for d, nb := range nd {
		ob := out[d]
		if ob == nil {
			out[d] = cloneBlock(nb)
			continue
		}
		if nb.state != "" {
			ob.state = nb.state
		}
		seen := map[string]int{}
		for i, l := range ob.logs {
			seen[logOrder(l)] = i
		}
		for _, l := range nb.logs {
			k := logOrder(l)
			if i, ok := seen[k]; ok {
				ob.logs[i] = l
			} else {
				seen[k] = len(ob.logs)
				ob.logs = append(ob.logs, l)
			}
		}
	}
	order := sortedDates(out)
	for _, b := range out {
		sort.SliceStable(b.logs, func(i, j int) bool { return logOrder(b.logs[i]) < logOrder(b.logs[j]) })
	}
	return renderSheet(out, order)
}

// EnforceWindow deterministically trims to the character budget by dropping
// whole oldest days, but never below retainDays days (the memory floor). It
// also canonicalises the sheet layout.
func EnforceWindow(sheet string, maxChars, retainDays int) string {
	canon := Merge(sheet, "")
	if maxChars <= 0 {
		return canon
	}
	if retainDays < 1 {
		retainDays = 1
	}
	days := parseSheet(canon)
	order := sortedDates(days)
	for len(order) > retainDays && len([]rune(canon)) > maxChars {
		delete(days, order[0])
		order = order[1:]
		canon = renderSheet(days, order)
	}
	return canon
}

// IsValid reports whether an LLM sheet is parseable (a section marker plus at
// least one dated entry). Used to refuse overwriting a good sheet with prose.
func IsValid(sheet string) bool {
	if strings.TrimSpace(sheet) == "" {
		return false
	}
	if !strings.Contains(sheet, "[USER STATE]") && !strings.Contains(sheet, "[LOG]") {
		return false
	}
	return len(parseSheet(sheet)) > 0
}

// DayCount returns how many distinct dated days a sheet holds (for audit/logs).
func DayCount(sheet string) int { return len(parseSheet(sheet)) }

// BuildUser turns the prompt template + sheets into the messages sent upstream.
// macros are already applied to prompt by the caller.
func BuildUser(prev, timeline string) string {
	var sb strings.Builder
	sb.WriteString("[Previous Fact Sheet]\n")
	if strings.TrimSpace(prev) == "" {
		sb.WriteString("(empty — this is the first distillation)\n")
	} else {
		sb.WriteString(prev + "\n")
	}
	sb.WriteString("\n[New Messages]\n")
	sb.WriteString(timeline)
	sb.WriteString("\n")
	return sb.String()
}
