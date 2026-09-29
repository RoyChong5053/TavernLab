package reitti

import "testing"

// TestHasMovement guards the caller-side check. reitti-mcp returns a
// well-formed "[Recent Movement]" header even when the window is empty, so a
// caller testing for a non-empty string would happily inject a geography-free
// block and invite the extractor to comment on it.
func TestHasMovement(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"whitespace", "   \n ", false},
		{"header only, no stays", "[Recent Movement] (Asia/Kuala_Lumpur, 2026-09-29 20:00 → 2026-09-30 02:00)\n(窗口内没有停留点记录 —— Reitti 尚未处理这段轨迹, 或该设备未上传)", false},
		{"one stay", "[Recent Movement] (Asia/Kuala_Lumpur, ...)\n- 20:00–20:30 @Petronas (加油站) — 停留 30 分钟", true},
	}
	for _, c := range cases {
		if got := HasMovement(c.text); got != c.want {
			t.Errorf("%s: HasMovement = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMovementWindowNilClientFailsOpen: a nil client must not panic and must
// report "no movement" rather than an error, because the caller treats any
// failure as "distill without geography".
func TestMovementWindowNilClientFailsOpen(t *testing.T) {
	var c *Client
	text, err := c.MovementWindow(nil, "", 0) //nolint:staticcheck // deliberate nil receiver
	if err != nil {
		t.Errorf("nil client should not error, got %v", err)
	}
	if text != "" {
		t.Errorf("nil client should return empty, got %q", text)
	}
}
