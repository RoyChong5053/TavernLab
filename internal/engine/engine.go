// Package engine implements the Context Budget Engine.
//
// Design (Plan v2, merged with ChatGPT review):
//   - order    decides WHERE a block lands in the final prompt.
//   - priority decides WHO gets compressed first when over budget.
//   - Never call it "16K sliding window" again. Budget is configurable
//     per model (max_tokens: auto + response_reserve).
package engine

import (
	"sort"
	"strings"
)

// Priority tiers.
const (
	PriorityLocked  = 100 // time anchor, system: never compressed
	PriorityHigh    = 90  // character, latest N turns, distilled
	PriorityNormal  = 70
	PriorityElastic = 40 // RAG, old chat: first to shrink
)

// Block is a single prompt unit in the IDE.
type Block struct {
	ID       string `json:"id"`
	Role     string `json:"role"` // system | user | assistant
	Order    int    `json:"order"`
	Priority int    `json:"priority"`
	Enabled  bool   `json:"enabled"`
	Budget   Budget `json:"budget"`
	Source   Source `json:"source"`
	Template string `json:"template"`
	// Content is resolved text for this turn (after source fetch +
	// template render). Empty means "use Template as-is".
	Content string `json:"content,omitempty"`
}

// Budget per block.
type Budget struct {
	Min int `json:"min"`
	Max int `json:"max"` // 0 = no cap
}

// Source describes where block content comes from.
type Source struct {
	Type       string `json:"type"` // static | chat | distilled | vectra | mcp
	Collection string `json:"collection,omitempty"`
}

// ContextConfig is the global budget.
type ContextConfig struct {
	MaxTokens        int `json:"max_tokens"` // 0 = auto (default 16384)
	ResponseReserve  int `json:"response_reserve"`
	RecentChatMinTurn int `json:"recent_chat_min_turns"`
}

// DefaultConfig matches the poverty setup: 16k fixed, 4k reserved
// for the reply, last 4 turns are HIGH priority protected.
func DefaultConfig() ContextConfig {
	return ContextConfig{
		MaxTokens:        16384,
		ResponseReserve:  4096,
		RecentChatMinTurn: 4,
	}
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
	Dropped    []string     `json:"dropped"`
	PromptText string       `json:"prompt_text"`
}

// Message is an OpenAI-style chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// EstimateTokens is a cheap heuristic (4 chars ~= 1 token for EN/CJK mix).
// Real billing comes from one-api; audit shows both estimate + upstream usage.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len([]rune(s))/4 + 1
	if n < 1 {
		n = 1
	}
	return n
}

// Assemble sorts by Order, then compresses ELASTIC-first when over budget.
//
// Rules:
//  1. Disabled blocks are skipped (recorded in Dropped).
//  2. Sort by Order for final position.
//  3. Budget = MaxTokens - ResponseReserve.
//  4. If over budget, shrink from lowest Priority first, never touching
//     PriorityLocked. Truncation cuts from the head (keep the tail =
//     most recent) and marks Truncated=true.
func Assemble(blocks []Block, cfg ContextConfig) AssembleResult {
	maxTok := cfg.MaxTokens
	if maxTok <= 0 {
		maxTok = 16384
	}
	budget := maxTok - cfg.ResponseReserve
	if budget <= 0 {
		budget = maxTok
	}

	// 1. filter + sort by order
	active := make([]Block, 0, len(blocks))
	var dropped []string
	for _, b := range blocks {
		if !b.Enabled {
			dropped = append(dropped, b.ID+" (disabled)")
			continue
		}
		text := b.Content
		if text == "" {
			text = b.Template
		}
		if strings.TrimSpace(text) == "" {
			dropped = append(dropped, b.ID+" (empty)")
			continue
		}
		b.Content = text
		active = append(active, b)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Order < active[j].Order })

	// 2. measure
	usage := make([]BlockUsage, 0, len(active))
	total := 0
	for _, b := range active {
		t := EstimateTokens(b.Content)
		// respect per-block Max cap at measure time (hard cut from head)
		if b.Budget.Max > 0 && t > b.Budget.Max {
			b.Content = headCut(b.Content, b.Budget.Max)
			t = EstimateTokens(b.Content)
			usage = append(usage, BlockUsage{ID: b.ID, Role: b.Role, Order: b.Order, Tokens: t, Truncated: true, DroppedNote: "over block.max"})
		} else {
			usage = append(usage, BlockUsage{ID: b.ID, Role: b.Role, Order: b.Order, Tokens: t})
		}
		total += t
		_ = b
	}

	// 3. compress ELASTIC-first while over budget
	if total > budget {
		// indices sorted by priority asc
		idx := make([]int, len(active))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool {
			return active[idx[a]].Priority < active[idx[b]].Priority
		})
		for _, ii := range idx {
			if total <= budget {
				break
			}
			if active[ii].Priority >= PriorityLocked {
				continue
			}
			over := total - budget
			cur := usage[ii].Tokens
			cut := over
			if cut > cur {
				cut = cur
			}
			// keep at least Budget.Min if set
			minKeep := active[ii].Budget.Min
			if minKeep > 0 && cur-cut < minKeep {
				cut = cur - minKeep
				if cut <= 0 {
					continue
				}
			}
			newTok := cur - cut
			active[ii].Content = headCutToTokens(active[ii].Content, newTok)
			usage[ii].Tokens = EstimateTokens(active[ii].Content)
			usage[ii].Truncated = true
			usage[ii].DroppedNote = "budget compression"
			total -= (cur - usage[ii].Tokens)
			if usage[ii].Tokens == 0 {
				dropped = append(dropped, active[ii].ID+" (fully compressed)")
			}
		}
	}

	// 4. build messages + flat text
	msgs := make([]Message, 0, len(active))
	var sb strings.Builder
	for i, b := range active {
		if usage[i].Tokens == 0 {
			continue
		}
		msgs = append(msgs, Message{Role: b.Role, Content: b.Content})
		sb.WriteString("----- [" + b.ID + " order=" + itoa(b.Order) + "] -----\n")
		sb.WriteString(b.Content)
		sb.WriteString("\n\n")
	}
	return AssembleResult{
		Messages:   msgs,
		Blocks:     usage,
		TotalTok:   total,
		BudgetTok:  budget,
		Dropped:    dropped,
		PromptText: sb.String(),
	}
}

// headCutToTokens keeps the tail of s approximating tok tokens.
func headCutToTokens(s string, tok int) string {
	if tok <= 0 {
		return ""
	}
	runes := []rune(s)
	keep := tok * 4
	if keep >= len(runes) {
		return s
	}
	return "[...truncated head...]\n" + string(runes[len(runes)-keep:])
}

func headCut(s string, maxTok int) string { return headCutToTokens(s, maxTok) }

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
