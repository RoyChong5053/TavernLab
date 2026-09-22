package engine

// DefaultBlocks returns the P0 starter set.
// Order decides position, Priority decides who gets squeezed.
// Time anchor stays LOCKED at order 0 — this is the "time aware" trick:
// mid-prompt timestamps get ignored, top-pinned timestamps don't.
func DefaultBlocks() []Block {
	return []Block{
		{
			ID: "system", Role: "system", Order: 0, Priority: PriorityLocked, Enabled: true,
			Budget:   Budget{Min: 100, Max: 2000},
			Source:   Source{Type: "static"},
			Template: "You are a helpful roleplay partner. Follow the character card and user instructions.",
		},
		{
			ID: "time_anchor", Role: "system", Order: 5, Priority: PriorityLocked, Enabled: true,
			Budget:   Budget{Min: 50, Max: 300},
			Source:   Source{Type: "static"},
			Template: "[LIVE {{isodate}} {{weekday}} {{time}}] Current time context. Today is {{isodate}}.",
		},
		{
			ID: "character", Role: "system", Order: 10, Priority: PriorityHigh, Enabled: true,
			Budget:   Budget{Min: 200, Max: 3000},
			Source:   Source{Type: "static"},
			Template: "{{character_card}}",
		},
		{
			ID: "distilled", Role: "system", Order: 40, Priority: PriorityHigh, Enabled: true,
			Budget:   Budget{Min: 300, Max: 2000},
			Source:   Source{Type: "distilled", Collection: "default"},
			Template: "<User State(Distilled Memory)>\n{{distilled}}\n</User State>",
		},
		{
			ID: "rag_vectra", Role: "system", Order: 50, Priority: PriorityElastic, Enabled: true,
			Budget:   Budget{Min: 0, Max: 5000},
			Source:   Source{Type: "vectra", Collection: "default"},
			Template: "<RAG_retrieved_context>\n{{rag}}\n</RAG_retrieved_context>",
		},
		{
			ID: "rag_mcp", Role: "system", Order: 55, Priority: PriorityElastic, Enabled: false,
			Budget:   Budget{Min: 0, Max: 6000},
			Source:   Source{Type: "mcp", Collection: "personal_memory"},
			Template: "<RAG_qdrant>\n{{rag}}\n</RAG_qdrant>",
		},
		{
			ID: "chat", Role: "user", Order: 100, Priority: PriorityElastic, Enabled: true,
			Budget:   Budget{Min: 500, Max: 0},
			Source:   Source{Type: "chat"},
			Template: "{{chat_history}}",
		},
	}
}
