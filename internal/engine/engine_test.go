package engine

import (
	"strings"
	"testing"
)

func lockedSys(sys string) []Block {
	return []Block{
		{ID: "system", Role: "system", Order: 0, Enabled: true, Level: LevelLocked, Source: Source{Type: "static"}, Template: sys},
		{ID: "chat", Role: "user", Order: 100, Enabled: true, Level: LevelTrim, Source: Source{Type: "chat"}},
	}
}

func TestLockedFits(t *testing.T) {
	defer SetTextScale(1.0)
	res := Assemble(Input{
		Blocks: lockedSys("You are helpful."),
		Cfg:    DefaultConfig(),
		Turns:  []Message{{Role: "user", Content: "hi"}},
	})
	if res.Overflow {
		t.Fatal("unexpected overflow")
	}
	if res.BudgetTok != 16384-4096 {
		t.Fatalf("want input budget 12288, got %d", res.BudgetTok)
	}
	if len(res.Messages) < 2 {
		t.Fatalf("want system+user, got %d", len(res.Messages))
	}
}

func TestLockedOverflow(t *testing.T) {
	defer SetTextScale(1.0)
	big := strings.Repeat("字", 40000) // ~40k tokens, L1, uncuttable
	res := Assemble(Input{
		Blocks: []Block{
			{ID: "system", Role: "system", Order: 0, Enabled: true, Level: LevelLocked, Source: Source{Type: "static"}, Template: big},
			{ID: "chat", Role: "user", Order: 100, Enabled: true, Level: LevelTrim, Source: Source{Type: "chat"}},
		},
		Cfg:   DefaultConfig(),
		Turns: []Message{{Role: "user", Content: "hi"}},
	})
	if !res.Overflow {
		t.Fatal("want overflow for 40k locked block")
	}
}

func TestElasticDroppedFirst(t *testing.T) {
	defer SetTextScale(1.0)
	elastic := strings.Repeat("新", 20000) // 20k tokens, must be dropped
	res := Assemble(Input{
		Blocks: []Block{
			{ID: "system", Role: "system", Order: 0, Enabled: true, Level: LevelLocked, Source: Source{Type: "static"}, Template: "sys"},
			{ID: "news", Role: "system", Order: 60, Enabled: true, Level: LevelElastic, Source: Source{Type: "static"}, Template: elastic},
			{ID: "chat", Role: "user", Order: 100, Enabled: true, Level: LevelTrim, Source: Source{Type: "chat"}},
		},
		Cfg:   DefaultConfig(),
		Turns: []Message{{Role: "user", Content: "hi"}},
	})
	for _, m := range res.Messages {
		if strings.Contains(m.Content, "新") {
			t.Fatal("elastic block should have been dropped")
		}
	}
	found := false
	for _, d := range res.Dropped {
		if strings.Contains(d, "elastic dropped") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want elastic dropped note, got %v", res.Dropped)
	}
}

func TestChatFloorProtectsNewest(t *testing.T) {
	defer SetTextScale(1.0)
	c := ContextConfig{Window: 8192, ReplyReserve: 4096, HistoryMinTurns: 2} // keep >= 4 msgs
	var turns []Message
	for i := 0; i < 12; i++ {
		turns = append(turns, Message{Role: "user", Content: strings.Repeat("字", 1000)})
	}
	turns = append(turns, Message{Role: "user", Content: "NEWEST" + strings.Repeat("字", 1000)})
	res := Assemble(Input{Blocks: lockedSys("sys"), Cfg: c, Turns: turns})
	last := res.Messages[len(res.Messages)-1]
	if !strings.HasPrefix(last.Content, "NEWEST") {
		t.Fatalf("newest turn must survive, got %q", last.Content[:20])
	}
	// Only the newest few should fit in a 4k budget of ~1004-token turns.
	if len(res.Messages) > 6 {
		t.Fatalf("window should have evicted older turns, kept %d messages", len(res.Messages))
	}
}

func TestRAGLowestScoreEvictedFirst(t *testing.T) {
	defer SetTextScale(1.0)
	c := ContextConfig{Window: 2600, ReplyReserve: 0, HistoryMinTurns: 1}
	blocks := []Block{
		{ID: "system", Role: "system", Order: 0, Enabled: true, Level: LevelLocked, Source: Source{Type: "static"}, Template: "sys"},
		{ID: "rag_mcp", Role: "system", Order: 55, Enabled: true, Level: LevelTrim, Source: Source{Type: "mcp"}, Template: "<r>{{rag}}</r>"},
		{ID: "chat", Role: "user", Order: 100, Enabled: true, Level: LevelTrim, Source: Source{Type: "chat"}},
	}
	lists := []ItemList{{BlockID: "rag_mcp", Floor: 0, FloorFromHead: true, Items: []Item{
		{Text: "BEST" + strings.Repeat("字", 1200), EvictRank: 0.9},
		{Text: "MID" + strings.Repeat("字", 1200), EvictRank: 0.5},
		{Text: "WORST" + strings.Repeat("字", 1200), EvictRank: 0.1},
	}}}
	res := Assemble(Input{Blocks: blocks, Cfg: c, Turns: []Message{{Role: "user", Content: "hi"}}, Lists: lists})
	joined := res.PromptText
	if strings.Contains(joined, "WORST") {
		t.Fatal("lowest-score chunk must be evicted first")
	}
	if !strings.Contains(joined, "BEST") {
		t.Fatal("best chunk must survive")
	}
}

func TestImageTokensCounted(t *testing.T) {
	defer SetTextScale(1.0)
	res := Assemble(Input{
		Blocks: lockedSys("sys"),
		Cfg:    DefaultConfig(),
		Turns:  []Message{{Role: "user", Content: "look", ImageTokens: 3000}},
	})
	if res.TotalTok < 3000 {
		t.Fatalf("image tokens must count toward the budget, got %d", res.TotalTok)
	}
}

func TestTextScaleCalibration(t *testing.T) {
	defer SetTextScale(1.0)
	raw := EstimateTokens("字字字字字字字字字字") // 10 CJK runes -> raw 10
	SetTextScale(0.5)
	if got := EstimateTokens("字字字字字字字字字字"); got >= raw || got != 5 {
		t.Fatalf("scale 0.5 on raw %d: want 5, got %d", raw, got)
	}
}

func intp(v int) *int { return &v }

func TestResolvePriorityDefaults(t *testing.T) {
	cases := []struct {
		b    Block
		want int
	}{
		{Block{Source: Source{Type: "chat"}}, 0},
		{Block{Source: Source{Type: "mcp"}}, 1},
		{Block{Source: Source{Type: "vectra"}}, 1},
		{Block{Source: Source{Type: "distilled_log"}}, 2},
		{Block{Source: Source{Type: "chat"}, EvictPriority: intp(2)}, 2},
		{Block{Source: Source{Type: "mcp"}, EvictPriority: intp(0)}, 0},
	}
	for _, c := range cases {
		if got := resolvePriority(c.b); got != c.want {
			t.Fatalf("resolvePriority(%s) = %d, want %d", c.b.Source.Type, got, c.want)
		}
	}
}

func TestL2PriorityBeatsRank(t *testing.T) {
	defer SetTextScale(1.0)
	// Two equal-size L2 lists. lowpri has a HIGH rank (0.9) but priority 0;
	// highpri has a LOW rank (0.1) but priority 1. Only one item can fit.
	// The old scalar rule evicted highpri first (rank 0.1); priority must
	// instead drain the whole lowpri tier first.
	blocks := []Block{
		{ID: "system", Role: "system", Order: 0, Enabled: true, Level: LevelLocked, Source: Source{Type: "static"}, Template: "sys"},
		{ID: "lowpri", Role: "system", Order: 10, Enabled: true, Level: LevelTrim, Source: Source{Type: "mcp"}, Template: "<a>{{items}}</a>", EvictPriority: intp(0)},
		{ID: "highpri", Role: "system", Order: 20, Enabled: true, Level: LevelTrim, Source: Source{Type: "mcp"}, Template: "<b>{{items}}</b>", EvictPriority: intp(1)},
	}
	lists := []ItemList{
		{BlockID: "lowpri", Items: []Item{{Text: "LOWPRI" + strings.Repeat("字", 600), EvictRank: 0.9}}},
		{BlockID: "highpri", Items: []Item{{Text: "HIGHPRI" + strings.Repeat("字", 600), EvictRank: 0.1}}},
	}
	res := Assemble(Input{Blocks: blocks, Cfg: ContextConfig{Window: 700, ReplyReserve: 0, HistoryMinTurns: 1}, Lists: lists})
	if strings.Contains(res.PromptText, "LOWPRI") {
		t.Fatal("lower-priority tier must be evicted first regardless of rank")
	}
	if !strings.Contains(res.PromptText, "HIGHPRI") {
		t.Fatal("higher-priority tier must survive")
	}
}

func TestDistilledLogEvictsOldestDay(t *testing.T) {
	defer SetTextScale(1.0)
	// The diary is a per-day list; the floor protects the newest day, so under
	// pressure the engine drops the oldest whole day (never the newest).
	blocks := []Block{
		{ID: "system", Role: "system", Order: 0, Enabled: true, Level: LevelLocked, Source: Source{Type: "static"}, Template: "sys"},
		{ID: "distilled_log", Role: "system", Order: 40, Enabled: true, Level: LevelTrim, Source: Source{Type: "distilled_log"}, Template: "<m>{{items}}</m>"},
	}
	lists := []ItemList{{BlockID: "distilled_log", Floor: 1, FloorFromHead: false, Weight: 1, Items: []Item{
		{Text: "OLD" + strings.Repeat("字", 600), EvictRank: 0},
		{Text: "NEW" + strings.Repeat("字", 600), EvictRank: 1},
	}}}
	res := Assemble(Input{Blocks: blocks, Cfg: ContextConfig{Window: 700, ReplyReserve: 0, HistoryMinTurns: 1}, Lists: lists})
	if strings.Contains(res.PromptText, "OLD") {
		t.Fatal("oldest day must be evicted first")
	}
	if !strings.Contains(res.PromptText, "NEW") {
		t.Fatal("newest day must survive the floor")
	}
}

func TestUnresolvedDropped(t *testing.T) {
	blocks := []Block{{
		ID: "distilled", Role: "system", Order: 40, Enabled: true, Level: LevelLocked,
		Source: Source{Type: "distilled"}, Template: "<x>{{distilled}}</x>",
	}}
	res := Assemble(Input{Blocks: blocks, Cfg: DefaultConfig()})
	found := false
	for _, d := range res.Dropped {
		if strings.Contains(d, "unresolved") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want unresolved drop, got %v", res.Dropped)
	}
}
