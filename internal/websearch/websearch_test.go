package websearch

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in    string
		ok    bool
		mode  string
		query string
	}{
		{in: "/web_search", ok: true, mode: ModeOn},
		{in: "/web_search on", ok: true, mode: ModeOn},
		{in: "/web_search off", ok: true, mode: ModeOff},
		{in: "/web_search ON", ok: true, mode: ModeOn},
		{in: "/web_search OFF", ok: true, mode: ModeOff},
		{in: `/web_search on "RTX spark laptop"`, ok: true, mode: ModeOn, query: "RTX spark laptop"},
		{in: `/web_search on 'RTX spark laptop'`, ok: true, mode: ModeOn, query: "RTX spark laptop"},
		{in: `/web_search "RTX spark laptop"`, ok: true, mode: ModeOn, query: "RTX spark laptop"},
		{in: "/web_search on RTX spark laptop", ok: true, mode: ModeOn, query: "RTX spark laptop"},
		{in: "/web_search off web_cache", ok: true, mode: ModeOff, query: "web_cache"},
		{in: "  /web_search on  \"x\"  ", ok: true, mode: ModeOn, query: "x"},
		{in: "/web_searchfoo", ok: false},
		{in: "hello /web_search", ok: false},
		{in: "", ok: false},
		{in: "web_search", ok: false},
	}
	for _, tc := range cases {
		cmd, ok := Parse(tc.in)
		if ok != tc.ok {
			t.Errorf("Parse(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if cmd.Mode != tc.mode {
			t.Errorf("Parse(%q).Mode = %q, want %q", tc.in, cmd.Mode, tc.mode)
		}
		if cmd.Query != tc.query {
			t.Errorf("Parse(%q).Query = %q, want %q", tc.in, cmd.Query, tc.query)
		}
	}
}

func TestStateRetention(t *testing.T) {
	s := NewState()
	s.Put("char1", "raw web result", 5)

	if got := s.Get("char1", 5, 1); len(got) != 1 {
		t.Fatalf("the search turn must keep its result, got %v", got)
	}
	if got := s.Get("char1", 6, 1); len(got) != 0 {
		t.Fatalf("keep_turns=1 must drop the result on the next turn, got %v", got)
	}

	s.Put("char1", "raw web result", 5)
	if got := s.Get("char1", 7, 3); len(got) != 1 {
		t.Fatalf("keep_turns=3 must keep the result two turns later, got %v", got)
	}
	if got := s.Get("char1", 8, 3); len(got) != 0 {
		t.Fatalf("keep_turns=3 must drop the result on the third turn, got %v", got)
	}
}

func TestStateIsPerSession(t *testing.T) {
	s := NewState()
	s.Put("a", "result a", 1)
	if got := s.Get("b", 1, 1); len(got) != 0 {
		t.Fatalf("sessions must not share results, got %v", got)
	}
	s.Clear("a")
	if got := s.Get("a", 1, 1); len(got) != 0 {
		t.Fatalf("Clear must drop the results, got %v", got)
	}
}

func TestStatePrunesOldEntries(t *testing.T) {
	s := NewState()
	s.Put("a", "old", 1)
	s.Get("a", 99, 1) // forces the prune
	s.mu.Lock()
	n := len(s.bySession["a"])
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("expired entries must be dropped, %d left", n)
	}
}
