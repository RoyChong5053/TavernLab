package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/distill"
	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/settings"
	"github.com/RoyChong5053/TavernLab/internal/store"
)

// CharCard is the character description ("角色卡"). No SillyTavern macro
// layer: this is a plain description written straight into the character
// block at assemble time.
type CharCard struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Created     string `json:"created,omitempty"`
}

func charBase(root, name string) string {
	// All character paths go through CleanSession so the API layer and the
	// chat store never disagree on names with spaces/separators.
	return filepath.Join(root, "characters", store.CleanSession(name))
}

// CharProfile is a character's self-contained memory / context-engineering
// profile. It lives in characters/<name>/profile.json so it travels with the
// character's data package (rclone-friendly) and switching characters switches
// the whole memory setup. There is no global memory default layer: a fresh
// character starts from DefaultProfile().
type CharProfile struct {
	RAG     RAGProfile     `json:"rag"`
	Distill DistillProfile `json:"distill"`
}

// RAGProfile separates what a character READS from what it WRITES. Recall is a
// multi-select set (empty = recall nothing, e.g. a brand-new character kept
// clean until its card is proven); auto-vectorize is a SINGLE explicit target
// — knowledge collections are recall-only so the diary only ever flows into the
// character's own chat-memory collection.
type RAGProfile struct {
	Collections     []string `json:"collections,omitempty"`      // recall (multi); empty = no recall
	WriteCollection string   `json:"write_collection,omitempty"` // auto-vectorize target (single); empty = no write
}

// DistillProfile is how this character distills its chat into memory. Prompt is
// the personality-specific extractor; empty = the built-in DefaultPrompt.
type DistillProfile struct {
	Enabled  bool   `json:"enabled"`
	Interval int    `json:"interval,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

// DefaultProfile is what a new character starts with: no recall (clean slate),
// no auto-vectorize target, auto-distill on with the built-in STATE/LOG memory.
func DefaultProfile() CharProfile {
	return CharProfile{
		RAG:     RAGProfile{Collections: []string{}},
		Distill: DistillProfile{Enabled: true, Interval: 8},
	}
}

func profilePath(base string) string { return filepath.Join(base, "profile.json") }

// loadCharProfile reads profile.json. Reports whether the file existed (false
// means "never configured", which triggers a one-time migration).
func loadCharProfile(base string) (CharProfile, bool) {
	b, err := os.ReadFile(profilePath(base))
	if err != nil {
		return CharProfile{}, false
	}
	var p CharProfile
	if json.Unmarshal(b, &p) != nil {
		return CharProfile{}, false
	}
	if p.Distill.Interval <= 0 {
		p.Distill.Interval = 8
	}
	return p, true
}

func saveCharProfile(base string, p CharProfile) error {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	if p.Distill.Interval <= 0 {
		p.Distill.Interval = 8
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(profilePath(base), b, 0o644)
}

// profileFor resolves the effective profile for a session. A character with no
// profile.json yet is migrated once from the legacy global mcp_collection (and
// any old per-character meta.json override, which wins), so nothing silently
// stops recalling/writing after the upgrade.
func profileFor(root string, s settings.Settings, session string) CharProfile {
	base := charBase(root, session)
	if p, ok := loadCharProfile(base); ok {
		return p
	}
	p := DefaultProfile()
	p.Distill.Enabled = s.DistillEnabled
	if s.DistillInterval > 0 {
		p.Distill.Interval = s.DistillInterval
	}
	p.Distill.Prompt = s.DistillPrompt
	seed := s.MCPCollection
	if m := loadCharMeta(base); m != nil {
		if v, ok := m["mcp_collection"].(string); ok && strings.TrimSpace(v) != "" {
			seed = v
		}
	}
	if cols := splitCSV(seed); len(cols) > 0 {
		p.RAG.Collections = cols
		p.RAG.WriteCollection = cols[0]
	}
	return p
}

// splitCSV trims and de-duplicates a comma-separated collection list.
func splitCSV(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

// cleanList trims and de-duplicates a recall collection list from JSON.
func cleanList(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// hasCharCard reports whether a character directory holds a real card.
func hasCharCard(base string) bool {
	if _, err := os.Stat(filepath.Join(base, "card.json")); err != nil {
		return false
	}
	return true
}

// isOrphanDir reports a directory that is only a leftover chat session (the
// legacy main/test cases): no card, no avatar, no meta, no distilled record,
// no archive/media/expressions. A real character may legitimately lack
// card.json (e.g. Leer乐儿 before its first edit) but still owns an avatar or
// meta, so "no card" alone must not mark it an orphan.
func isOrphanDir(base string) bool {
	if hasCharCard(base) {
		return false
	}
	for _, f := range []string{"meta.json", "distilled.md", "distilled.meta.json"} {
		if _, err := os.Stat(filepath.Join(base, f)); err == nil {
			return false
		}
	}
	for _, d := range []string{"archive", "media", "expressions"} {
		if st, err := os.Stat(filepath.Join(base, d)); err == nil && st.IsDir() {
			return false
		}
	}
	for _, a := range []string{"avatar.webp", "avatar.png", "avatar.jpg", "avatar.jpeg", "avatar.gif"} {
		if _, err := os.Stat(filepath.Join(base, a)); err == nil {
			return false
		}
	}
	return true
}

// loadCharCard reads card.json (empty card when absent).
func loadCharCard(base string) CharCard {
	var c CharCard
	b, err := os.ReadFile(filepath.Join(base, "card.json"))
	if err != nil {
		return c
	}
	_ = json.Unmarshal(b, &c)
	return c
}

func saveCharCard(base string, c CharCard) error {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(base, "card.json"), b, 0o644)
}

// charAvatarURL finds the character's avatar file, if any.
func charAvatarURL(root, name string) string {
	base := charBase(root, name)
	for _, cand := range []string{"avatar.webp", "avatar.png", "avatar.jpg", "avatar.jpeg", "avatar.gif"} {
		if _, err := os.Stat(filepath.Join(base, cand)); err == nil {
			return "/chars/" + name + "/" + cand
		}
	}
	return ""
}

// userAvatarName finds the user's own avatar file under data root, if any.
func userAvatarName(root string) string {
	for _, cand := range []string{"user_avatar.webp", "user_avatar.png", "user_avatar.jpg", "user_avatar.jpeg", "user_avatar.gif"} {
		if _, err := os.Stat(filepath.Join(root, cand)); err == nil {
			return cand
		}
	}
	return ""
}

// charAvatarPx reads meta.json avatar_px (default 88).
func charAvatarPx(base string) int {
	meta := loadCharMeta(base)
	if v, ok := meta["avatar_px"].(float64); ok && v >= 24 {
		return int(v)
	}
	if v, ok := meta["avatar_px"].(int); ok && v >= 24 {
		return v
	}
	return 88
}

func listExpressions(base string) []string {
	var out []string
	if es, err := os.ReadDir(filepath.Join(base, "expressions")); err == nil {
		for _, e := range es {
			if !e.IsDir() {
				out = append(out, "expressions/"+e.Name())
			}
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// renderModules fills code-level placeholders before assembly:
//   - time placeholders ({{isodate}} {{date}} {{time}} {{weekday}} …) once
//     per request;
//   - {{character_card}} from the session's card.json for Kind character.
//
// Returns a copy so the caller's editor state is untouched.
func renderModules(root, session, userName string, modules []engine.Module) []engine.Module {
	now := time.Now()
	out := make([]engine.Module, len(modules))
	copy(out, modules)
	desc := ""
	if session != "" {
		desc = loadCharCard(charBase(root, session)).Description
	}
	locText := loadLocationMacro(root, now)
	for i := range out {
		rendered := applyMacros(out[i].Template, now, userName)
		rendered = strings.ReplaceAll(rendered, "{{location}}", locText)
		// A time_anchor template saved without {{location}} still gets the
		// live location appended, so the anchor never silently loses it.
		if out[i].Kind == engine.KindTimeAnchor && locText != "" && !strings.Contains(rendered, locText) {
			rendered += " " + locText
		}
		switch out[i].Kind {
		case engine.KindCharacter:
			if strings.Contains(rendered, "{{character_card}}") {
				out[i].Content = strings.ReplaceAll(rendered, "{{character_card}}", desc)
			} else {
				out[i].Content = desc
			}
		case engine.KindSystem, engine.KindTimeAnchor, engine.KindNote:
			out[i].Content = rendered
		case engine.KindDistilledState:
			state := distill.LoadState(root, session, 0)
			if state == "" {
				continue // leave unresolved; engine will drop it
			}
			if strings.Contains(rendered, "{{distilled_state}}") {
				out[i].Content = strings.ReplaceAll(rendered, "{{distilled_state}}", state)
			} else {
				out[i].Content = state
			}
		default:
			// rag/chat/distilled_log are resolved elsewhere; keep any resolved
			// Content the caller already set (e.g. preview overrides).
		}
	}
	return out
}

// normalizeModules repairs a loaded preset: missing Kind is inferred from the
// module id, invalid Levels are repaired, and the per-module numeric cap
// (budget control now lives in Level + L2 eviction) is cleared. List-backed
// kinds are forced to L2 and distilled_state to L1 so the editor never shows
// a no-op Level.
func normalizeModules(modules []engine.Module) []engine.Module {
	out := make([]engine.Module, 0, len(modules))
	for _, m := range modules {
		if m.Kind == "" {
			m.Kind = inferKind(m.ID)
		}
		if m.Level < engine.LevelLocked || m.Level > engine.LevelElastic {
			m.Level = defaultLevel(m)
		}
		switch m.Kind {
		case engine.KindChat, engine.KindRAG, engine.KindDistilledLog:
			m.Level = engine.LevelTrim
		case engine.KindDistilledState:
			m.Level = engine.LevelLocked
		}
		m.Budget = engine.Budget{}
		out = append(out, m)
	}
	return out
}

// inferKind maps a known module id to its kind (for presets written before
// Kind existed).
func inferKind(id string) string {
	switch id {
	case "system", "time_anchor", "character", "distilled_state", "distilled_log", "rag", "chat":
		return id
	case "rag_mcp", "rag_vectra":
		return engine.KindRAG
	}
	return engine.KindNote
}

// defaultLevel infers the eviction level from a module's kind.
func defaultLevel(m engine.Module) engine.Level {
	switch m.Kind {
	case engine.KindChat, engine.KindRAG, engine.KindDistilledLog:
		return engine.LevelTrim
	case engine.KindDistilledState, engine.KindSystem, engine.KindTimeAnchor, engine.KindCharacter, engine.KindNote:
		return engine.LevelLocked
	}
	return engine.LevelElastic
}
func applyMacros(s string, now time.Time, userName string) string {
	if s == "" {
		return s
	}
	repl := map[string]string{
		"{{isodate}}":    now.Format("2006-01-02"),
		"{{date}}":       now.Format("2006-01-02"),
		"{{time}}":       now.Format("15:04"),
		"{{weekday}}":    now.Weekday().String(),
		"{{weekday_zh}}": weekdayZh(now.Weekday()),
		"{{daypart}}":    daypart(now.Hour()),
		"{{datetime}}":   now.Format("2006-01-02 15:04:05"),
		"{{timestamp}}":  strconv.FormatInt(now.Unix(), 10),
		"{{timezone}}":   now.Format("MST"),
		"{{user}}":       userName,
	}
	for k, v := range repl {
		s = strings.ReplaceAll(s, k, v)
	}
	return s
}

func weekdayZh(w time.Weekday) string {
	return []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[w]
}

// daypart buckets the hour for Chinese day-part words.
func daypart(h int) string {
	switch {
	case h < 5:
		return "凌晨"
	case h < 8:
		return "清晨"
	case h < 12:
		return "上午"
	case h < 14:
		return "中午"
	case h < 18:
		return "下午"
	case h < 23:
		return "晚上"
	default:
		return "夜里"
	}
}

// loadLocationMacro renders {{location}} via the shared renderer in
// location.go (bare facts + dwell; behaviour rules live in system block).
func loadLocationMacro(root string, now time.Time) string {
	return renderLocationText(root, settings.Load(root), locationHTTP, now)
}
