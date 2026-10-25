package distill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RunAudit is one distillation attempt, stored next to the chat audits in
// data/audit/<id>-distill.json. The chat audit answers "why did the model say
// that"; this one answers "what did the extractor actually receive, and what
// came back". The request body is kept verbatim so the file itself is the raw
// record (nano-friendly), while the UI renders it as readable text.
//
// The "-distill" suffix keeps both kinds in one folder (one rclone backup) but
// lets ListAudits hide them from the chat dropdown.
type RunAudit struct {
	ID        string `json:"id"`
	Time      string `json:"time"`
	Session   string `json:"session"`
	Force     bool   `json:"force"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`

	// Request is the exact JSON body sent upstream (model/stream/max_tokens/
	// messages). The UI extracts the system and user messages from it.
	Request json.RawMessage `json:"request"`

	// Which slice of chat.jsonl this run captured. FreshFrom is the cursor
	// before the run, FreshTo is len(all) after it.
	FreshFrom int `json:"fresh_from"`
	FreshTo   int `json:"fresh_to"`
	FreshMsgs int `json:"fresh_msgs"`

	// Rune counts of the input sections, so a huge capture is obvious at a
	// glance without opening the request.
	StateChars     int `json:"state_chars"`
	RecentLogChars int `json:"recent_log_chars"`
	MovementChars  int `json:"movement_chars"`
	TimelineChars  int `json:"timeline_chars"`

	Attempts []Attempt `json:"attempts"`

	// Outcome is ok | empty-delta | parse-failed | http-error.
	Outcome   string `json:"outcome"`
	Error     string `json:"error,omitempty"`
	Delta     string `json:"delta,omitempty"` // accepted delta (ok runs only)
	ElapsedMS int64  `json:"elapsed_ms"`
}

// Attempt is one upstream call. The extractor retries once when the reply is
// empty/truncated, so a failed run has two entries with the same request.
type Attempt struct {
	N              int    `json:"n"`
	Finish         string `json:"finish_reason"`
	Completion     int    `json:"completion_tokens"`
	ReasoningChars int    `json:"reasoning_chars"`
	Reasoning      string `json:"reasoning,omitempty"` // capped, for debugging
	DeltaChars     int    `json:"delta_chars"`
}

func auditDir(root string) string { return filepath.Join(root, "audit") }

// cleanRunID accepts "20261026-063858.661", with or without the -distill
// suffix / .json extension, and rejects anything that could escape the folder.
func cleanRunID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimSuffix(id, ".json")
	id = strings.TrimSuffix(id, "-distill")
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return ""
	}
	return id
}

// SaveRunAudit writes (or overwrites) one run. Called before the upstream call
// and again after every attempt, so a timeout or crash still leaves the request.
func SaveRunAudit(root string, a RunAudit) error {
	id := cleanRunID(a.ID)
	if id == "" {
		return fmt.Errorf("invalid distill audit id")
	}
	a.ID = id
	if a.Time == "" {
		a.Time = time.Now().Format(time.RFC3339)
	}
	if err := os.MkdirAll(auditDir(root), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(auditDir(root), id+"-distill.json"), b, 0o644)
}

// ListRunAudits returns distill run ids, newest first. A missing/empty folder is
// not an error: after an rclone backup moves everything away the UI should show
// an empty list, not a failure.
func ListRunAudits(root string) ([]string, error) {
	es, err := os.ReadDir(auditDir(root))
	if err != nil {
		return []string{}, nil
	}
	ids := make([]string, 0, len(es))
	for _, e := range es {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, "-distill.json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(n, "-distill.json"))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if len(ids) > 200 {
		ids = ids[:200]
	}
	return ids, nil
}

// LoadRunAudit reads one run by id.
func LoadRunAudit(root, id string) (RunAudit, error) {
	var a RunAudit
	id = cleanRunID(id)
	if id == "" {
		return a, fmt.Errorf("invalid distill audit id")
	}
	b, err := os.ReadFile(filepath.Join(auditDir(root), id+"-distill.json"))
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(b, &a)
	return a, err
}
