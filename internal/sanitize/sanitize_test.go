package sanitize

import (
	"strings"
	"testing"
)

func defaultCfg() Config {
	return Config{Enabled: true, Categories: []string{CatEmoji, CatKaomoji, CatFiller}}
}

// noForbidden verifies no emoji/kaomoji/filler rune survives cleaning.
func noForbidden(t *testing.T, e *Engine, got string) {
	t.Helper()
	for _, ch := range got {
		if e.emoji.has(ch) || e.kaomoji.has(ch) || e.filler.has(ch) {
			t.Fatalf("leftover forbidden rune U+%04X in %q", ch, got)
		}
	}
}

func TestStripsRealPollution(t *testing.T) {
	e := defaultCfg().Compile()
	tests := []struct {
		name       string
		in         string
		want       string // after TrimSpace
		keepWant   string // must remain present
	}{
		{"kaomoji_face", "（｡•́︿ •̀）", "（ ）", "（"},
		{"emoticon_eyes", "（๑•̀ㅂ•́）و✧", "（）", "（"},
		{"nested_brackets", "« 「不能解析」 »", "不能解析", "不能解析"},
		{"emoji_inline", "🧠 乐儿的小型「生理学」报告", "乐儿的小型生理学报告", "乐儿的小型生理学报告"},
		{"heart_sparkle", "❤️🌈宝贝", "宝贝", "宝贝"},
		{"arrow_box", "→─┃┤╯╥▔▀", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, st := e.Clean(tc.in)
			if st.TotalDropped == 0 {
				t.Fatalf("Clean(%q): expected drops, stats %+v", tc.in, st)
			}
			noForbidden(t, e, got)
			if tc.keepWant != "" && !strings.Contains(got, tc.keepWant) {
				t.Fatalf("Clean(%q)=%q missing %q", tc.in, got, tc.keepWant)
			}
			if got2 := strings.TrimSpace(got); got2 != tc.want {
				t.Fatalf("Clean(%q) trimmed=%q want %q (raw=%q)", tc.in, got2, tc.want, got)
			}
		})
	}
}

// Normal CJK sentence punctuation must survive intact — the guardrail.
func TestNormalPunctuationPreserved(t *testing.T) {
	e := defaultCfg().Compile()
	in := "，。！？：；——……“”‘’（）"
	got, st := e.Clean(in)
	for _, ch := range in {
		if !strings.ContainsRune(got, ch) {
			t.Fatalf("punctuation U+%04X dropped", ch)
		}
	}
	if st.TotalDropped != 0 {
		t.Fatalf("normal punctuation dropped something: %+v", st)
	}
}

func TestDecorativeBracketsStripped(t *testing.T) {
	e := defaultCfg().Compile()
	got, _ := e.Clean("«»〔〕﴾﴿〈〉「」『』")
	noForbidden(t, e, got)
}

func TestDisabledIsNoop(t *testing.T) {
	e := Config{Enabled: false, Categories: []string{CatEmoji}}.Compile()
	if e.Enabled() {
		t.Fatal("disabled engine should report not-enabled")
	}
	in := "❤️🌈宝贝"
	got, st := e.Clean(in)
	if got != in || st.TotalDropped != 0 {
		t.Fatalf("disabled Clean must be identity, got %q stats %+v", got, st)
	}
}

func TestModeSpace(t *testing.T) {
	e := Config{Enabled: true, Mode: "space", Categories: []string{CatEmoji}}.Compile()
	if got, _ := e.Clean("a❤️🌈b"); got != "a b" {
		t.Fatalf("space contiguous: got %q want %q", got, "a b")
	}
	if got, _ := e.Clean("a❤️ b🌈c"); got != "a  b c" {
		t.Fatalf("space non-contiguous: got %q", got)
	}
}

func TestExcluded(t *testing.T) {
	e := Config{Enabled: true, Categories: []string{CatKaomoji}, Excluded: []string{"۶"}}.Compile()
	got, n := e.CleanExcluded("❤️۶❤️")
	if n != 1 {
		t.Fatalf("CleanExcluded n=%d want 1", n)
	}
	if strings.Count(got, "۶") != 0 {
		t.Fatalf("excluded symbol not removed: %q", got)
	}
}

// The web toggle only sends {"enabled": true} with no categories. Compile must
// fill the three calibrated defaults so cleaning is out-of-the-box. This is the
// default path the operator actually turns on.
func TestCompileFillsDefaultCategories(t *testing.T) {
	e := Config{Enabled: true}.Compile() // no Categories listed
	if !e.Enabled() {
		t.Fatal("enabled with empty categories should still strip via defaults")
	}
	got, st := e.Clean("🧠 乐儿 😎 «不能» ۶۶，。！？")
	noForbidden(t, e, got)
	if st.TotalDropped == 0 {
		t.Fatalf("expected default-category drops, stats %+v", st)
	}
	// Normal CJK punctuation must survive the same Clean call.
	for _, ch := range "，。！？" {
		if !strings.ContainsRune(got, ch) {
			t.Fatalf("punctuation U+%04X dropped", ch)
		}
	}
	// Explicit categories still win over defaults.
	e2 := Config{Enabled: true, Categories: []string{CatEmoji}}.Compile()
	got2, _ := e2.Clean("«不能»")
	if !strings.Contains(got2, "不能") {
		t.Fatalf("kaomoji not in emoji-only config, got %q", got2)
	}
}
