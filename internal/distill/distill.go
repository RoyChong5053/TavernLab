// Package distill implements the Distilled Memory feature (ported from the
// SillyTavern "Distilled-Memory" extension): recent chat is periodically
// distilled by the LLM into a compact, dated personal record that is injected
// into the prompt as two blocks: `distilled_state` (L1, always present) and
// `distilled_log` (L2, oldest whole days evicted by the engine under budget).
//
// Design (2026-09-26 v2 — read/write separation): the LLM is a pure *extractor*
// of deltas. It never sees or rewrites the whole record, so it can neither
// delete history nor make the output grow without bound. The application owns
// the database: it appends the delta, then deterministically trims the oldest
// whole days down to the retention floor.
//
// Storage is per-character and file-based so data/ stays rclone-friendly:
//
//	characters/<name>/distilled.md        [USER STATE] + [LOG] sections
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

// DefaultPrompt is the delta extractor. {{user}} / {{isodate}} / {{weekday}} /
// {{time}} are filled at run time. It deliberately never mentions a size
// limit: the app enforces retention in code, so the model is not tempted to
// either delete everything or reproduce the whole record.
const DefaultPrompt = `You are {{user}}'s fact extractor. The application owns the memory database; you only extract new facts. Never manage, prune, or rewrite the whole record.

## Input
- [Current State]: {{user}}'s current one-line-per-day snapshot (context only).
- [New Messages]: the conversation since your last extraction.
- LIVE TIMESTAMP: {{isodate}} ({{weekday}}) {{time}} UTC+8.

## Rules
- Record ONLY facts {{user}} explicitly states or does. Ignore the assistant's words, roleplay, hypotheticals, plans, and weak confirmations.
- Output ONLY new information. Do NOT repeat anything already present in [Current State].
- NEVER delete, trim, summarise away, or add commentary. The application handles size and retention.
- Dates are ALWAYS DD-MM-YYYY. The message timestamps you see are ISO (YYYY-MM-DD) — convert them.
- One event per line, in {{user}}'s own words. No bullets, no sub-lines.

## Output (exact format, nothing else)
[STATE]
[DD-MM-YYYY] <today's current snapshot, one line>

[LOG]
[DD-MM-YYYY HH:MM] <event>

Omit [STATE] if it did not change. Omit [LOG] if there is no new event.`

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

// Load returns the current record ("" when none yet).
func Load(root, char string) string {
	b, err := os.ReadFile(sheetPath(root, char))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Save writes the record.
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

// FormatTimeline renders messages in the extractor's input shape:
// `**Speaker** [DD-MM-YYYY HH:MM]: text`. DD-MM-YYYY matches the output format
// so models do not have to translate (they were silently emitting ISO dates,
// which the old parser then dropped).
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
			ts = t.Format("02-01-2006 15:04")
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		sb.WriteString("**" + speaker + "** [" + ts + "]: " + text + "\n")
	}
	return strings.TrimSpace(sb.String())
}

// ---- parsing --------------------------------------------------------------
//
// Dates are accepted in either DD-MM-YYYY (canonical) or YYYY-MM-DD (ISO, what
// models sometimes echo from the raw message timestamps). Everything is
// normalised to DD-MM-YYYY on parse, so a format slip can no longer silently
// drop entries.

var (
	entryRe   = regexp.MustCompile(`^\[(\d{2}-\d{2}-\d{4}|\d{4}-\d{2}-\d{2})(?: (\d{2}:\d{2}))?\]\s*(.*)$`)
	dateishRe = regexp.MustCompile(`^\[\d{2,4}[-/]\d{1,2}[-/]\d{2,4}`)
)

// normalizeDate converts a DD-MM-YYYY or YYYY-MM-DD date to canonical
// DD-MM-YYYY, reporting whether it was a valid date.
func normalizeDate(d string) (string, bool) {
	if len(d) != 10 {
		return "", false
	}
	if d[2] == '-' && d[5] == '-' {
		if _, err := time.Parse("02-01-2006", d); err == nil {
			return d, true
		}
	}
	if d[4] == '-' && d[7] == '-' {
		if t, err := time.Parse("2006-01-02", d); err == nil {
			return t.Format("02-01-2006"), true
		}
	}
	return "", false
}

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
		m := entryRe.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		d, ok := normalizeDate(m[1])
		if !ok {
			continue
		}
		b := get(d)
		if m[2] != "" {
			b.logs = append(b.logs, "["+d+" "+m[2]+"]"+tail(m[3]))
		} else {
			b.state = "[" + d + "]" + tail(m[3])
		}
	}
	return days
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return " " + s
}

func dateOrder(d string) string {
	if t, err := time.Parse("02-01-2006", d); err == nil {
		return t.Format("2006-01-02")
	}
	return d
}

func logOrder(line string) string {
	m := entryRe.FindStringSubmatch(line)
	if m == nil || m[2] == "" {
		return line
	}
	d, ok := normalizeDate(m[1])
	if !ok {
		return line
	}
	if t, err := time.Parse("02-01-2006 15:04", d+" "+m[2]); err == nil {
		return t.Format("2006-01-02 15:04")
	}
	return d + " " + m[2]
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

// Merge unions the previous record with an incoming one so a model that drops
// whole days (or individual entries) cannot lose history. For the same day a
// newer entry wins on an identical timestamp; otherwise both are kept. The
// result is re-rendered in canonical chronological order.
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

// ApplyDelta merges an extracted delta into the canonical record. It is the
// same union as Merge (new state replaces that day's state, new log lines are
// appended and deduped by timestamp) — nothing already stored is ever lost.
func ApplyDelta(prev, delta string) string { return Merge(prev, delta) }

// Retain applies the deterministic code-side retention policy:
//   - per-entry character cap (newest text kept, prefix preserved);
//   - per-day log cap (keep the newest N entries of each day);
//   - total character budget by dropping whole oldest days, but never below
//     retainDays days;
//   - state hard cap by dropping the oldest state lines, but never the logs.
//
// The LLM never deletes; this function is the only thing that does.
func Retain(sheet string, maxChars, retainDays, stateMaxDays, maxLogPerDay, maxEntryChars int) string {
	days := parseSheet(sheet)
	if maxEntryChars > 0 {
		for _, b := range days {
			for i, l := range b.logs {
				b.logs[i] = capEntry(l, maxEntryChars)
			}
		}
	}
	if maxLogPerDay > 0 {
		for _, b := range days {
			if len(b.logs) > maxLogPerDay {
				b.logs = b.logs[len(b.logs)-maxLogPerDay:]
			}
		}
	}
	order := sortedDates(days)
	canon := renderSheet(days, order)
	if maxChars > 0 {
		if retainDays < 1 {
			retainDays = 1
		}
		for len(order) > retainDays && len([]rune(canon)) > maxChars {
			delete(days, order[0])
			order = order[1:]
			canon = renderSheet(days, order)
		}
	}
	if stateMaxDays > 0 {
		var withState []string
		for _, d := range order {
			if b := days[d]; b != nil && b.state != "" {
				withState = append(withState, d)
			}
		}
		if len(withState) > stateMaxDays {
			for _, d := range withState[:len(withState)-stateMaxDays] {
				days[d].state = ""
			}
			canon = renderSheet(days, order)
		}
	}
	return canon
}

// capEntry truncates the free text of a dated line to max runes, preserving
// its "[date time] " prefix.
func capEntry(line string, max int) string {
	if max <= 0 {
		return line
	}
	m := entryRe.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	rest := m[3]
	r := []rune(rest)
	if len(r) <= max {
		return line
	}
	prefix := line[:len(line)-len(rest)]
	return prefix + string(r[:max]) + "…"
}

// IsValid reports whether an LLM sheet is parseable (a section marker plus at
// least one dated entry). Used to refuse overwriting a good record with prose.
func IsValid(sheet string) bool {
	if strings.TrimSpace(sheet) == "" {
		return false
	}
	if !strings.Contains(sheet, "[USER STATE]") && !strings.Contains(sheet, "[LOG]") &&
		!strings.Contains(sheet, "[STATE]") {
		return false
	}
	return len(parseSheet(sheet)) > 0
}

// DayCount returns how many distinct dated days a record holds (for audit/logs).
func DayCount(sheet string) int { return len(parseSheet(sheet)) }

// UnparsedDatedLines returns lines that look like dated entries but were not
// recognised by the tolerant parser (e.g. a model invented a third layout).
// Used for observability so silent data loss becomes visible.
func UnparsedDatedLines(sheet string) []string {
	var out []string
	for _, raw := range strings.Split(sheet, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || t == "[USER STATE]" || t == "[LOG]" || t == "[STATE]" {
			continue
		}
		if entryRe.MatchString(t) {
			continue
		}
		if dateishRe.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

// LogDay is one day's diary block for the L2 evictable log list.
type LogDay struct {
	Date string // canonical DD-MM-YYYY
	Text string // the day's "[DD-MM-YYYY HH:MM] ..." lines, newline-joined
}

// LoadState returns the current-state lines (canonical, oldest-first), capped
// to the newest maxDays when maxDays > 0. This is the L1 constant.
func LoadState(root, char string, maxDays int) string {
	days := parseSheet(Load(root, char))
	order := sortedDates(days)
	var lines []string
	for _, d := range order {
		if b := days[d]; b != nil && b.state != "" {
			lines = append(lines, b.state)
		}
	}
	if maxDays > 0 && len(lines) > maxDays {
		lines = lines[len(lines)-maxDays:]
	}
	return strings.Join(lines, "\n")
}

// LogDays returns one LogDay per day that has log entries, oldest-first.
func LogDays(root, char string) []LogDay {
	days := parseSheet(Load(root, char))
	order := sortedDates(days)
	var out []LogDay
	for _, d := range order {
		b := days[d]
		if b == nil || len(b.logs) == 0 {
			continue
		}
		out = append(out, LogDay{Date: d, Text: strings.Join(b.logs, "\n")})
	}
	return out
}

// Rebuild unions many historical sheets into one canonical record. Used by the
// one-off backfill (no LLM involved): every distilled_memory row ever written
// to chat.jsonl is replayed so previously dropped days are recovered.
func Rebuild(sheets []string) string {
	out := ""
	for _, s := range sheets {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = Merge(out, s)
	}
	return out
}

// BuildUser turns the current state + new timeline into the extractor's input.
func BuildUser(state, timeline string) string {
	var sb strings.Builder
	sb.WriteString("[Current State]\n")
	if strings.TrimSpace(state) == "" {
		sb.WriteString("(none yet)\n")
	} else {
		sb.WriteString(state + "\n")
	}
	sb.WriteString("\n[New Messages]\n")
	sb.WriteString(timeline)
	sb.WriteString("\n")
	return sb.String()
}
