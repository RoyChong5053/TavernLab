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
		case "distilled":
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
			// mcp/vectra/distilled are resolved elsewhere; keep any resolved
			// Content the caller already set (e.g. preview overrides).
		}
	}
	return out
}

// normalizeBlocks migrates legacy block data in place: the "character" block
// must use source.type=="character" so the card is injected (older files had
// it as "static"). The removed numeric priority is ignored on load and
// dropped on save.
func normalizeBlocks(blocks []engine.Block) []engine.Block {
	for i := range blocks {
		if blocks[i].ID == "character" && blocks[i].Source.Type != "character" {
			blocks[i].Source.Type = "character"
		}
	}
	return blocks
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
