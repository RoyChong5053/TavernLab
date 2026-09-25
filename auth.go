package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Auth is the optional login gate (one-api style, same design as
// rag-mcp-server/webui/auth.go): empty username = disabled, everything stays
// open. When enabled, every /api/* (except health/login/logout/me), /v1/* and
// /chars/* requires a Bearer session token, a ?token= query fallback (for
// EventSource and <img>, which can't send headers), or the static app token
// (for the Flutter app, which has no login UI).
type Auth struct {
	enabled     bool
	user        string
	passHash    string // hex(sha256(password)), lowercased
	sessionDays int
	appToken    string
	sess        *SessionManager
}

// sessionFile reshapes the persisted table so a credential change invalidates
// old tokens: {"fp": fingerprint, "tokens": {token: expiry}}.
type sessionFile struct {
	FP     string            `json:"fp"`
	Tokens map[string]string `json:"tokens"`
}

// fingerprint binds sessions to the current credentials.
func authFingerprint(user, passHash, appToken string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(user) + "\n" + strings.ToLower(passHash) + "\n" + appToken))
	return hex.EncodeToString(sum[:])
}

// isHex64 reports whether s looks like hex(sha256(...)).
func isHex64(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// NewAuth builds the gate. Credentials come server-side only (flags/env or
// data/settings.json); the WebUI can never set them.
func NewAuth(dataRoot, user, passHash string, sessionDays int, appToken string) *Auth {
	user = strings.TrimSpace(user)
	passHash = strings.TrimSpace(strings.ToLower(passHash))
	if sessionDays <= 0 {
		sessionDays = 30
	}
	a := &Auth{user: user, passHash: passHash, sessionDays: sessionDays, appToken: appToken}
	a.enabled = user != "" && isHex64(passHash)
	fp := authFingerprint(user, passHash, appToken)
	a.sess = NewSessionManager(filepath.Join(dataRoot, "sessions.json"), fp)
	return a
}

func (a *Auth) Enabled() bool { return a.enabled }
func (a *Auth) User() string  { return a.user }

// validAppToken compares in constant time (via sha256 so lengths match).
func (a *Auth) validAppToken(tok string) bool {
	if a.appToken == "" || tok == "" {
		return false
	}
	x := sha256.Sum256([]byte(tok))
	y := sha256.Sum256([]byte(a.appToken))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// TokenFromRequest extracts Bearer <token> or the ?token= fallback.
func TokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			if t := strings.TrimSpace(parts[1]); t != "" {
				return t
			}
		}
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// Check reports whether r carries a valid credential.
func (a *Auth) Check(r *http.Request) bool {
	if !a.enabled {
		return true
	}
	tok := TokenFromRequest(r)
	if tok == "" {
		return false
	}
	return a.sess.Valid(tok) || a.validAppToken(tok)
}

// VerifyPassword compares hex(sha256(password)) in constant time.
func (a *Auth) VerifyPassword(password string) bool {
	if !a.enabled {
		return false
	}
	sum := sha256.Sum256([]byte(password))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.passHash)) == 1
}

// SessionManager issues opaque tokens persisted across restarts.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	path     string
	fp       string
}

// NewSessionManager loads persisted sessions; a fingerprint mismatch (password
// or app token changed) drops every old token.
func NewSessionManager(path, fp string) *SessionManager {
	m := &SessionManager{sessions: map[string]time.Time{}, path: path, fp: fp}
	if path == "" {
		return m
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var raw sessionFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return m
	}
	if raw.FP != fp {
		return m // credentials rotated: force every client to log in again
	}
	now := time.Now()
	for tok, expStr := range raw.Tokens {
		exp, err := time.Parse(time.RFC3339, expStr)
		if err != nil || !exp.After(now) {
			continue
		}
		m.sessions[tok] = exp
	}
	return m
}

// Issue creates a token valid for ttl and persists the table.
func (m *SessionManager) Issue(ttl time.Duration) (token string, exp time.Time) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		sum := sha256.Sum256([]byte(time.Now().UTC().String()))
		copy(b[:], sum[:])
	}
	token = hex.EncodeToString(b[:])
	exp = time.Now().Add(ttl).UTC()
	m.mu.Lock()
	m.sessions[token] = exp
	m.persistLocked()
	m.mu.Unlock()
	return token, exp
}

// Valid reports whether token exists and hasn't expired.
func (m *SessionManager) Valid(token string) bool {
	if token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(m.sessions, token)
		m.persistLocked()
		return false
	}
	return true
}

// Revoke drops one token (logout).
func (m *SessionManager) Revoke(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[token]; !ok {
		return false
	}
	delete(m.sessions, token)
	m.persistLocked()
	return true
}

func (m *SessionManager) persistLocked() {
	if m.path == "" {
		return
	}
	raw := sessionFile{FP: m.fp, Tokens: map[string]string{}}
	now := time.Now()
	for tok, exp := range m.sessions {
		if exp.After(now) {
			raw.Tokens[tok] = exp.Format(time.RFC3339)
		}
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, m.path)
}
