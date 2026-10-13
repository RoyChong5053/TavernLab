package main

import (
	"regexp"
	"strconv"
	"strings"
)

// memoryHit is one recalled chunk, parsed out of the rag-mcp-server search
// payload for the Memory page's card view. The payload is the exact text that
// gets injected into the prompt, so these cards are a rendering of what the
// model actually receives — not a second, divergent data source. Chunk bodies
// are passed through untouched: whatever chunking produced is what shows.
type memoryHit struct {
	Score      float64  `json:"score"`
	ID         string   `json:"id,omitempty"`
	Hash       string   `json:"hash,omitempty"`
	Collection string   `json:"collection,omitempty"`
	Backend    string   `json:"backend,omitempty"`
	Source     string   `json:"source,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Text       string   `json:"text"`
}

// resultHeaderRe matches formatSearchResults' per-hit header line, e.g.
// "--- Result 3 (score: 0.642) ---".
var resultHeaderRe = regexp.MustCompile(`^--- Result \d+ \(score: ([0-9.eE+-]+)\) ---$`)

// parseMemoryHits walks the payload produced by rag-mcp-server's
// formatSearchResults ("Found N results:" then one "--- Result i ---" block per
// hit with optional ID/Hash/Collection/Source/Tags lines followed by the raw
// chunk text). Anything that does not match yields nil so the caller can fall
// back to showing the raw text — we never invent structure.
func parseMemoryHits(text string) []memoryHit {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Found ") {
		return nil
	}
	var hits []memoryHit
	var cur *memoryHit
	var body []string

	flush := func() {
		if cur == nil {
			return
		}
		// Only the "\n\n" that formatSearchResults appends after each chunk is
		// stripped; the body is otherwise text-identical to what is injected.
		cur.Text = strings.TrimRight(strings.Join(body, "\n"), "\n")
		hits = append(hits, *cur)
		cur, body = nil, nil
	}

	for _, ln := range lines[1:] {
		if m := resultHeaderRe.FindStringSubmatch(ln); m != nil {
			flush()
			score, _ := strconv.ParseFloat(m[1], 64)
			cur = &memoryHit{Score: score}
			continue
		}
		if cur == nil {
			continue // the "Found N results:" line and blank separators
		}
		// The header block ends at the first non-header line; everything from
		// there on is the raw chunk text (never re-interpreted as headers).
		if len(body) == 0 {
			switch {
			case strings.HasPrefix(ln, "ID: "):
				cur.ID = strings.TrimPrefix(ln, "ID: ")
				continue
			case strings.HasPrefix(ln, "Hash: "):
				cur.Hash = strings.TrimPrefix(ln, "Hash: ")
				continue
			case strings.HasPrefix(ln, "Collection: "):
				cur.Collection, cur.Backend = splitCollectionBackend(strings.TrimPrefix(ln, "Collection: "))
				continue
			case strings.HasPrefix(ln, "Source: "):
				cur.Source = strings.TrimPrefix(ln, "Source: ")
				continue
			case strings.HasPrefix(ln, "Tags: "):
				cur.Tags = splitTagList(strings.TrimPrefix(ln, "Tags: "))
				continue
			}
		}
		body = append(body, ln)
	}
	flush()
	return hits
}

// splitCollectionBackend splits "global_memory [vectra]" into its name and
// backend. A name without the bracketed suffix leaves backend empty.
func splitCollectionBackend(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, " ["); i >= 0 && strings.HasSuffix(s, "]") {
		return strings.TrimSpace(s[:i]), s[i+2 : len(s)-1]
	}
	return s, ""
}

// splitTagList turns "a, b, c" into ["a","b","c"].
func splitTagList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}
