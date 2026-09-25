package engine

// DefaultBlocks returns the starter set.
//
// Order decides position only. Each block carries a Level:
//   - L1 locked  : never trimmed (identity + live memory).
//   - L2 trim    : evictable (RAG hits, chat) under one unified policy.
//   - L3 elastic : dropped entirely first (future experimental blocks).
//
// Time macros ({{isodate}} {{weekday}} {{time}} …) and {{character_card}}
// are filled at assemble time by the backend (code-level injection, no
// SillyTavern-style user macro layer).
func DefaultBlocks() []Block {
	return []Block{
		{
			ID: "system", Role: "system", Order: 0, Enabled: true, Level: LevelLocked,
			Source:   Source{Type: "static"},
			Template: "You are a helpful roleplay partner. Follow the character card and user instructions.",
		},
		{
			ID: "time_anchor", Role: "system", Order: 5, Enabled: true, Level: LevelLocked,
			Source:   Source{Type: "static"},
			Template: "[LIVE {{isodate}} {{weekday}} {{time}}] Current time context. Today is {{isodate}}.",
		},
		{
			ID: "character", Role: "system", Order: 10, Enabled: true, Level: LevelLocked,
			Source:   Source{Type: "character"},
			Template: "{{character_card}}",
		},
		{
			ID: "distilled", Role: "system", Order: 40, Enabled: true, Level: LevelLocked,
			Source:   Source{Type: "distilled", Collection: "default"},
			Template: "<User State(Distilled Memory)>\n{{distilled}}\n</User State>",
		},
		{
			ID: "rag_mcp", Role: "system", Order: 55, Enabled: true, Level: LevelTrim,
			Source:   Source{Type: "mcp", Collection: "personal_memory"},
			Template: "<RAG_qdrant>\n{{rag}}\n</RAG_qdrant>",
		},
		{
			ID: "chat", Role: "user", Order: 100, Enabled: true, Level: LevelTrim,
			Source: Source{Type: "chat"},
		},
	}
}
