package main

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/mcp"
	"github.com/RoyChong5053/TavernLab/internal/obs"
	"github.com/RoyChong5053/TavernLab/internal/settings"
	"github.com/RoyChong5053/TavernLab/internal/store"
	"github.com/RoyChong5053/TavernLab/internal/websearch"
)

// webSearchCollection is the rag-mcp-server collection that web pages are
// vectorized into. It is a scratch pad for web content, not a knowledge base
// of its own: a character recalls it only while the operator wants it to.
const webSearchCollection = "web_cache"

// webSearchRole is the chat role of the visible record a /web_search command
// leaves behind. It is deliberately not user, assistant or system, so the
// prompt never sees it: the record is for the human reading the transcript.
const webSearchRole = "web_search"

// webSearchState holds the raw results of recent searches, per session.
var webSearchState = websearch.NewState()

// webSearchRequest is what the frontend sends along with a message when the
// composer held a /web_search command.
//
// The frontend parses the command (it has to, to show the chip), so the
// server receives the mode and the query already separated.
type webSearchRequest struct {
	// Mode is "on" or "off".
	Mode string `json:"mode"`
	// Query is the search text; empty means "no search, just switch the
	// collection".
	Query string `json:"query"`
}

// userTurnCount is the number of user messages in a turn list, which is the
// turn number a search belongs to.
func userTurnCount(turns []engine.Message) int {
	n := 0
	for _, t := range turns {
		if t.Role == "user" {
			n++
		}
	}
	return n
}

// webSearchKeepTurns reads how many turns the raw search result should stay
// in the context, from the web_search module itself. 0 = this turn only.
func webSearchKeepTurns(modules []engine.Module) int {
	for _, m := range modules {
		if m.Kind == engine.KindWebSearch {
			if m.KeepTurns > 0 {
				return m.KeepTurns
			}
			return 1
		}
	}
	return 1
}

// setWebSearchContent puts the raw search result into the web_search module.
// It runs before renderModules, which wraps it in the module template.
func setWebSearchContent(modules []engine.Module, content string) {
	for i := range modules {
		if modules[i].Kind != engine.KindWebSearch {
			continue
		}
		modules[i].Content = content
	}
}

// setWebRecall adds the web collection to the character's recall set, or
// takes it out.
//
// The change lands in the character profile, so it is visible on the Memory
// page and survives a restart. That matters: some characters want the web
// collection always (an assistant that must know today's news) and some must
// not have it (a character whose own memory is enough, and whose recall
// quality would suffer from a collection full of near-neighbours).
func setWebRecall(root string, s settings.Settings, session string, enable bool) error {
	base := charBase(root, session)
	prof, ok := loadCharProfile(base)
	if !ok {
		prof = profileFor(root, s, session)
	}
	cols := cleanList(prof.RAG.Collections)
	out := make([]string, 0, len(cols)+1)
	found := false
	for _, c := range cols {
		if c == webSearchCollection {
			found = true
			if !enable {
				continue
			}
		}
		out = append(out, c)
	}
	if enable && !found {
		out = append(out, webSearchCollection)
	}
	prof.RAG.Collections = out
	return saveCharProfile(base, prof)
}

// webSearchMCP calls the server's web_search tool.
func webSearchMCP(ctx context.Context, s settings.Settings, query string) (string, error) {
	timeout := mcpTimeout(s)
	// Detached from the request context, like the memory search: the pages
	// are vectorized on the server side, and that is worth finishing even if
	// the client goes away mid-search.
	searchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return mcp.NewWithTimeout(firstNonEmpty(s.MCPURL, "http://192.168.10.2:8199"), timeout).
		WebSearch(searchCtx, query, 0)
}

// handleWebSearchTurn runs the /web_search command for one turn.
//
// Two independent things happen:
//
//   - a query, if there is one, is searched, and the raw result is kept for
//     the module to inject;
//   - the web collection joins (or leaves) the character's recall set, which
//     is what lets later turns recall what was found.
//
// A failing search is not fatal. The turn continues without the raw material
// and the error goes to the log; the character still recalls the web
// collection, which by now holds the material of every earlier search. The
// command is a last resort for facts the model cannot know, not a lifeline:
// if even this finds nothing, there is nothing to find.
func handleWebSearchTurn(ctx context.Context, st *store.Store, s settings.Settings, root, session string, cmd websearch.Command, turns []engine.Message) map[string]any {
	info := map[string]any{"mode": cmd.Mode}
	turn := userTurnCount(turns)

	enable := cmd.Mode == websearch.ModeOn
	if err := setWebRecall(root, s, session, enable); err != nil {
		info["profile_error"] = err.Error()
		obs.Warn("web_search: recall switch failed", map[string]any{"session": session, "error": err.Error()})
	}
	info["collection"] = webSearchCollection
	info["recall"] = enable

	if !enable {
		// Turning the web off takes the raw material with it: "off" means
		// the character is done with the web for now.
		webSearchState.Clear(session)
		recordWebSearch(st, session, cmd, "")
		return info
	}

	if strings.TrimSpace(cmd.Query) == "" {
		recordWebSearch(st, session, cmd, "")
		return info
	}

	text, err := webSearchMCP(ctx, s, cmd.Query)
	if err != nil {
		info["error"] = err.Error()
		obs.Warn("web_search failed", map[string]any{"session": session, "query": cmd.Query, "error": err.Error()})
	} else {
		webSearchState.Put(session, text, turn)
		info["chars"] = utf8.RuneCountInString(text)
	}
	recordWebSearch(st, session, cmd, cmd.Query)
	return info
}

// recordWebSearch writes the visible record of the command into the chat.
//
// The row's role is not one of user/assistant/system, so it is left out of
// the prompt: it tells the human what was searched, without telling the
// model anything it did not already receive through the module.
func recordWebSearch(st *store.Store, session string, cmd websearch.Command, query string) {
	if st == nil {
		return
	}
	text := webSearchRecordText(cmd, query)
	if _, err := st.AppendChat(session, webSearchRole, text); err != nil {
		obs.Warn("web_search: record failed", map[string]any{"session": session, "error": err.Error()})
	}
}

// webSearchRecordText renders the one-line record of a command.
func webSearchRecordText(cmd websearch.Command, query string) string {
	if cmd.Mode == websearch.ModeOff {
		return fmt.Sprintf("🔕 web_search off · %s removed from recall", webSearchCollection)
	}
	if strings.TrimSpace(query) == "" {
		return fmt.Sprintf("🔍 web_search on · %s added to recall", webSearchCollection)
	}
	return fmt.Sprintf("🔍 web_search on · %q", query)
}

// webSearchContent returns the raw search material that is still in scope for
// this turn, given the module's keep_turns setting.
func webSearchContent(session string, turn, keepTurns int) string {
	texts := webSearchState.Get(session, turn, keepTurns)
	if len(texts) == 0 {
		return ""
	}
	return strings.Join(texts, "\n\n")
}

// seedMissingKinds adds compiled-in modules that a stored preset predates.
//
// Only the note kind can be deleted by hand, so a missing compiled kind is
// always an older preset rather than a deliberate removal. The new module
// takes its place in the prompt from its Order field, not from where it
// lands in the slice.
func seedMissingKinds(ms, defaults []engine.Module) []engine.Module {
	have := make(map[string]bool, len(ms))
	for _, m := range ms {
		have[m.Kind] = true
	}
	for _, d := range defaults {
		if have[d.Kind] {
			continue
		}
		ms = append(ms, d)
	}
	return ms
}
