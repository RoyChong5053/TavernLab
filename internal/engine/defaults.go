package engine

// DefaultModules returns the starter set of compiled-in modules.
//
// Order decides position only. Each module carries a Level:
//   - L1 locked  : never trimmed (identity + live memory).
//   - L2 trim    : evictable (RAG hits, chat) under one unified policy.
//   - L3 elastic : dropped entirely first (experimental modules).
//
// Placeholders ({{isodate}} {{time}} {{character_card}} …) are filled at
// assemble time by the binary; there is no SillyTavern-style user macro layer.
func DefaultModules() []Module {
	return []Module{
		{
			ID: "system", Kind: KindSystem, Role: "system", Order: 0, Enabled: true, Level: LevelLocked,
			Template: "You are a helpful roleplay partner. Follow the character card and user instructions.",
		},
		{
			ID: "time_anchor", Kind: KindTimeAnchor, Role: "system", Order: 5, Enabled: true, Level: LevelLocked,
			Template: "[LIVE] {{isodate}} {{weekday_zh}} {{time}} {{daypart}} · {{location}}",
		},
		{
			ID: "character", Kind: KindCharacter, Role: "system", Order: 10, Enabled: true, Level: LevelLocked,
			Template: "{{character_card}}",
		},
		{
			// Current one-line-per-day snapshot. Small and code-capped, so it
			// stays L1: the identity/state layer is always present.
			ID: "distilled_state", Kind: KindDistilledState, Role: "system", Order: 40, Enabled: true,
			Level: LevelLocked, Collection: "default",
			Template: "<User State(Distilled Memory)>\n{{distilled_state}}\n</User State>",
		},
		{
			// The diary. L2 with the highest evict priority: under budget
			// pressure the oldest whole days are dropped by the engine (never
			// by the LLM) after chat and RAG have given way.
			ID: "distilled_log", Kind: KindDistilledLog, Role: "system", Order: 41, Enabled: true,
			Level: LevelTrim, Collection: "default",
			Template: "<Memory Log>\n{{items}}\n</Memory Log>",
		},
		{
			ID: "rag", Kind: KindRAG, Role: "system", Order: 55, Enabled: true,
			Level: LevelTrim, Collection: "personal_memory",
			Template: "<RAG_qdrant>\n{{rag}}\n</RAG_qdrant>",
		},
		{
			ID: "chat", Kind: KindChat, Role: "user", Order: 100, Enabled: true, Level: LevelTrim,
		},
	}
}
