// Package distill implements the Distilled Memory feature (ported from the
// SillyTavern "Distilled-Memory" extension): recent chat is periodically
// distilled by the LLM into a compact, dated personal record that is injected
// into the prompt as two blocks: `distilled_state` (L1, always present) and
// `distilled_log` (L2, oldest whole days evicted by the engine under budget).
//
// Design (2026-10-13 v3 — generic sections): the record is a set of named
// sections, each holding dated lines. `[STATE]` is the only L1 (constant)
// section; every other section is dated, append-only and evicted oldest-first.
// Section names are free-form ([LOG], [INSIGHT], [MONEY], [ITEM], ...): the
// per-character prompt tells the model which to emit, and the WebUI renders one
// box per section. The LLM stays a pure *extractor* of deltas; the application
// owns the database (append + deterministic retention), so it can neither
// delete history nor grow without bound.
//
// Storage is per-character and file-based so data/ stays rclone-friendly:
//
//	characters/<name>/distilled.md        [USER STATE] + named sections
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
//
// Four input sections, three of which are context/evidence rather than fact
// sources. Keeping that distinction explicit is the whole difficulty of this
// prompt: a bare movement dump gets ignored (the "only what the user says" rule
// vetoes it) or transcribed as noise ("[14:47] moving").
const DefaultPrompt = `You are {{user}}'s fact extractor. The application owns the memory database; you only extract new facts. Never manage, prune, or rewrite the whole record.

## Input
- [Current State]: {{user}}'s current one-line-per-day snapshot.
- [Recent Entries]: the tail of the diary already written. Read it for CONTINUITY, not to repeat.
- [Movement]: {{user}}'s staypoints from GPS, already humanised. EVIDENCE ONLY — never a fact source on its own.
- [New Messages]: the conversation since your last extraction.
- LIVE TIMESTAMP: {{isodate}} ({{weekday}}) {{time}} UTC+8.

## What to record
- Facts {{user}} explicitly states or does. Ignore the assistant's words, roleplay, hypotheticals, plans, and weak confirmations.
- [Movement] may CORROBORATE and ENRICH those facts: a place can disambiguate what was said ("grabbed food" while a 25-min stay is logged at a restaurant), a duration can make it concrete ("worked overtime" during a 6-hour stay), a departure can date an event.
- When movement and words agree, write the richer single line: name the place and the duration inside the event.
- When they conflict, {{user}}'s own words WIN. Never let GPS overwrite what was actually said.
- A staypoint is a place {{user}} was, never a place they intend to go, want to go, or are thinking about. Plans live only in what {{user}} says.

## What NOT to record
- NEVER write a movement trace as its own line. No "[14:47] moving", no "[14:50-15:20] stayed at X", no place list, no coordinates. Movement belongs INSIDE an event that the messages already justify.
- Do not create an event from movement alone. If the messages say nothing and movement shows nothing notable, emit nothing.
- Output ONLY new information. Do NOT repeat anything in [Current State] or [Recent Entries].
- Never mention [Movement] itself: no "according to GPS", no "the staypoint data". Write as plain fact.
- NEVER delete, trim, summarise away, or add commentary. The application handles size and retention.

## Format
- Dates are ALWAYS DD-MM-YYYY. The message timestamps you see are ISO (YYYY-MM-DD) — convert them.
- One event per line, in {{user}}'s own voice. No bullets, no sub-lines. Prefer a concrete noun over an abstraction: "grabbed dinner at Petronas, 30 minutes" over "ate".

## Output (exact format, nothing else)
[STATE]
[DD-MM-YYYY] <today's current snapshot, one line>

[LOG]
[DD-MM-YYYY HH:MM] <event>

Omit [STATE] if it did not change. Omit [LOG] if there is no new event.`

// Meta is distillation bookkeeping for one character.
type Meta struct {
	LastRun          string `json:"last_run,omitempty"`
	LastIndex        int    `json:"last_index"` // messages already distilled
	UserTurns        int    `json:"user_turns"` // user turns since last run
	Runs             int    `json:"runs"`
	LastStatus       string `json:"last_status,omitempty"`
	LastVectorize    string `json:"last_vectorize,omitempty"`
	LastVectorizeErr string `json:"last_vectorize_error,omitempty"`
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

// ---- generic sections ------------------------------------------------------
//
// The record is a set of named sections, each holding dated lines. A line
// `[DD-MM-YYYY] text` is a per-day "state" line (within its section a newer
// line for the same day replaces the older one); a line
// `[DD-MM-YYYY HH:MM] text` is a timestamped log line (deduped by date+time).
// Section names are free-form ([STATE], [LOG], [INSIGHT], [MONEY], ...); the
// prompt decides which the model emits and the app parses them generically.
// Dates are accepted as DD-MM-YYYY (canonical) or ISO YYYY-MM-DD and normalised
// to DD-MM-YYYY on parse, so a model's format slip cannot silently drop entries.

// Entry is one parsed, dated line.
type Entry struct {
	Tag  string `json:"tag"`
	Date string `json:"date"`           // canonical DD-MM-YYYY
	Time string `json:"time,omitempty"` // HH:MM, empty for per-day state lines
	Text string `json:"text"`
}

func (e Entry) key() string {
	if e.Time == "" {
		return e.Date
	}
	return e.Date + " " + e.Time
}

// Line renders the canonical "[date (time)] text" line.
func (e Entry) Line() string {
	if e.Time != "" {
		return "[" + e.Date + " " + e.Time + "] " + e.Text
	}
	return "[" + e.Date + "] " + e.Text
}

var (
	entryRe   = regexp.MustCompile(`^\[(\d{2}-\d{2}-\d{4}|\d{4}-\d{2}-\d{2})(?: (\d{2}:\d{2}))?\]\s*(.*)$`)
	headerRe  = regexp.MustCompile(`^\[([A-Z][A-Z0-9_ ]*)\]\s*$`)
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

func dateOrder(d string) string {
	if t, err := time.Parse("02-01-2006", d); err == nil {
		return t.Format("2006-01-02")
	}
	return d
}

// normalizeTag canonicalises a section header ("USER STATE" -> "STATE").
func normalizeTag(t string) string {
	t = strings.ToUpper(strings.TrimSpace(t))
	t = strings.Join(strings.Fields(t), " ")
	if t == "USER STATE" {
		return "STATE"
	}
	return t
}

// sectionHeader renders a section's canonical header line (STATE keeps the
// legacy "[USER STATE]" spelling).
func sectionHeader(tag string) string {
	if tag == "STATE" {
		return "[USER STATE]"
	}
	return "[" + tag + "]"
}

// sheet is the parsed record: an insertion-ordered set of sections keyed by
// (tag, entry key) so a newer line replaces the older one in place.
type sheet struct {
	order []string
	byTag map[string]map[string]Entry
}

func newSheet() *sheet { return &sheet{byTag: map[string]map[string]Entry{}} }

func (s *sheet) add(e Entry) {
	m := s.byTag[e.Tag]
	if m == nil {
		m = map[string]Entry{}
		s.byTag[e.Tag] = m
		s.order = append(s.order, e.Tag)
	}
	m[e.key()] = e
}

func parseSheet(str string) *sheet {
	sh := newSheet()
	tag := ""
	for _, raw := range strings.Split(str, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "[End of") {
			continue
		}
		if h := headerRe.FindStringSubmatch(t); h != nil {
			tag = normalizeTag(h[1])
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
		e := Entry{Date: d, Time: m[2], Text: strings.TrimSpace(m[3])}
		switch {
		case tag != "":
			e.Tag = tag
		case e.Time != "":
			e.Tag = "LOG"
		default:
			e.Tag = "STATE"
		}
		sh.add(e)
	}
	return sh
}

// orderedTags puts STATE first, LOG second, then any other section in the
// order it first appeared.
func (s *sheet) orderedTags() []string {
	out := make([]string, 0, len(s.order))
	seen := map[string]bool{}
	for _, t := range []string{"STATE", "LOG"} {
		if _, ok := s.byTag[t]; ok {
			out = append(out, t)
			seen[t] = true
		}
	}
	for _, t := range s.order {
		if !seen[t] {
			out = append(out, t)
			seen[t] = true
		}
	}
	return out
}

func (s *sheet) tagsExcept(skip ...string) []string {
	skipSet := map[string]bool{}
	for _, t := range skip {
		skipSet[t] = true
	}
	var out []string
	for _, t := range s.order {
		if !skipSet[t] {
			out = append(out, t)
		}
	}
	return out
}

func (s *sheet) entries(tag string) []Entry {
	m := s.byTag[tag]
	if len(m) == 0 {
		return nil
	}
	out := make([]Entry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := dateOrder(out[i].Date), dateOrder(out[j].Date)
		if di != dj {
			return di < dj
		}
		return out[i].Time < out[j].Time
	})
	return out
}

func (s *sheet) render() string {
	var sb strings.Builder
	first := true
	for _, tag := range s.orderedTags() {
		es := s.entries(tag)
		if len(es) == 0 {
			continue
		}
		if !first {
			sb.WriteString("\n")
		}
		first = false
		sb.WriteString(sectionHeader(tag) + "\n")
		for _, e := range es {
			sb.WriteString(e.Line() + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// dropOldest removes the single oldest entry among the given tags. On the same
// date a timestamped row sorts before a per-day state row. Reports whether
// anything was dropped.
func (s *sheet) dropOldest(tags ...string) bool {
	var best Entry
	var bestTag, bestKey string
	found := false
	for _, tag := range tags {
		for k, e := range s.byTag[tag] {
			if !found || dateOrder(e.Date) < dateOrder(best.Date) ||
				(dateOrder(e.Date) == dateOrder(best.Date) && e.Time < best.Time) {
				best, bestTag, bestKey, found = e, tag, k, true
			}
		}
	}
	if !found {
		return false
	}
	delete(s.byTag[bestTag], bestKey)
	if len(s.byTag[bestTag]) == 0 {
		delete(s.byTag, bestTag)
		for i, t := range s.order {
			if t == bestTag {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
	}
	return true
}

// Merge unions the previous record with an incoming one so a model that drops
// whole days (or individual entries) cannot lose history. For the same
// (section, date[, time]) the newer entry wins; otherwise both are kept. The
// result is re-rendered in canonical chronological order.
func Merge(prev, next string) string {
	a, b := parseSheet(prev), parseSheet(next)
	for _, tag := range b.orderedTags() {
		for _, e := range b.entries(tag) {
			a.add(e)
		}
	}
	return a.render()
}

// ApplyDelta merges an extracted delta into the canonical record. It is the
// same union as Merge (new state replaces that day's state, new log lines are
// appended and deduped by timestamp) — nothing already stored is ever lost.
func ApplyDelta(prev, delta string) string { return Merge(prev, delta) }

// Retain applies the deterministic code-side retention policy: a per-entry
// character cap, then a total character budget that drops the oldest dated
// entries first. The L1 [STATE] snapshot is never evicted to satisfy the
// budget (it is tiny and is the character's current state); it is only
// entry-capped. The LLM never deletes; this function is the only thing that
// does.
func Retain(str string, maxChars, maxEntryChars int) string {
	sh := parseSheet(str)
	if maxEntryChars > 0 {
		for _, m := range sh.byTag {
			for k, e := range m {
				e.Text = capText(e.Text, maxEntryChars)
				m[k] = e
			}
		}
	}
	if maxChars > 0 {
		for len([]rune(sh.render())) > maxChars {
			if !sh.dropOldest(sh.tagsExcept("STATE")...) {
				break
			}
		}
	}
	return sh.render()
}

// capText truncates free text to max runes.
func capText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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

// IsValid reports whether an LLM sheet is parseable (at least one dated entry
// under some section). Used to refuse overwriting a good record with prose.
func IsValid(str string) bool {
	if strings.TrimSpace(str) == "" {
		return false
	}
	sh := parseSheet(str)
	for _, m := range sh.byTag {
		if len(m) > 0 {
			return true
		}
	}
	return false
}

// UnparsedDatedLines returns lines that look like dated entries but were not
// recognised by the parser (e.g. a model invented a slash layout). Used for
// observability so silent data loss becomes visible.
func UnparsedDatedLines(str string) []string {
	var out []string
	for _, raw := range strings.Split(str, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "[End of") || headerRe.MatchString(t) {
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

// DayCount returns how many distinct dated days a record holds (for audit/logs).
func DayCount(str string) int {
	days := map[string]bool{}
	for _, m := range parseSheet(str).byTag {
		for _, e := range m {
			days[e.Date] = true
		}
	}
	return len(days)
}

// LoadState returns the [STATE] lines (canonical, oldest-first), capped to the
// newest maxDays when maxDays > 0. This is the L1 constant.
func LoadState(root, char string, maxDays int) string {
	es := parseSheet(Load(root, char)).entries("STATE")
	lines := make([]string, 0, len(es))
	for _, e := range es {
		lines = append(lines, e.Line())
	}
	if maxDays > 0 && len(lines) > maxDays {
		lines = lines[len(lines)-maxDays:]
	}
	return strings.Join(lines, "\n")
}

// Entries returns every non-STATE (dated, evictable) entry, oldest-first. This
// is the L2 list the engine evicts under budget pressure.
func Entries(root, char string) []Entry {
	sh := parseSheet(Load(root, char))
	var out []Entry
	for _, tag := range sh.orderedTags() {
		if tag == "STATE" {
			continue
		}
		out = append(out, sh.entries(tag)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := dateOrder(out[i].Date), dateOrder(out[j].Date)
		if di != dj {
			return di < dj
		}
		return out[i].Time < out[j].Time
	})
	return out
}

// Section is one named group of entries, for the WebUI boxes.
type Section struct {
	Tag     string  `json:"tag"`
	Entries []Entry `json:"entries"`
}

// Sections returns the parsed record grouped by section, in canonical order
// (STATE, LOG, then first-seen). Used by the Memory page to render one box per
// section.
func Sections(root, char string) []Section {
	sh := parseSheet(Load(root, char))
	var out []Section
	for _, tag := range sh.orderedTags() {
		es := sh.entries(tag)
		if len(es) == 0 {
			continue
		}
		out = append(out, Section{Tag: tag, Entries: es})
	}
	return out
}

// BuildUser turns the current state, the recent diary tail, the movement
// evidence and the new timeline into the extractor's input.
//
// Section ORDER is deliberate and matches DefaultPrompt: identity/state first,
// then what was already written (so it does not repeat it), then the external
// evidence, then the new material being extracted from.
//
// recentLog and movement are optional; when empty their sections are omitted
// entirely rather than filled with a placeholder, because a "[Recent Entries]
// (none)" line invites the model to comment on its own emptiness, and an empty
// "[Movement]" would still invite trace transcription.
func BuildUser(state, recentLog, movement, timeline string) string {
	var sb strings.Builder
	sb.WriteString("[Current State]\n")
	if strings.TrimSpace(state) == "" {
		sb.WriteString("(none yet)\n")
	} else {
		sb.WriteString(state + "\n")
	}
	if s := strings.TrimSpace(recentLog); s != "" {
		sb.WriteString("\n[Recent Entries]\n")
		sb.WriteString(s + "\n")
	}
	if s := strings.TrimSpace(movement); s != "" {
		sb.WriteString("\n")
		sb.WriteString(s + "\n") // movement already carries its own [Recent Movement] header
	}
	sb.WriteString("\n[New Messages]\n")
	sb.WriteString(timeline)
	sb.WriteString("\n")
	return sb.String()
}

// RecentLogEntries returns the newest n non-STATE entries, oldest-first among
// themselves, in the same "[DD-MM-YYYY HH:MM] ..." shape the model sees in the
// rest of the input. This is what gives the extractor continuity.
//
// The tail is hard-bounded on purpose. The extractor's safety property is that
// it never sees the whole diary, so its output cannot grow with history; an
// unbounded tail would quietly remove that guarantee.
func RecentLogEntries(root, char string, n int) string {
	if n <= 0 {
		return ""
	}
	es := Entries(root, char)
	if len(es) > n {
		es = es[len(es)-n:]
	}
	lines := make([]string, 0, len(es))
	for _, e := range es {
		lines = append(lines, e.Line())
	}
	return strings.Join(lines, "\n")
}
