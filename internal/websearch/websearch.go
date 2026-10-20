// Package websearch parses the /web_search command and keeps the raw
// results of recent searches.
//
// The command is a frontend concern: a slash command that a human types, and
// which turns into an MCP web_search call plus a change of which collections
// are recalled. The parsing lives here so the server and the tests agree on
// what the command means; the frontend has its own copy of the same rules
// because it has to react while the user is still typing.
//
// The state is the other half: the raw material a search produced has to
// survive exactly as many turns as the operator asked for, and then leave
// the context on its own. It is kept in memory only. A restart drops it,
// which is the right behaviour — raw news from before a restart is stale
// anyway, and the vectorized copy is what matters from then on.
package websearch

import (
	"strings"
	"sync"
)

// Mode values for Command.Mode.
const (
	// ModeOn turns the web collection into the recall set, and searches
	// when the command carries a query.
	ModeOn = "on"
	// ModeOff takes the web collection back out of the recall set.
	ModeOff = "off"
)

// Command is a parsed /web_search command.
type Command struct {
	// Mode is ModeOn or ModeOff.
	Mode string
	// Query is the search text. Empty means "do not search, just change
	// which collections are recalled".
	Query string
	// Raw is the original text, kept for the chat record.
	Raw string
}

// CommandName is the slash command itself.
const CommandName = "/web_search"

// Parse reads a /web_search command out of text. The second result is false
// when the text is not a command at all.
//
// The accepted forms:
//
//	/web_search                      on, no query
//	/web_search on                   on, no query
//	/web_search off                  off
//	/web_search on "some query"      on, with a query
//	/web_search "some query"         on, with a query
//
// A bare /web_search means "on": turning the collection on is the common
// case, and asking for it twice costs nothing.
func Parse(text string) (Command, bool) {
	t := strings.TrimSpace(text)
	if len(t) < len(CommandName) || !strings.EqualFold(t[:len(CommandName)], CommandName) {
		return Command{}, false
	}
	raw := t[len(CommandName):]
	// The command name has to end here: "/web_searchfoo" is not a command.
	// The check runs on the untrimmed remainder, so the space that
	// separates the command from its arguments is still there.
	if raw != "" && !strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "\t") {
		return Command{}, false
	}
	rest := strings.TrimSpace(raw)
	cmd := Command{Mode: ModeOn, Raw: t}
	if rest == "" {
		return cmd, true
	}
	word, tail := splitFirst(rest)
	switch strings.ToLower(word) {
	case ModeOff:
		cmd.Mode = ModeOff
		cmd.Query = unquote(tail)
	case ModeOn:
		cmd.Query = unquote(tail)
	default:
		// No mode keyword: whatever follows is the query.
		cmd.Query = unquote(rest)
	}
	return cmd, true
}

// splitFirst cuts the first whitespace-separated word off a string.
func splitFirst(s string) (word, tail string) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

// unquote strips one layer of matching quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	return s
}

// Entry is one stored search result.
type Entry struct {
	// Text is the provider's answer, verbatim.
	Text string
	// Turn is the user-turn the search happened on. The current turn
	// number is the number of user messages in the session.
	Turn int
}

// State holds the recent search results of every session.
type State struct {
	mu        sync.Mutex
	bySession map[string][]Entry
}

// NewState builds an empty state.
func NewState() *State {
	return &State{bySession: map[string][]Entry{}}
}

// Put records a search result for a session.
func (s *State) Put(session, text string, turn int) {
	if s == nil || strings.TrimSpace(text) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bySession == nil {
		s.bySession = map[string][]Entry{}
	}
	s.bySession[session] = append(s.bySession[session], Entry{Text: text, Turn: turn})
}

// Clear drops every stored result of a session.
func (s *State) Clear(session string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bySession, session)
}

// Get returns the results that are still within keepTurns turns of the
// current one. A result recorded on the current turn is always in; one from
// keepTurns turns ago is not.
//
// keepTurns <= 0 means "this turn only", which is the default: the raw
// material is fresh information, and the vectorized copy is what carries it
// further.
func (s *State) Get(session string, turn, keepTurns int) []string {
	if s == nil {
		return nil
	}
	if keepTurns <= 0 {
		keepTurns = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.bySession[session]
	if len(entries) == 0 {
		return nil
	}
	var out []string
	kept := entries[:0]
	for _, e := range entries {
		if turn-e.Turn < keepTurns {
			kept = append(kept, e)
			out = append(out, e.Text)
		}
	}
	// Anything older is dropped for good: the session map must not grow
	// without bound.
	if len(kept) == 0 {
		delete(s.bySession, session)
	} else {
		s.bySession[session] = kept
	}
	return out
}
