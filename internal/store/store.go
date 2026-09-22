// Package store persists chats and audits as plain files so the whole
// data/ dir stays rclone-friendly (no sqlite native modules, no AVX).
package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
type Store struct {
	Root string
	mu   sync.Mutex // serializes append/archive against each other
}

// CleanSession maps a chat/session name to a safe filename stem.
// Session is fixed to the character name (no user-facing session picker);
// path separators and ".." are neutralized, empty falls back to "main".
func CleanSession(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", "_")
	}
	if name == "" || name == "." {
		name = "main"
	}
	return name
}

// New creates a store rooted at data/.
func New(root string) *Store { return &Store{Root: root} }

func (s *Store) chatPath(session string) string {
	return filepath.Join(s.Root, "chats", session+".jsonl")
}

// AppendChat appends one message.
func (s *Store) AppendChat(session, role, text string) error {
	session = CleanSession(session)
	s.mu.Lock()
	defer s.mu.Unlock()
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

// LoadAll reads every message oldest-first. Corrupt lines are skipped so one
// bad row never kills the whole history.
func (s *Store) LoadAll(session string) ([]ChatMessage, error) {
	session = CleanSession(session)
	f, err := os.Open(s.chatPath(session))
	if err != nil {
		if os.IsNotExist(err) {
			return []ChatMessage{}, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []ChatMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m ChatMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, sc.Err()
}

// Tail returns the total message count plus a window: the last `limit`
// messages, skipping `before` messages from the end (for "load earlier").
// limit<=0 means all.
func (s *Store) Tail(session string, limit, before int) (msgs []ChatMessage, total int, err error) {
	all, err := s.LoadAll(session)
	if err != nil {
		return nil, 0, err
	}
	total = len(all)
	if before < 0 {
		before = 0
	}
	if before >= total {
		return []ChatMessage{}, total, nil
	}
	end := total - before
	start := 0
	if limit > 0 && end-limit > 0 {
		start = end - limit
	}
	return all[start:end], total, nil
}

// Archive seals the current chat ("new chat" in ST terms): moves
// chats/<session>.jsonl to chats/archive/<session>-<ts>.jsonl and starts fresh.
// Returns the archive filename ("" when there was nothing to archive).
func (s *Store) Archive(session string) (string, error) {
	session = CleanSession(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.chatPath(session)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return "", nil
	}
	dir := filepath.Join(s.Root, "chats", "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := session + "-" + time.Now().Format("20060102-150405") + ".jsonl"
	if err := os.Rename(src, filepath.Join(dir, name)); err != nil {
		return "", err
	}
	return name, nil
}
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
