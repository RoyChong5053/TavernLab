// Package store persists chats and audits as plain files so the whole
// data/ dir stays rclone-friendly (no sqlite native modules, no AVX).
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ChatMessage is one JSONL row.
type ChatMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Time string `json:"time"`
}

// Audit records one generation: the "why did the model say that" answer.
type Audit struct {
	ID        string         `json:"id"`
	Time      string         `json:"time"`
	Model     string         `json:"model"`
	Budget    int            `json:"budget_tokens"`
	TotalTok  int            `json:"total_tokens"`
	Blocks    []BlockRow     `json:"blocks"`
	Dropped   []string       `json:"dropped"`
	Raw       map[string]any `json:"raw_request"`
	ReplyText string         `json:"reply_text,omitempty"`
	Upstream  map[string]any `json:"upstream_usage,omitempty"`
	Memory    map[string]any `json:"memory,omitempty"` // mcp recall: query/collection/hits/error
}

// BlockRow mirrors engine.BlockUsage for storage.
type BlockRow struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Order  int    `json:"order"`
	Tokens int    `json:"tokens"`
	Cut    bool   `json:"truncated"`
	Note   string `json:"dropped_note,omitempty"`
}

// Store is a data-root wrapper.
type Store struct{ Root string }

// New creates a store rooted at data/.
func New(root string) *Store { return &Store{Root: root} }

func (s *Store) chatPath(session string) string {
	return filepath.Join(s.Root, "chats", session+".jsonl")
}

// AppendChat appends one message.
func (s *Store) AppendChat(session, role, text string) error {
	p := s.chatPath(session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(ChatMessage{Role: role, Text: text, Time: time.Now().Format(time.RFC3339)})
	_, err = f.Write(append(b, '\n'))
	return err
}

// SaveAudit writes data/audit/<id>.json.
func (s *Store) SaveAudit(a Audit) error {
	dir := filepath.Join(s.Root, "audit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if a.Time == "" {
		a.Time = time.Now().Format(time.RFC3339)
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	return os.WriteFile(filepath.Join(dir, a.ID+".json"), b, 0o644)
}

// ListAudits returns newest-first ids (cap 200).
func (s *Store) ListAudits() ([]string, error) {
	dir := filepath.Join(s.Root, "audit")
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var ids []string
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".json") {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if len(ids) > 200 {
		ids = ids[:200]
	}
	return ids, nil
}

// LoadAudit reads one audit.
func (s *Store) LoadAudit(id string) (Audit, error) {
	var a Audit
	b, err := os.ReadFile(filepath.Join(s.Root, "audit", id+".json"))
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(b, &a)
	return a, err
}
