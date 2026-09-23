// Package store persists chats and audits as plain files so the whole
// data/ dir stays rclone-friendly (no sqlite native modules, no AVX).
//
// Layout invariant: every character is a self-contained package under
// data/characters/<name>/ holding card.json + avatar.* + chat.jsonl +
// archive/ + media/. session == character name; switching a character only
// changes which package is read, it never deletes or overwrites chat data.
package store

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChatMessage is one JSONL row. Images holds media paths relative to the
// character package ("media/<id>.<ext>"), never absolute so data/ stays
// portable. ID is stable for client-side de-duplication (SSE/ntfy).
type ChatMessage struct {
	ID     string   `json:"id,omitempty"`
	Role   string   `json:"role"`
	Text   string   `json:"text"`
	Images []string `json:"images,omitempty"`
	Time   string   `json:"time"`
}

// Audit records one generation: the "why did the model say that" answer.
type Audit struct {
	ID        string         `json:"id"`
	Time      string         `json:"time"`
	Model     string         `json:"model"`
	Tier      int            `json:"tier,omitempty"`     // chosen budget window
	Overflow  bool           `json:"overflow,omitempty"` // assembled prompt exceeded largest tier
	Budget    int            `json:"budget_tokens"`
	TotalTok  int            `json:"total_tokens"`
	Estimate  int            `json:"estimate_tokens,omitempty"` // heuristic pre-call estimate
	Actual    int            `json:"actual_tokens,omitempty"`   // upstream usage.prompt_tokens
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

// CleanSession maps a character name to a safe directory/file stem.
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

// SafeCharName validates a user-supplied character name for creation.
// Returns the cleaned stem and false when the name is unusable or would
// collide after sanitizing with a different raw name.
func SafeCharName(name string) (string, bool) {
	raw := strings.TrimSpace(name)
	if raw == "" || raw == "." || raw == ".." {
		return "", false
	}
	clean := CleanSession(raw)
	if clean != raw {
		return "", false // reject path-ish names rather than silently remap
	}
	return clean, true
}

// New creates a store rooted at data/.
func New(root string) *Store { return &Store{Root: root} }

// CharDir returns the self-contained package dir for a character.
func (s *Store) CharDir(session string) string {
	return filepath.Join(s.Root, "characters", CleanSession(session))
}

func (s *Store) chatPath(session string) string {
	return filepath.Join(s.CharDir(session), "chat.jsonl")
}

// NewID returns a short random id for messages/media (no external deps).
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return time.Now().Format("20060102-150405.000000")
	}
	return hex.EncodeToString(b)
}

// AppendChat appends one message and returns the stored row (with id/time)
// so callers can broadcast it to SSE/ntfy subscribers. images are media
// paths relative to the character package.
func (s *Store) AppendChat(session, role, text string, images ...string) (ChatMessage, error) {
	session = CleanSession(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.chatPath(session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return ChatMessage{}, err
	}
	msg := ChatMessage{
		ID:     NewID(),
		Role:   role,
		Text:   text,
		Images: images,
		Time:   time.Now().Format(time.RFC3339),
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return ChatMessage{}, err
	}
	defer f.Close()
	b, _ := json.Marshal(msg)
	if _, err := f.Write(append(b, '\n')); err != nil {
		return ChatMessage{}, err
	}
	return msg, nil
}

// SaveMedia writes an image under the character package media/ dir and
// returns its package-relative path ("media/<id>.<ext>").
func (s *Store) SaveMedia(session, ext string, data []byte) (string, error) {
	session = CleanSession(session)
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	if ext == "" {
		ext = "png"
	}
	dir := filepath.Join(s.CharDir(session), "media")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := NewID() + "." + ext
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", err
	}
	return "media/" + name, nil
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
// <角色>/chat.jsonl to <角色>/archive/<ts>.jsonl and starts fresh.
// Returns the archive filename ("" when there was nothing to archive).
func (s *Store) Archive(session string) (string, error) {
	session = CleanSession(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.chatPath(session)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return "", nil
	}
	dir := filepath.Join(s.CharDir(session), "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := time.Now().Format("20060102-150405") + ".jsonl"
	if err := os.Rename(src, filepath.Join(dir, name)); err != nil {
		return "", err
	}
	return name, nil
}

// ListArchives returns archive filenames (newest first) for a character.
func (s *Store) ListArchives(session string) ([]string, error) {
	dir := filepath.Join(s.CharDir(session), "archive")
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// LoadArchive reads one archived floor (name from ListArchives).
func (s *Store) LoadArchive(session, name string) ([]ChatMessage, error) {
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		return nil, os.ErrNotExist
	}
	return loadJSONL(filepath.Join(s.CharDir(session), "archive", name))
}

// loadJSONL reads messages from an explicit jsonl path.
func loadJSONL(p string) ([]ChatMessage, error) {
	f, err := os.Open(p)
	if err != nil {
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
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		out = append(out, m)
	}
	return out, sc.Err()
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

var archiveStamp = regexp.MustCompile(`-\d{8}-\d{6}\.jsonl$`)

// MigrateLegacyChats moves the pre-self-contained data/chats/<name>.jsonl
// (and data/chats/archive/*.jsonl) into each character package. It is
// idempotent and append-safe: existing target content is preserved.
func (s *Store) MigrateLegacyChats() {
	legacy := filepath.Join(s.Root, "chats")
	es, err := os.ReadDir(legacy)
	if err != nil {
		return
	}
	for _, e := range es {
		if e.IsDir() {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".jsonl")
		if name == e.Name() || name == "" {
			continue
		}
		s.moveLegacyChat(filepath.Join(legacy, e.Name()), name)
	}
	// legacy archives
	adir := filepath.Join(legacy, "archive")
	if aes, err := os.ReadDir(adir); err == nil {
		for _, e := range aes {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			char := archiveStamp.ReplaceAllString(e.Name(), "")
			if char == "" {
				continue
			}
			dstDir := filepath.Join(s.CharDir(char), "archive")
			_ = os.MkdirAll(dstDir, 0o755)
			dst := filepath.Join(dstDir, e.Name())
			if _, err := os.Stat(dst); err == nil {
				continue
			}
			_ = os.Rename(filepath.Join(adir, e.Name()), dst)
		}
	}
}

func (s *Store) moveLegacyChat(src, session string) {
	base := s.CharDir(session)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return
	}
	dst := filepath.Join(base, "chat.jsonl")
	sb, err := os.ReadFile(src)
	if err != nil {
		return
	}
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		_ = os.WriteFile(dst, sb, 0o644)
	} else {
		// append with newline separation, preserving both histories
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		if len(sb) > 0 && sb[len(sb)-1] != '\n' {
			sb = append(sb, '\n')
		}
		_, _ = f.Write(sb)
		_ = f.Close()
	}
	_ = os.Remove(src)
}
