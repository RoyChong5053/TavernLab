package engine

// DefaultBlocks returns the starter set.
//
// Order decides position only. There is no priority: the budget engine
// protects recent chat by filling it newest-first, caps each block with
// Budget.Max, and trims elastic source types (mcp/vectra/distilled) only
// when the whole prompt cannot fit the largest tier.
//
// Time macros ({{isodate}} {{weekday}} {{time}} …) and {{character_card}}
// are filled at assemble time by the backend (code-level injection, no
// SillyTavern-style user macro layer).
func DefaultBlocks() []Block {
	return []Block{
		{
			ID: "system", Role: "system", Order: 0, Enabled: true,
			Budget:   Budget{Max: 2000},
			Source:   Source{Type: "static"},
			Template: "You are a helpful roleplay partner. Follow the character card and user instructions.",
		},
		{
			ID: "time_anchor", Role: "system", Order: 5, Enabled: true,
			Budget:   Budget{Max: 300},
			Source:   Source{Type: "static"},
			Template: "[LIVE {{isodate}} {{weekday}} {{time}}] Current time context. Today is {{isodate}}.",
		},
		{
			ID: "character", Role: "system", Order: 10, Enabled: true,
			Budget:   Budget{Max: 3000},
			Source:   Source{Type: "character"},
			Template: "{{character_card}}",
		},
		{
			ID: "distilled", Role: "system", Order: 40, Enabled: true,
			Budget:   Budget{Max: 2000},
			Source:   Source{Type: "distilled", Collection: "default"},
			Template: "<User State(Distilled Memory)>\n{{distilled}}\n</User State>",
		},
		{
			ID: "rag_vectra", Role: "system", Order: 50, Enabled: true,
			Budget:   Budget{Max: 5000},
			Source:   Source{Type: "vectra", Collection: "default"},
			Template: "<RAG_retrieved_context>\n{{rag}}\n</RAG_retrieved_context>",
		},
		{
			ID: "rag_mcp", Role: "system", Order: 55, Enabled: false,
			Budget:   Budget{Max: 6000},
			Source:   Source{Type: "mcp", Collection: "personal_memory"},
			Template: "<RAG_qdrant>\n{{rag}}\n</RAG_qdrant>",
		},
		{
			ID: "chat", Role: "user", Order: 100, Enabled: true,
			Source: Source{Type: "chat"},
		},
	}
}
