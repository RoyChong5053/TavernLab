package engine

// Module kinds. These are the compiled-in "tools" the binary understands.
// A module's Kind is the single discriminator for how it is resolved at
// assemble time; there is no user-extensible macro language.
const (
	KindSystem         = "system"
	KindTimeAnchor     = "time_anchor"
	KindCharacter      = "character"
	KindDistilledState = "distilled_state"
	KindDistilledLog   = "distilled_log"
	KindRAG            = "rag"
	KindChat           = "chat"
	KindNote           = "note"
)

// ModuleSpec describes one compiled-in module kind for the Prompts-IDE UI.
// It is the single source of truth: the frontend renders whatever the backend
// reports here instead of hardcoding labels, descriptions or placeholders.
type ModuleSpec struct {
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Role           string   `json:"role"`
	Editable       bool     `json:"editable"`        // exposes a prompt textarea
	Placeholders   []string `json:"placeholders"`    // compiled placeholders (editable kinds)
	ImportsContext bool     `json:"imports_context"` // pulls external data into the prompt
	DefaultLevel   Level    `json:"default_level"`
	DefaultOrder   int      `json:"default_order"`
	DefaultTemplate string  `json:"default_template,omitempty"`
	Singleton      bool     `json:"singleton"` // at most one instance (compiled kinds)
}

// Catalog returns every module kind compiled into this binary, in default
// order. Only `note` is non-singleton and freely addable.
func Catalog() []ModuleSpec {
	return []ModuleSpec{
		{
			Kind: KindSystem, Title: "System Prompt", Role: "system", Editable: true,
			Description:  "Free-text system instruction. Sent as a system message verbatim.",
			DefaultLevel: LevelLocked, DefaultOrder: 0, Singleton: true,
			DefaultTemplate: "You are a helpful roleplay partner. Follow the character card and user instructions.",
		},
		{
			Kind: KindTimeAnchor, Title: "Time & Location", Role: "system", Editable: true,
			Description:  "Live anchor rendered by the binary each turn: date, weekday, day-part, timezone and current location. Format is stable.",
			Placeholders: []string{"{{isodate}}", "{{date}}", "{{time}}", "{{weekday}}", "{{weekday_zh}}", "{{daypart}}", "{{datetime}}", "{{timestamp}}", "{{timezone}}", "{{location}}", "{{user}}"},
			ImportsContext: true,
			DefaultLevel:   LevelLocked, DefaultOrder: 5, Singleton: true,
			DefaultTemplate: "[LIVE] {{isodate}} {{weekday_zh}} {{time}} {{daypart}} · {{location}}",
		},
		{
			Kind: KindCharacter, Title: "Character Card", Role: "system",
			Description:  "The active character's description, injected from its card.json. Edit it on the Characters page.",
			ImportsContext: true,
			DefaultLevel:   LevelLocked, DefaultOrder: 10, Singleton: true,
			DefaultTemplate: "{{character_card}}",
		},
		{
			Kind: KindDistilledState, Title: "User State (Memory)", Role: "system",
			Description:  "One-line-per-day memory snapshot distilled from chat. Always present; managed on the Memory page.",
			ImportsContext: true,
			DefaultLevel:   LevelLocked, DefaultOrder: 40, Singleton: true,
			DefaultTemplate: "<User State(Distilled Memory)>\n{{distilled_state}}\n</User State>",
		},
		{
			Kind: KindDistilledLog, Title: "Memory Log", Role: "system",
			Description:  "The distilled timeline/diary. Under budget pressure the oldest whole days are evicted first. Managed on the Memory page.",
			ImportsContext: true,
			DefaultLevel:   LevelTrim, DefaultOrder: 41, Singleton: true,
			DefaultTemplate: "<Memory Log>\n{{items}}\n</Memory Log>",
		},
		{
			Kind: KindRAG, Title: "Retrieved Context (RAG)", Role: "system",
			Description:  "External knowledge retrieved from the active character's recall collections via MCP. Configure recall on the Memory page.",
			ImportsContext: true,
			DefaultLevel:   LevelTrim, DefaultOrder: 55, Singleton: true,
			DefaultTemplate: "<RAG_qdrant>\n{{rag}}\n</RAG_qdrant>",
		},
		{
			Kind: KindChat, Title: "Chat History", Role: "user",
			Description:  "The conversation, slid server-side from full history. Window size and minimum kept turns live in Settings.",
			ImportsContext: true,
			DefaultLevel:   LevelTrim, DefaultOrder: 100, Singleton: true,
		},
		{
			Kind: KindNote, Title: "Author Note", Role: "system", Editable: true,
			Description:  "An extra instruction you add ad hoc. Give it a title and prompt; add as many as you need.",
			DefaultLevel:   LevelLocked, DefaultOrder: 20, Singleton: false,
		},
	}
}

// SpecFor returns the catalog entry for a kind, and whether it exists.
func SpecFor(kind string) (ModuleSpec, bool) {
	for _, s := range Catalog() {
		if s.Kind == kind {
			return s, true
		}
	}
	return ModuleSpec{}, false
}
