// Package sanitize implements non-destructive context cleaning.
//
// The character's raw replies are always stored verbatim in chat.jsonl (and the
// web UI always shows them verbatim). Cleaning only rewrites the text that gets
// packaged into the API request for a turn, so the model stops re-reading its
// own heavy-symbol history — the driver of the "pattern reinforcement loop"
// (overfitting) seen on small local models. It is a global, opt-in switch
// (data/settings.json context_sanitize), off by default.
//
// Categories are Unicode-based on purpose: suppressing a single symbol only
// makes the model emit a probability-neighbour symbol, so we strip whole
// categories (emoji, kaomoji face-glyphs, decorative brackets). That removes the
// escape hatch. The default ranges were calibrated against the real Leer乐儿
// chat (~2000 turns) so normal CJK sentence punctuation (。 、‘ ”" "— …) is left
// untouched.
package sanitize

import (
	"strings"
	"unicode/utf8"
)

// Category names.
const (
	CatEmoji   = "emoji"   // emoji + symbol glyphs (box, arrows, math, combining marks, rare scripts…)
	CatKaomoji = "kaomoji" // decorative bracket pairs used as speech/emotion framing
	CatFiller  = "filler"  // kaomoji "dots/eyes/lines" glyphs (｡ ﾟ ◦ • ╮ ╯ …)
)

// Config mirrors the data/settings.json context_sanitize block.
type Config struct {
	Enabled    bool     `json:"enabled"`
	Mode       string   `json:"mode,omitempty"` // "strip" (default) | "space"
	Categories []string `json:"categories,omitempty"`
	Excluded   []string `json:"excluded,omitempty"` // explicit per-symbol / substring exclusions
}

// Stats is what one Clean call reports back so the caller can build an audit.
type Stats struct {
	TotalDropped int            `json:"total_dropped"`
	ByCategory   map[string]int `json:"by_category"`
}

// Engine is a compiled, stateless cleaner. Safe for concurrent use.
type Engine struct {
	cfg      Config
	emoji    emojiRanges
	kaomoji  *pointSet
	filler   *pointSet
	excluded []string
}

// emojiRanges is a list of runic intervals.
type emojiRanges struct {
	starts []rune
	ends   []rune
}

func (r emojiRanges) has(ch rune) bool {
	for i := range r.starts {
		if ch >= r.starts[i] && ch <= r.ends[i] {
			return true
		}
	}
	return false
}

// pointSet is a small codepoint deny-list.
type pointSet struct {
	m map[rune]bool
}

func (p *pointSet) has(ch rune) bool { return p != nil && p.m[ch] }

// Compile turns a Config into a ready Engine. A nil/empty engine is a no-op.
// When enabled but no categories are listed, the three calibrated defaults are
// applied so a simple "enabled" toggle strips emoji/kaomoji/filler out of the
// box; callers that want a narrower set must list them explicitly.
func (c Config) Compile() *Engine {
	e := &Engine{cfg: c}
	if !c.Enabled {
		return e
	}
	if len(c.Categories) == 0 {
		c.Categories = []string{CatEmoji, CatKaomoji, CatFiller}
	}
	e.cfg = c
	e.excluded = append([]string(nil), c.Excluded...)

	if c.has(CatEmoji) {
		runs := [][2]rune{
			{0x1F000, 0x1FAFF},   // mahjong/domino/emoji/pictographs
			{0x2190, 0x21FF},     // arrows
			{0x2200, 0x22FF},     // math symbols
			{0x2500, 0x25FF},     // box drawing + geometric shapes + blocks
			{0x2600, 0x26FF},     // misc symbols
			{0x2700, 0x27BF},     // dingbats
			{0x2800, 0x28FF},     // braille
			{0x2E80, 0x2EFF},     // CJK radicals supplement
			{0x2F00, 0x2F5F},     // Kangxi radicals
			{0x0300, 0x036F},     // combining diacritical marks
			{0x0250, 0x02FF},     // phonetic / spacing modifiers (ᴗː˘˃˂…)
			{0x1D00, 0x1D3F},     // Latin Extended Additional (kaomoji eyes)
			{0x1DC0, 0x1DFF},     // combining diacritical supplements
			{0x20D0, 0x20FF},     // combining marks for symbols
			{0xFE00, 0xFE0F},     // variation selectors (kaomoji eyes: ︎)
			{0xFE10, 0xFE19},     // vertical forms
			{0xFE30, 0xFE4F},     // CJK forms supplement
			{0xFE70, 0xFEFF},     // CJK compatibility forms
			{0xFF61, 0xFF9F},     // halfwidth forms
			{0x0400, 0x04FF},     // Cyrillic (kaomoji letters)
			{0x0600, 0x06FF},     // Arabic + combining (kaomoji letters)
			{0x0900, 0x097F},     // Devanagari
			{0x0E00, 0x0E7F},     // Thai
			{0x1100, 0x115F},     // Hangul Jamo
			{0x3130, 0x318F},     // Hangul Compatibility Jamo
		}
		e.emoji.starts = make([]rune, len(runs))
		e.emoji.ends = make([]rune, len(runs))
		for i, r := range runs {
			e.emoji.starts[i], e.emoji.ends[i] = r[0], r[1]
		}
	}

	if c.has(CatKaomoji) {
		e.kaomoji = &pointSet{m: map[rune]bool{
			'«': true, '»': true, '‹': true, '›': true,
			'〔': true, '〕': true, '﴾': true, '﴿': true,
			'〈': true, '〉': true, '《': true, '》': true,
			'『': true, '』': true, '「': true, '」': true,
			'【': true, '】': true, '〖': true, '〗': true,
			'┄': true, '┅': true, '┆': true, '┇': true,
		}}
	}

	if c.has(CatFiller) {
		e.filler = &pointSet{m: map[rune]bool{
			'｡': true, 'ﾟ': true, '◦': true, '･': true, '•': true,
			'╮': true, '╯': true, '╰': true, '▴': true, '▾': true,
			'▔': true, '▕': true, '┃': true, '┤': true, '├': true,
			'─': true, '│': true, '┴': true, '┬': true, '╬': true,
		}}
	}
	return e
}

func (c Config) has(cat string) bool {
	for _, s := range c.Categories {
		if s == cat {
			return true
		}
	}
	return false
}

// Enabled reports whether cleaning is active.
func (e *Engine) Enabled() bool {
	if e == nil || !e.cfg.Enabled {
		return false
	}
	return e.emoji.starts != nil || e.kaomoji != nil || e.filler != nil
}

// Clean returns the cleaned text and a Stats of what was removed. Normal CJK
// sentence punctuation is never touched. mode "space" replaces a removed run
// with a single space (avoids stitching neighbouring runes together).
func (e *Engine) Clean(text string) (string, Stats) {
	st := Stats{ByCategory: map[string]int{}}
	if !e.Enabled() || text == "" {
		return text, st
	}

	var b strings.Builder
	runes := []rune(text)
	prevRemoved := false
	for _, ch := range runes {
		cat := ""
		switch {
		case e.emoji.has(ch):
			cat = CatEmoji
		case e.kaomoji.has(ch):
			cat = CatKaomoji
		case e.filler.has(ch):
			cat = CatFiller
		}
		if cat == "" {
			b.WriteRune(ch)
			prevRemoved = false
			continue
		}
		st.TotalDropped++
		st.ByCategory[cat]++
		if e.cfg.Mode == "space" {
			// one space per contiguous removed run
			if !prevRemoved {
				if b.Len() == 0 || b.String()[b.Len()-1] != ' ' {
					b.WriteByte(' ')
				}
			}
		}
		prevRemoved = true
	}
	return b.String(), st
}

// CleanExcluded strips explicit per-symbol / substring exclusions (case
// sensitive). This layers on top of the category stripping for user tuning.
func (e *Engine) CleanExcluded(s string) (string, int) {
	n := 0
	for _, sub := range e.excluded {
		if sub == "" {
			continue
		}
		if d := strings.Count(s, sub); d > 0 {
			s = strings.ReplaceAll(s, sub, "")
			n += d
		}
	}
	return s, n
}

// CountRune is a convenience wrapper.
func CountRune(s string) int { return utf8.RuneCountInString(s) }
