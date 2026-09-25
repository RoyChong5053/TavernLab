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
	return filepath.Join(root, "characters", name)
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

// renderBlocks fills code-level macros before assembly:
//   - time macros ({{isodate}} {{date}} {{time}} {{weekday}} {{datetime}}
//     {{timestamp}} {{timezone}} {{user}}) once per request;
//   - {{character_card}} from the session's card.json for source.type=="character".
//
// Returns a copy so the caller's editor state is untouched.
func renderBlocks(root, session, userName string, blocks []engine.Block) []engine.Block {
	now := time.Now()
	out := make([]engine.Block, len(blocks))
	copy(out, blocks)
	desc := ""
	if session != "" {
		desc = loadCharCard(charBase(root, session)).Description
	}
	for i := range out {
		rendered := applyMacros(out[i].Template, now, userName)
		switch out[i].Source.Type {
		case "character":
			if strings.Contains(rendered, "{{character_card}}") {
				out[i].Content = strings.ReplaceAll(rendered, "{{character_card}}", desc)
			} else {
				out[i].Content = desc
			}
		case "static":
			out[i].Content = rendered
		case "distilled_state":
			state := distill.LoadState(root, session, 0)
			if state == "" {
				continue // leave unresolved; engine will drop it
			}
			if strings.Contains(rendered, "{{distilled_state}}") {
				out[i].Content = strings.ReplaceAll(rendered, "{{distilled_state}}", state)
			} else {
				out[i].Content = state
			}
		case "distilled":
			// Legacy single-block form (normalizeBlocks migrates it to the
			// state/log split, but the editor may still send it raw).
			sheet := distill.Load(root, session)
			if sheet == "" {
				continue // leave unresolved; engine will drop it
			}
			if strings.Contains(rendered, "{{distilled}}") {
				out[i].Content = strings.ReplaceAll(rendered, "{{distilled}}", sheet)
			} else {
				out[i].Content = sheet
			}
		default:
			// mcp/vectra/distilled_log are resolved elsewhere; keep any
			// resolved Content the caller already set (e.g. preview overrides).
		}
	}
	return out
}

// normalizeBlocks migrates legacy block data in place: the "character" block
// must use source.type=="character" so the card is injected (older files had
// it as "static"). Retired fields are cleaned up: the per-block numeric
// "max" cap (budget control moved to Level + L2 eviction) and the old
// "vectra" RAG block (the RAG database is fully MCP now). Missing levels are
// inferred from the block id/source for old presets.
func normalizeBlocks(blocks []engine.Block) []engine.Block {
	out := make([]engine.Block, 0, len(blocks)+1)
	for _, b := range blocks {
		if b.Source.Type == "vectra" {
			continue // retired: RAG is fully MCP
		}
		if b.ID == "character" && b.Source.Type != "character" {
			b.Source.Type = "character"
		}
		// Migrate the legacy single distilled block into the state/log split:
		// STATE (L1, always present, code-capped) + LOG (L2, oldest whole days
		// evicted by the engine under budget pressure).
		if b.Source.Type == "distilled" {
			stateBlk := b
			stateBlk.ID = "distilled_state"
			stateBlk.Source = engine.Source{Type: "distilled_state", Collection: b.Source.Collection}
			stateBlk.Level = engine.LevelLocked
			stateBlk.Budget = engine.Budget{}
			if strings.TrimSpace(stateBlk.Template) == "" || strings.Contains(stateBlk.Template, "{{distilled}}") {
				stateBlk.Template = "<User State(Distilled Memory)>\n{{distilled_state}}\n</User State>"
			}
			logBlk := b
			logBlk.ID = "distilled_log"
			logBlk.Source = engine.Source{Type: "distilled_log", Collection: b.Source.Collection}
			logBlk.Level = engine.LevelTrim
			logBlk.Order = b.Order + 1
			logBlk.Budget = engine.Budget{}
			logBlk.Template = "<Memory Log>\n{{items}}\n</Memory Log>"
			out = append(out, stateBlk, logBlk)
			continue
		}
		if b.Level < engine.LevelLocked || b.Level > engine.LevelElastic {
			b.Level = defaultLevel(b)
		}
		// List-backed sources are always L2 (the engine builds them as
		// evictable lists regardless of Level) and distilled_state is always
		// L1; keep the editor's Level in sync with reality instead of showing
		// a no-op value (chat in L3, RAG mislabelled L1, ...).
		switch b.Source.Type {
		case "chat", "mcp", "vectra", "distilled_log":
			b.Level = engine.LevelTrim
		case "distilled_state":
			b.Level = engine.LevelLocked
		}
		b.Budget = engine.Budget{}
		out = append(out, b)
	}
	return out
}

// defaultLevel infers the eviction level for legacy blocks that predate the
// L1/L2/L3 model.
func defaultLevel(b engine.Block) engine.Level {
	switch b.Source.Type {
	case "chat", "mcp", "vectra", "distilled_log":
		return engine.LevelTrim
	case "distilled_state", "distilled":
		return engine.LevelLocked
	}
	switch b.ID {
	case "system", "time_anchor", "character", "distilled", "distilled_state":
		return engine.LevelLocked
	case "rag_mcp", "rag_vectra", "chat", "distilled_log":
		return engine.LevelTrim
	}
	return engine.LevelElastic
}

func applyMacros(s string, now time.Time, userName string) string {
	if s == "" {
		return s
	}
	repl := map[string]string{
		"{{isodate}}":   now.Format("2006-01-02"),
		"{{date}}":      now.Format("2006-01-02"),
		"{{time}}":      now.Format("15:04"),
		"{{weekday}}":   now.Weekday().String(),
		"{{datetime}}":  now.Format("2006-01-02 15:04:05"),
		"{{timestamp}}": strconv.FormatInt(now.Unix(), 10),
		"{{timezone}}":  now.Format("MST"),
		"{{user}}":      userName,
	}
	for k, v := range repl {
		s = strings.ReplaceAll(s, k, v)
	}
	return s
}
