// Package engine implements the Context Budget Engine.
//
// Design (2026-09-26: sliding-window rewrite):
//   - context_window is the TOTAL budget (input + reply), SillyTavern-style.
//     input_budget = context_window - reply_reserve.
//   - Every block carries a Level:
//   - L1 LevelLocked  : never trimmed. If L1 alone exceeds input_budget the
//     result is Overflow (a system error), never a silent send.
//   - L2 LevelTrim    : evictable, kept above a floor. Chat turns and RAG hits
//     share ONE eviction primitive (an ordered unit list + eviction rank +
//     floor), so a future source (news cards, ...) plugs in with no new code.
//   - L3 LevelElastic : dropped entirely before any L2 item is touched.
//
// Eviction is global across all L2 lists by a normalised 0..1 "staleness /
// irrelevance" rank (0 = cut first): chat is ranked by age (oldest first),
// RAG by reranker score (lowest first). Floors protect the newest turns and
// the best hits.
package engine

import (
	"math"
	"sort"
	"strings"
)

// Level is the eviction tier. Zero value is treated as LevelLocked for
// legacy block data (safest: never silently trimmed).
type Level int

const (
	LevelLocked  Level = 1 // L1: never trimmed
	LevelTrim    Level = 2 // L2: evictable, guarded by a floor
	LevelElastic Level = 3 // L3: dropped entirely first
)

// Block is a single prompt unit in the IDE.
type Block struct {
	ID       string `json:"id"`
	Role     string `json:"role"` // system | user | assistant
	Order    int    `json:"order"`
	Enabled  bool   `json:"enabled"`
	Level    Level  `json:"level"`
	Budget   Budget `json:"budget"`
	Source   Source `json:"source"`
	Template string `json:"template"`
	// EvictPriority orders L2 blocks against each other: lower is evicted
	// first, and the whole tier is drained (down to its floor) before the
	// next tier is touched. nil = infer from Source.Type so heterogeneous
	// lists never share one 0..1 scale (the "average everyone" bug).
	EvictPriority *int `json:"evict_priority,omitempty"`
	// Content is resolved text for this turn (after source fetch +
	// template render). Empty means "use Template as-is" or "filled from a
	// list source at assemble time".
	Content string `json:"content,omitempty"`
}

// Budget per block. Max=0 means no cap; Min is retained for data
// compatibility but the engine does not use it.
type Budget struct {
	Max int `json:"max"` // 0 = no cap
	Min int `json:"min,omitempty"`
}

// Source describes where block content comes from.
type Source struct {
	Type       string `json:"type"` // static | character | chat | distilled | vectra | mcp | list
	Collection string `json:"collection,omitempty"`
}

// ContextConfig is the global budget. Window is the total context (input +
// reply) and ReplyReserve is what is held back for the model's answer; the
// input budget the sliding window fills is Window - ReplyReserve.
type ContextConfig struct {
	Window          int `json:"context_window"`
	ReplyReserve    int `json:"reply_reserve"`
	HistoryMinTurns int `json:"history_min_turns"`
}

// DefaultConfig: 16k total window, 4k reply, keep at least 4 rounds of chat.
func DefaultConfig() ContextConfig {
	return ContextConfig{Window: 16384, ReplyReserve: 4096, HistoryMinTurns: 4}
}

// Message is an OpenAI-style chat message. ImageTokens carries the estimated
// cost of the images actually sent on this turn so the budget engine reserves
// room for the multimodal payload it cannot see in Content.
type Message struct {
	Role        string `json:"role"`
	Content     string `json:"content"`
	ImageTokens int    `json:"image_tokens,omitempty"`
}

// Item is one evictable unit inside an L2 list block (a chat turn, a RAG hit,
// a future news card...). EvictRank is a normalised 0..1 score where LOWER is
// evicted first; it is only used for non-chat lists (chat is ranked by age).
type Item struct {
	Role        string  `json:"role,omitempty"`
	Text        string  `json:"text"`
	ImageTokens int     `json:"image_tokens,omitempty"`
	EvictRank   float64 `json:"evict_rank,omitempty"`
}

// ItemList binds evictable items to a block id (chat blocks build their list
// from Input.Turns instead).
type ItemList struct {
	BlockID       string
	Items         []Item
	Floor         int  // min items to keep
	FloorFromHead bool // true: protect the first N items (best-scored first)
	Weight        float64
}

// BlockUsage is per-block audit info.
type BlockUsage struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Order       int    `json:"order"`
	Level       int    `json:"level"`
	Tokens      int    `json:"tokens"`
	Truncated   bool   `json:"truncated"`
	DroppedNote string `json:"dropped_note,omitempty"`
}

// AssembleResult is what gets sent to the provider + saved to audit.
type AssembleResult struct {
	Messages   []Message    `json:"messages"`
	Blocks     []BlockUsage `json:"blocks"`
	TotalTok   int          `json:"total_tokens"`
	BudgetTok  int          `json:"budget_tokens"` // input budget
	Window     int          `json:"window"`
	Overflow   bool         `json:"overflow"`
	Dropped    []string     `json:"dropped"`
	PromptText string       `json:"prompt_text"`
}

// Input is the full assemble request.
type Input struct {
	Blocks []Block
	Cfg    ContextConfig
	Turns  []Message  // structured chat history, oldest first (newest last)
	Lists  []ItemList // extra L2 list sources (RAG hits, future cards)
}

// EstimateTokens is a script-aware heuristic: CJK/kana/hangul runs count
// ~1 token per rune, other text ~4 runes per token. Much closer than a flat
// len/4 for Chinese, and calibrated against upstream usage in Audit.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, other := 0, 0
	for _, r := range s {
		if isWide(r) {
			cjk++
		} else {
			other++
		}
	}
	return ScaleTokens(cjk + (other+3)/4)
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // hangul jamo
		r >= 0x2E80 && r <= 0x303F, // CJK radicals .. CJK symbols/punct
		r >= 0x3040 && r <= 0x30FF, // hiragana + katakana
		r >= 0x3130 && r <= 0x318F, // hangul compatibility jamo
		r >= 0x3400 && r <= 0x4DBF, // CJK ext A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified
		r >= 0xAC00 && r <= 0xD7AF, // hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFF00 && r <= 0xFF60: // fullwidth forms
		return true
	}
	return false
}

// msgOverhead approximates the per-message role/format overhead.
const msgOverhead = 4

// l2List is the runtime form of an L2 evictable list.
type l2List struct {
	id        string
	role      string
	order     int
	isChat    bool
	tmpl      string
	items     []Item
	tok       []int
	rank      []float64
	protected []bool
	keep      []bool
	weight    float64
	priority  int
	sumAll    int
}

// resolvePriority returns the L2 eviction tier for a block (lower = evicted
// first). An explicit EvictPriority always wins; otherwise it is inferred from
// the source so the default order is chat(0) -> RAG(1) -> distilled log(2):
// recent chat is squeezed to its floor first, retrieval next, the diary last.
func resolvePriority(b Block) int {
	if b.EvictPriority != nil {
		return *b.EvictPriority
	}
	switch b.Source.Type {
	case "chat":
		return 0
	case "distilled_log":
		return 2
	case "mcp", "vectra":
		return 1
	}
	return 1
}

// Assemble builds the final prompt.
func Assemble(in Input) AssembleResult {
	cfg := in.Cfg
	if cfg.Window <= 0 {
		cfg.Window = 16384
	}
	if cfg.ReplyReserve < 0 {
		cfg.ReplyReserve = 0
	}
	minTurns := cfg.HistoryMinTurns
	if minTurns <= 0 {
		minTurns = 4
	}
	minMsgs := minTurns * 2

	inputBudget := cfg.Window - cfg.ReplyReserve
	if inputBudget < 0 {
		inputBudget = 0
	}

	var dropped []string

	lockedText := map[string]string{}
	lockedTok := map[string]int{}
	elasticText := map[string]string{}
	elasticTok := map[string]int{}
	l2byID := map[string]*l2List{}
	var l2order []string

	listByID := map[string]ItemList{}
	for _, il := range in.Lists {
		listByID[il.BlockID] = il
	}

	// Enabled blocks in prompt order (also the emission order later).
	ordered := make([]Block, 0, len(in.Blocks))
	for _, b := range in.Blocks {
		if !b.Enabled {
			dropped = append(dropped, b.ID+" (disabled)")
			continue
		}
		ordered = append(ordered, b)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })

	// Classify blocks into L1 / L2 / L3.
	for _, b := range ordered {
		if b.Source.Type == "chat" {
			items := make([]Item, len(in.Turns))
			for i, t := range in.Turns {
				items[i] = Item{Role: t.Role, Text: t.Content, ImageTokens: t.ImageTokens}
			}
			l2byID[b.ID] = &l2List{id: b.ID, role: b.Role, order: b.Order, isChat: true, items: items, weight: 1, priority: resolvePriority(b)}
			l2order = append(l2order, b.ID)
			continue
		}
		if il, ok := listByID[b.ID]; ok {
			l := &l2List{id: b.ID, role: b.Role, order: b.Order, tmpl: b.Template, items: il.Items, weight: il.Weight, priority: resolvePriority(b)}
			if l.weight <= 0 {
				l.weight = 1
			}
			n := len(l.items)
			l.protected = make([]bool, n)
			f := il.Floor
			if f > n {
				f = n
			}
			if il.FloorFromHead {
				for i := 0; i < f; i++ {
					l.protected[i] = true
				}
			} else {
				for i := n - f; i < n; i++ {
					if i >= 0 {
						l.protected[i] = true
					}
				}
			}
			l2byID[b.ID] = l
			l2order = append(l2order, b.ID)
			continue
		}
		// Fixed text block.
		text := b.Content
		if text == "" {
			text = b.Template
		}
		if strings.TrimSpace(text) == "" {
			dropped = append(dropped, b.ID+" (empty)")
			continue
		}
		// Unresolved macro with no resolved content means no source filled it
		// (e.g. distilled/vectra not wired). Sending the literal "{{...}}"
		// would pollute the prompt, so drop it.
		if b.Content == "" && strings.Contains(text, "{{") {
			dropped = append(dropped, b.ID+" (unresolved)")
			continue
		}
		if m := b.Budget.Max; m > 0 && EstimateTokens(text) > m {
			text = truncateToTokens(text, m, keepHead(b.Source.Type))
		}
		tk := EstimateTokens(text)
		switch b.Level {
		case LevelElastic:
			elasticText[b.ID] = text
			elasticTok[b.ID] = tk
		case LevelTrim:
			// A fixed L2 block is a one-item evictable list.
			l2byID[b.ID] = &l2List{id: b.ID, role: b.Role, order: b.Order, items: []Item{{Text: text}}, weight: 0.5, priority: resolvePriority(b)}
			l2order = append(l2order, b.ID)
		default:
			lockedText[b.ID] = text
			lockedTok[b.ID] = tk
		}
	}

	// Prepare L2 arrays (tokens + eviction rank + protected floor).
	for _, id := range l2order {
		l := l2byID[id]
		n := len(l.items)
		l.tok = make([]int, n)
		l.rank = make([]float64, n)
		l.keep = make([]bool, n)
		if l.protected == nil {
			l.protected = make([]bool, n)
		}
		for i := range l.items {
			l.tok[i] = EstimateTokens(l.items[i].Text) + l.items[i].ImageTokens
			if l.isChat {
				l.tok[i] += msgOverhead
			}
			l.keep[i] = true
			l.sumAll += l.tok[i]
		}
		if l.isChat {
			if n > 1 {
				for i := range l.items {
					l.rank[i] = float64(i) / float64(n-1)
				}
			} else if n == 1 {
				l.rank[0] = 1
			}
			f := minMsgs
			if f > n {
				f = n
			}
			for i := n - f; i < n; i++ {
				if i >= 0 {
					l.protected[i] = true
				}
			}
		} else {
			mn, mx := math.Inf(1), math.Inf(-1)
			for i := range l.items {
				r := l.items[i].EvictRank
				if r < mn {
					mn = r
				}
				if r > mx {
					mx = r
				}
			}
			for i := range l.items {
				if mx > mn {
					l.rank[i] = (l.items[i].EvictRank - mn) / (mx - mn)
				} else {
					l.rank[i] = 0
				}
			}
		}
	}

	// Totals.
	lockedTotal := 0
	for _, id := range lockedText {
		lockedTotal += lockedTok[id]
	}
	elasticTotal := 0
	for id := range elasticText {
		elasticTotal += elasticTok[id]
	}
	l2Total := 0
	for _, id := range l2order {
		l2Total += l2byID[id].sumAll
	}

	overflow := lockedTotal > inputBudget
	target := inputBudget - lockedTotal
	if target < 0 {
		target = 0
	}

	// Evict L2 items until they fit the target. Eviction is lexicographic:
	// lower EvictPriority first, then (within a tier) lowest weighted rank.
	// This drains a tier down to its floor before touching the next, instead
	// of averaging heterogeneous sources on one 0..1 scale.
	cur := l2Total
	for cur > target {
		var bi *l2List
		bii := -1
		bestP := 0
		bestER := 0.0
		for _, id := range l2order {
			l := l2byID[id]
			for i := range l.items {
				if !l.keep[i] || l.protected[i] {
					continue
				}
				er := l.rank[i] * l.weight
				if bi == nil || l.priority < bestP || (l.priority == bestP && er < bestER) {
					bi, bii, bestP, bestER = l, i, l.priority, er
				}
			}
		}
		if bi == nil {
			break
		}
		bi.keep[bii] = false
		cur -= bi.tok[bii]
	}
	l2Kept := cur
	if cur > target {
		overflow = true
	}

	// L3 is all-or-nothing, and only after L1 + L2 are placed.
	keepElastic := false
	if elasticTotal > 0 {
		if room := inputBudget - lockedTotal - l2Kept; elasticTotal <= room {
			keepElastic = true
		}
	}

	// Emit messages in block order.
	var msgs []Message
	var sb strings.Builder
	var usage []BlockUsage
	total := 0

	writeHeader := func(id string, order int) {
		sb.WriteString("----- [" + id + " order=" + itoa(order) + "] -----\n")
	}

	for _, b := range ordered {
		id := b.ID
		if l, ok := l2byID[id]; ok {
			if l.isChat {
				kept := 0
				any := false
				for i := range l.items {
					if !l.keep[i] {
						continue
					}
					any = true
					kept += l.tok[i]
					msgs = append(msgs, Message{Role: l.items[i].Role, Content: l.items[i].Text, ImageTokens: l.items[i].ImageTokens})
					sb.WriteString(l.items[i].Role + ": " + l.items[i].Text + "\n")
				}
				if any {
					total += kept
					usage = append(usage, BlockUsage{ID: id, Role: b.Role, Order: b.Order, Level: int(LevelTrim), Tokens: kept, Truncated: kept < l.sumAll})
				}
				continue
			}
			var parts []string
			for i := range l.items {
				if l.keep[i] {
					parts = append(parts, l.items[i].Text)
				}
			}
			if len(parts) > 0 {
				content := renderListContent(l.tmpl, strings.Join(parts, "\n\n"))
				tk := EstimateTokens(content)
				msgs = append(msgs, Message{Role: b.Role, Content: content})
				total += tk
				usage = append(usage, BlockUsage{ID: id, Role: b.Role, Order: b.Order, Level: int(LevelTrim), Tokens: tk, Truncated: len(parts) < len(l.items)})
				writeHeader(id, b.Order)
				sb.WriteString(content + "\n\n")
			} else if l.sumAll > 0 {
				dropped = append(dropped, id+" (evicted)")
			}
			continue
		}
		// Fixed L1 / L3.
		if b.Level == LevelElastic {
			if !keepElastic {
				dropped = append(dropped, id+" (elastic dropped)")
				continue
			}
			text := elasticText[id]
			msgs = append(msgs, Message{Role: b.Role, Content: text})
			total += elasticTok[id]
			usage = append(usage, BlockUsage{ID: id, Role: b.Role, Order: b.Order, Level: int(LevelElastic), Tokens: elasticTok[id]})
			writeHeader(id, b.Order)
			sb.WriteString(text + "\n\n")
			continue
		}
		if text, ok := lockedText[id]; ok {
			msgs = append(msgs, Message{Role: b.Role, Content: text})
			total += lockedTok[id]
			usage = append(usage, BlockUsage{ID: id, Role: b.Role, Order: b.Order, Level: int(LevelLocked), Tokens: lockedTok[id]})
			writeHeader(id, b.Order)
			sb.WriteString(text + "\n\n")
		}
	}

	if total > inputBudget {
		overflow = true
	}

	return AssembleResult{
		Messages:   msgs,
		Blocks:     usage,
		TotalTok:   total,
		BudgetTok:  inputBudget,
		Window:     cfg.Window,
		Overflow:   overflow,
		Dropped:    dropped,
		PromptText: sb.String(),
	}
}

// renderListContent substitutes joined items into a list block template.
func renderListContent(tmpl, joined string) string {
	if joined == "" {
		return ""
	}
	if tmpl == "" {
		return joined
	}
	if strings.Contains(tmpl, "{{rag}}") {
		return strings.ReplaceAll(tmpl, "{{rag}}", joined)
	}
	if strings.Contains(tmpl, "{{items}}") {
		return strings.ReplaceAll(tmpl, "{{items}}", joined)
	}
	if strings.Contains(tmpl, "{{") {
		return tmpl + "\n" + joined
	}
	return joined
}

// keepHead reports whether truncation should preserve the beginning
// (RAG hits are best-first; distilled state reads top-down).
func keepHead(t string) bool {
	switch t {
	case "mcp", "vectra", "distilled":
		return true
	}
	return false
}

// truncateToTokens keeps at most tok estimated tokens. keepHead=true cuts
// the tail, otherwise cuts the head (keeping the most recent).
func truncateToTokens(s string, tok int, head bool) string {
	if tok <= 0 {
		return ""
	}
	if EstimateTokens(s) <= tok {
		return s
	}
	runes := []rune(s)
	keep := estimateRuneBudget(s, tok)
	if keep >= len(runes) {
		return s
	}
	if head {
		return string(runes[:keep]) + "\n[...truncated...]"
	}
	return "[...truncated...]\n" + string(runes[len(runes)-keep:])
}

// estimateRuneBudget approximates how many runes fit in tok tokens using the
// same script ratio as EstimateTokens.
func estimateRuneBudget(s string, tok int) int {
	total := EstimateTokens(s)
	if total <= 0 {
		return 0
	}
	runes := len([]rune(s))
	budget := runes * tok / total
	if budget < 1 {
		budget = 1
	}
	return budget
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [16]byte
	p := len(b)
	for n > 0 {
		p--
		b[p] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
