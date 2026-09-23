// Package distill implements the Distilled Memory feature (ported from the
// SillyTavern "Distilled-Memory" extension): recent chat is periodically
// distilled by the LLM into a compact, dated personal fact sheet that is
// injected into the prompt as the `distilled` block.
//
// Storage is per-character and file-based so data/ stays rclone-friendly:
//
//	characters/<name>/distilled.md        the current fact sheet
//	characters/<name>/distilled.meta.json last-run bookkeeping
package distill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/store"
)

// DefaultPrompt is the Memory Custodian prompt. {{user}} / {{isodate}} /
// {{weekday}} / {{time}} are filled at run time; {{maxchars}} is the output
// character budget.
const DefaultPrompt = `You are {{user}}'s automatic diary. Maintain a factual, dated record of what {{user}} explicitly states or does, in two sections: [USER STATE] (current snapshot by date) and [LOG] (timestamped event entries). This record is later used for long-term memory, so accuracy and date structure matter.

## Input
- [Previous Fact Sheet]: existing record. Treat it as the base state.
- [New Messages]: recent conversation.
- LIVE TIMESTAMP: {{isodate}} ({{weekday}}) {{time}} UTC+8 — current date/time.

## Core principles
- Incremental update: preserve valid existing entries unless explicitly corrected. Do not rebuild from scratch.
- Only record facts explicitly stated by {{user}}. Ignore assistant/character guesses, roleplay, hypotheticals, filler, and weak confirmations.
- Do not invent or infer anything: no causes, plans, preferences, emotions unless {{user}} stated them.
- Distinguish certainty: "I don't use X anymore" = state changed; "I used to" = historical; "I might" = intention, not fact.
- If a conflict arises: {{user}}'s explicit correction > newer explicit statement about the same fact > preserve old fact.

## [USER STATE]
- Snapshot of current status (mood, location, active project, health, current activity) true for a given day.
- Format: each line starts with [DD-MM-YYYY] and no time.
- Update in place for the same day; add a new line when a new day begins, preserving previous days.

## [LOG]
- Detailed chronological event log, the diary part.
- Format: every entry starts with full date+time: [DD-MM-YYYY HH:MM] <event in your own words>.
- Use the message timestamp when known; otherwise infer from LIVE TIMESTAMP.
- Cumulative: append new entries, never replace earlier ones. One line per distinct event. Do not invent causation.
- Correct a past entry only if {{user}} explicitly corrects it.

## Trimming and retention
- Strict limit: the whole output must be at most {{maxchars}} characters.
- If over the limit, delete the entire oldest day from both [LOG] and [USER STATE] and repeat until under the limit. Never cut a day in the middle.
- Always preserve at least the current day (trim oldest events within it if unavoidable).

## Output
Output ONLY the updated fact sheet in this exact format:

[USER STATE]
[DD-MM-YYYY] <state...>

[LOG]
[DD-MM-YYYY HH:MM] <event...>

Omit empty sections. No explanation, no commentary.`

// Meta is distillation bookkeeping for one character.
type Meta struct {
	LastRun    string `json:"last_run,omitempty"`
	LastIndex  int    `json:"last_index"` // messages already distilled
	UserTurns  int    `json:"user_turns"` // user turns since last run
	Runs       int    `json:"runs"`
	LastStatus string `json:"last_status,omitempty"`
}

func dir(root, char string) string {
	return filepath.Join(root, "characters", store.CleanSession(char))
}
func sheetPath(root, char string) string { return filepath.Join(dir(root, char), "distilled.md") }
func metaPath(root, char string) string  { return filepath.Join(dir(root, char), "distilled.meta.json") }

// Load returns the current fact sheet ("" when none yet).
func Load(root, char string) string {
	b, err := os.ReadFile(sheetPath(root, char))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Save writes the fact sheet.
func Save(root, char, sheet string) error {
	if err := os.MkdirAll(dir(root, char), 0o755); err != nil {
		return err
	}
	return os.WriteFile(sheetPath(root, char), []byte(strings.TrimSpace(sheet)+"\n"), 0o644)
}

// LoadMeta reads bookkeeping (zero value when absent).
func LoadMeta(root, char string) Meta {
	var m Meta
	b, err := os.ReadFile(metaPath(root, char))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// SaveMeta writes bookkeeping.
func SaveMeta(root, char string, m Meta) error {
	if err := os.MkdirAll(dir(root, char), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(metaPath(root, char), b, 0o644)
}

// FormatTimeline renders messages in the extension's raw shape:
// `**Speaker** [YYYY-MM-DD HH:MM]: text`.
func FormatTimeline(char, userName string, msgs []store.ChatMessage) string {
	if userName == "" {
		userName = "user"
	}
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		speaker := userName
		if m.Role == "assistant" {
			speaker = char
		}
		ts := m.Time
		if t, err := time.Parse(time.RFC3339, m.Time); err == nil {
			ts = t.Format("2006-01-02 15:04")
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		sb.WriteString("**" + speaker + "** [" + ts + "]: " + text + "\n")
	}
	return strings.TrimSpace(sb.String())
}

// BuildUser turns the prompt template + sheets into the messages sent upstream.
// macros are already applied to prompt by the caller.
func BuildUser(prev, timeline string) string {
	var sb strings.Builder
	sb.WriteString("[Previous Fact Sheet]\n")
	if strings.TrimSpace(prev) == "" {
		sb.WriteString("(empty — this is the first distillation)\n")
	} else {
		sb.WriteString(prev + "\n")
	}
	sb.WriteString("\n[New Messages]\n")
	sb.WriteString(timeline)
	sb.WriteString("\n")
	return sb.String()
}
