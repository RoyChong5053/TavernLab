package engine

import (
	"strings"
	"testing"
)

func fixedBlocks(sys string) []Block {
	return []Block{
		{
			ID: "system", Role: "system", Order: 0, Enabled: true,
			Budget: Budget{Max: 2000}, Source: Source{Type: "static"}, Template: sys,
		},
		{ID: "chat", Role: "user", Order: 100, Enabled: true, Source: Source{Type: "chat"}},
	}
}

func TestTierSmallest(t *testing.T) {
	res := Assemble(Input{
		Blocks: fixedBlocks("You are helpful."),
		Cfg:    DefaultConfig(),
		Turns:  []Message{{Role: "user", Content: "hi"}},
	})
	if res.Tier != 8192 {
		t.Fatalf("want tier 8192, got %d", res.Tier)
	}
	if res.Overflow {
		t.Fatal("unexpected overflow")
	}
	if len(res.Messages) < 2 {
		t.Fatalf("want system+user, got %d", len(res.Messages))
	}
}

func TestRecentChatProtectedNewestFirst(t *testing.T) {
	var turns []Message
	// 400 turns, each ~4000 CJK chars (~4000 tokens) => min 4 turns = 16k,
	// which only fits the 32k tier after the 4k reply reserve.
	for i := 0; i < 400; i++ {
		turns = append(turns, Message{Role: "user", Content: strings.Repeat("字", 4000)})
	}
	turns = append(turns, Message{Role: "user", Content: "NEWEST" + strings.Repeat("字", 4000)})
	res := Assemble(Input{
		Blocks: fixedBlocks("sys"),
		Cfg:    DefaultConfig(),
		Turns:  turns,
	})
	if res.Tier != 32768 {
		t.Fatalf("want top tier 32768, got %d", res.Tier)
	}
	last := res.Messages[len(res.Messages)-1]
	if !strings.HasPrefix(last.Content, "NEWEST") {
		t.Fatalf("newest turn must survive, got %q", last.Content[:20])
	}
}

func TestOverflow(t *testing.T) {
	big := strings.Repeat("字", 40000) // ~40k tokens fixed, no cap
	res := Assemble(Input{
		Blocks: []Block{
			{ID: "system", Role: "system", Order: 0, Enabled: true, Source: Source{Type: "static"}, Template: big},
			{ID: "chat", Role: "user", Order: 100, Enabled: true, Source: Source{Type: "chat"}},
		},
		Cfg:   DefaultConfig(),
		Turns: []Message{{Role: "user", Content: "hi"}},
	})
	if !res.Overflow {
		t.Fatal("want overflow for 40k fixed block")
	}
}

func TestImageTokensRaiseTier(t *testing.T) {
	turns := []Message{{Role: "user", Content: "look at this"}}
	plain := Assemble(Input{Blocks: fixedBlocks("sys"), Cfg: DefaultConfig(), Turns: turns})
	withImg := Assemble(Input{Blocks: fixedBlocks("sys"), Cfg: DefaultConfig(), Turns: []Message{
		{Role: "user", Content: "look at this", ImageTokens: 9000},
	}})
	if plain.Tier != 8192 {
		t.Fatalf("text-only should fit 8k, got %d", plain.Tier)
	}
	if withImg.Tier != 16384 {
		t.Fatalf("image turn should force 16k, got %d", withImg.Tier)
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

func TestUnresolvedDropped(t *testing.T) {
	blocks := []Block{{
		ID: "distilled", Role: "system", Order: 40, Enabled: true,
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
