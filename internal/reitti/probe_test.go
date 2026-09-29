//go:build live

// Live probe: builds the REAL distill user message against a running
// reitti-mcp and prints exactly what the LLM will see. This is the "look before
// you spend tokens" check for prompt work — TavernLab is a prompts IDE, and the
// only honest way to tune the geography rules is to read the actual input.
//
//	go test -tags live ./internal/reitti/ -run TestLiveProbe -v
//
// Requires REITTI_MCP_URL (e.g. http://192.168.100.78:8200). Skips otherwise so
// `go test ./...` stays hermetic.

package reitti

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/distill"
)

func TestLiveProbe(t *testing.T) {
	url := os.Getenv("REITTI_MCP_URL")
	if url == "" {
		t.Skip("REITTI_MCP_URL not set; skipping live probe")
	}
	rc := New(url, "Asia/Kuala_Lumpur", 20*time.Second)
	since := time.Now().Add(-14 * time.Hour).Format(time.RFC3339)
	text, err := rc.MovementWindow(context.Background(), since, 0)
	if err != nil {
		t.Fatalf("MovementWindow: %v", err)
	}
	t.Logf("err=%v hasMovement=%v", err, HasMovement(text))

	state := "[30-09-2026] 今天在调试 TavernLab 的蒸馏流程。"
	recent := "[29-09-2026 22:47] 回到 D'Quince Residences, Petaling Jaya。\n" +
		"[29-09-2026 23:15] 在家附近 99 Speedmart 买了东西，停留 8 分钟。"
	timeline := "**Leer乐儿** [30-09-2026 03:40]: 还在改代码，又改到半夜，饿得点了外卖。\n" +
		"**user** [30-09-2026 03:41]: 你应该出去走走，别老待在电脑前。"

	out := "\n" + fmt.Sprint(
		"================ SYSTEM PROMPT ================\n", distill.DefaultPrompt,
		"\n================ USER MESSAGE ================\n",
		distill.BuildUser(state, recent, text, timeline))
	t.Log(out)
}
