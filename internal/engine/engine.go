// Package engine implements the Context Budget Engine.
//
// Design (priority removed 2026-09-23):
//   - order decides WHERE a block lands in the final prompt.
//   - There is no numeric priority. Budget control is deterministic:
//     pre-compute every block's tokens, pick the smallest window tier that
//     fits, then fill the recent-chat window from the newest turn backwards.
//     Recent conversation is therefore never evicted by a large RAG block or
//     a heavy image payload — older turns are dropped instead.
//   - Window tiers are 8k / 16k / 32k (configurable). Exceeding the largest
//     tier is surfaced as Overflow (a system error), never silently sent.
package engine

import (
	"sort"
	"strings"
)

// Block is a single prompt unit in the IDE.
type Block struct {
	ID       string `json:"id"`
	Role     string `json:"role"` // system | user | assistant
	Order    int    `json:"order"`
	Enabled  bool   `json:"enabled"`
	Budget   Budget `json:"budget"`
	Source   Source `json:"source"`
	Template string `json:"template"`
	// Content is resolved text for this turn (after source fetch +
	// template render). Empty means "use Template as-is".
	Content string `json:"content,omitempty"`
}

// Budget per block. Max=0 means no cap; Min is retained for data
// compatibility but the new engine does not use it.
type Budget struct {
	Max int `json:"max"` // 0 = no cap
	Min int `json:"min,omitempty"`
}

// Source describes where block content comes from.
type Source struct {
	Type       string `json:"type"` // static | character | chat | distilled | vectra | mcp
	Collection string `json:"collection,omitempty"`
}

// ContextConfig is the global budget. Tiers is an ascending list of window
// sizes; the engine picks the smallest one that fits. MaxTokens (legacy)
// forces a single fixed window when Tiers is empty.
type ContextConfig struct {
	Tiers             []int `json:"tiers"`
	ResponseReserve   int   `json:"response_reserve"`
	RecentChatMinTurn int   `json:"recent_chat_min_turns"`
	MaxTokens         int   `json:"max_tokens"` // legacy fixed-window fallback
}

// DefaultConfig: 8k/16k/32k auto tiers, 4k reserved for the reply, at least
// the last 4 turns always kept.
func DefaultConfig() ContextConfig {
	return ContextConfig{
		Tiers:             []int{8192, 16384, 32768},
		ResponseReserve:   4096,
		RecentChatMinTurn: 4,
	}
}

// Message is an OpenAI-style chat message. ImageTokens carries the estimated
// cost of any images attached to this turn (0 for text-only) so the budget
// engine reserves room for multimodal payloads it cannot see in Content.
type Message struct {
	Role        string `json:"role"`
	Content     string `json:"content"`
	ImageTokens int    `json:"image_tokens,omitempty"`
}

// BlockUsage is per-block audit info.
type BlockUsage struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Order       int    `json:"order"`
	Tokens      int    `json:"tokens"`
	Truncated   bool   `json:"truncated"`
	DroppedNote string `json:"dropped_note,omitempty"`
}

// AssembleResult is what gets sent to the provider + saved to audit.
type AssembleResult struct {
	Messages   []Message    `json:"messages"`
	Blocks     []BlockUsage `json:"blocks"`
	TotalTok   int          `json:"total_tokens"`
	BudgetTok  int          `json:"budget_tokens"`
	Tier       int          `json:"tier"`
	Overflow   bool         `json:"overflow"`
	Dropped    []string     `json:"dropped"`
	PromptText string       `json:"prompt_text"`
}

// Input is the full assemble request.
type Input struct {
	Blocks []Block
	Cfg    ContextConfig
	Turns  []Message // structured chat history, oldest first (newest last)
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

// Assembled messages size for a turn (role + content overhead is small; we
// count content plus a flat per-message overhead).
const msgOverhead = 4

func turnsTokens(turns []Message) []int {
	out := make([]int, len(turns))
	for i, t := range turns {
		out[i] = EstimateTokens(t.Content) + msgOverhead + t.ImageTokens
	}
	return out
}

// Assemble builds the final prompt.
//
// Steps:
//  1. Keep enabled, non-empty blocks; apply each block's Budget.Max cap.
//  2. Separate the chat block (source.type=="chat") from fixed blocks.
//  3. fixedTok = sum of fixed blocks. Pick the smallest tier where
//     fixedTok + (last RecentChatMinTurn turns) fits in tier-reserve.
//  4. Fill the remaining budget with turns newest-first.
//  5. If even the largest tier cannot hold fixed + min turns -> Overflow
//     and best-effort trim elastic blocks.
func Assemble(in Input) AssembleResult {
	cfg := in.Cfg
	if len(cfg.Tiers) == 0 {
		if cfg.MaxTokens > 0 {
			cfg.Tiers = []int{cfg.MaxTokens}
		} else {
			cfg.Tiers = []int{16384}
		}
	}
	sort.Ints(cfg.Tiers)
	if cfg.ResponseReserve < 0 {
		cfg.ResponseReserve = 0
	}

	var dropped []string
	fixed := make([]Block, 0, len(in.Blocks))
	chatEnabled := false
	chatOrder := 1 << 30
	for _, b := range in.Blocks {
		if !b.Enabled {
			dropped = append(dropped, b.ID+" (disabled)")
			continue
		}
		text := b.Content
		if text == "" {
			text = b.Template
		}
		if b.Source.Type == "chat" {
			chatEnabled = true
			chatOrder = b.Order
			continue // turns are structured, not part of fixed text
		}
		if strings.TrimSpace(text) == "" {
			dropped = append(dropped, b.ID+" (empty)")
			continue
		}
		// Content empty + unresolved macro in the template means no source
		// filled this block (e.g. distilled/vectra not wired yet). Sending the
		// literal "{{...}}" would pollute the prompt, so drop it.
		if b.Content == "" && strings.Contains(text, "{{") {
			dropped = append(dropped, b.ID+" (unresolved)")
			continue
		}
		b.Content = text
		fixed = append(fixed, b)
	}
	sort.Slice(fixed, func(i, j int) bool { return fixed[i].Order < fixed[j].Order })

	// 1. per-block cap
	usage := make([]BlockUsage, 0, len(fixed))
	fixedTok := 0
	for i := range fixed {
		t := EstimateTokens(fixed[i].Content)
		cut := false
		if m := fixed[i].Budget.Max; m > 0 && t > m {
			fixed[i].Content = truncateToTokens(fixed[i].Content, m, keepHead(fixed[i].Source.Type))
			t = EstimateTokens(fixed[i].Content)
			cut = true
		}
		usage = append(usage, BlockUsage{ID: fixed[i].ID, Role: fixed[i].Role, Order: fixed[i].Order, Tokens: t, Truncated: cut})
		fixedTok += t
	}

	// 2. token size of each turn
	tok := turnsTokens(in.Turns)

	// 3. pick smallest tier fitting fixed + recent min turns
	budget := 0
	overflow := false
	chosen := cfg.Tiers[len(cfg.Tiers)-1]
	for _, tier := range cfg.Tiers {
		b := tier - cfg.ResponseReserve
		if b < 0 {
			b = 0
		}
		if fixedTok <= b && recentMinFits(tok, cfg.RecentChatMinTurn, b-fixedTok) {
			chosen = tier
			budget = b
			goto picked
		}
	}
	// nothing fits: use largest tier, best-effort, flag overflow
	chosen = cfg.Tiers[len(cfg.Tiers)-1]
	budget = chosen - cfg.ResponseReserve
	if budget < 0 {
		budget = 0
	}
	overflow = true
	// best-effort trim elastic fixed blocks (RAG/distilled) to fit recent min
	trimElastic(fixed, usage, &fixedTok, budget, tok, cfg.RecentChatMinTurn)

picked:

	// 4. choose turns newest-first within remaining budget
	chatBudget := budget - fixedTok
	if chatBudget < 0 {
		chatBudget = 0
	}
	chosenTurns, turnCut, turnDropped := slideTurns(in.Turns, tok, chatBudget, cfg.RecentChatMinTurn)

	// 5. build messages: fixed blocks in order, chat turns at chat block order
	msgs := make([]Message, 0, len(fixed)+len(chosenTurns))
	var sb strings.Builder
	for _, b := range fixed {
		msgs = append(msgs, Message{Role: b.Role, Content: b.Content})
		sb.WriteString("----- [" + b.ID + " order=" + itoa(b.Order) + "] -----\n")
		sb.WriteString(b.Content)
		sb.WriteString("\n\n")
	}
	if chatEnabled && len(chosenTurns) > 0 {
		sb.WriteString("----- [chat order=" + itoa(chatOrder) + "] -----\n")
		for _, t := range chosenTurns {
			msgs = append(msgs, t)
			sb.WriteString(t.Role + ": " + t.Content + "\n")
		}
		sb.WriteString("\n")
	}
	if len(chosenTurns) > 0 {
		tt := 0
		for _, n := range turnCut {
			tt += n
		}
		usage = append(usage, BlockUsage{ID: "chat", Role: "user", Order: chatOrder, Tokens: tt, Truncated: turnDropped > 0})
	}

	total := fixedTok
	for _, n := range turnCut {
		total += n
	}
	return AssembleResult{
		Messages:   msgs,
		Blocks:     usage,
		TotalTok:   total,
		BudgetTok:  budget,
		Tier:       chosen,
		Overflow:   overflow,
		Dropped:    dropped,
		PromptText: sb.String(),
	}
}

// recentMinFits reports whether the last n turns (or all if fewer) fit in
// the given token budget.
func recentMinFits(tok []int, n, budget int) bool {
	if n <= 0 || len(tok) == 0 {
		return true
	}
	sum := 0
	for i := len(tok) - 1; i >= 0 && len(tok)-i <= n; i-- {
		sum += tok[i]
	}
	return sum <= budget
}

// slideTurns keeps as many trailing turns as fit in budget, but always at
// least minKeep. Returns the kept turns (oldest first), their token sizes,
// and how many were dropped.
func slideTurns(turns []Message, tok []int, budget, minKeep int) ([]Message, []int, int) {
	if len(turns) == 0 {
		return nil, nil, 0
	}
	// always keep at least the newest turn
	start := len(turns) - 1
	sum := tok[start]
	for start > 0 {
		keep := len(turns) - start
		if sum+tok[start-1] > budget && keep >= minKeep {
			break
		}
		start--
		sum += tok[start]
	}
	return turns[start:], tok[start:], start
}

// trimElastic hard-trims elastic blocks (RAG/distilled/vectra) when fixed
// text alone cannot fit the largest tier, so recent chat still has room.
func trimElastic(fixed []Block, usage []BlockUsage, fixedTok *int, budget int, tok []int, minKeep int) {
	// target: fixed must leave room for min recent turns
	recent := 0
	for i := len(tok) - 1; i >= 0 && len(tok)-i <= minKeep; i-- {
		recent += tok[i]
	}
	target := budget - recent
	if target < 0 {
		target = 0
	}
	for *fixedTok > target {
		// find last elastic block with tokens
		idx := -1
		for i := len(fixed) - 1; i >= 0; i-- {
			if elastic(fixed[i].Source.Type) && usage[i].Tokens > 0 {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		over := *fixedTok - target
		cur := usage[idx].Tokens
		cut := over
		if cut > cur {
			cut = cur
		}
		newTok := cur - cut
		if newTok < 0 {
			newTok = 0
		}
		fixed[idx].Content = truncateToTokens(fixed[idx].Content, newTok, true)
		got := EstimateTokens(fixed[idx].Content)
		usage[idx].Tokens = got
		usage[idx].Truncated = true
		usage[idx].DroppedNote = "overflow trim"
		*fixedTok -= cur - got
	}
}

func elastic(t string) bool {
	switch t {
	case "mcp", "vectra", "distilled":
		return true
	}
	return false
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
	// convert token budget to a rune budget with the same script ratio
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
