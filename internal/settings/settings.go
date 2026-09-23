// Package settings persists runtime config under data/settings.json.
//
// The file lives in data/ (rclone-friendly, git-ignored) so API keys never
// touch the repo. Precedence: start flags > env (ONEAPI_KEY) > settings file.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings is the runtime-tunable config. Empty APIKey on PUT means "keep".
type Settings struct {
	Upstream          string  `json:"upstream,omitempty"`
	APIKey            string  `json:"api_key,omitempty"`
	RerankURL         string  `json:"rerank_url,omitempty"`
	MCPURL            string  `json:"mcp_url,omitempty"`
	MCPCollection     string  `json:"mcp_collection,omitempty"`
	MCPEnabled        bool    `json:"mcp_enabled"`
	MCPTopK           int     `json:"mcp_topk,omitempty"`
	MCPTimeout        int     `json:"mcp_timeout,omitempty"`
	MCPThreshold      float64 `json:"mcp_threshold,omitempty"` // <0 = omit (server default)
	CurrentChar       string  `json:"current_char,omitempty"`  // source of truth for web + app
	UserName          string  `json:"user_name,omitempty"`     // export attribution / {{user}}
	NtfyURL           string  `json:"ntfy_url,omitempty"`      // self-hosted ntfy base, empty = disabled
	NtfyTopic         string  `json:"ntfy_topic,omitempty"`
	DistillEnabled    bool    `json:"distill_enabled"`               // auto-distill every N user turns
	DistillInterval   int     `json:"distill_interval,omitempty"`    // user turns between runs (default 8)
	DistillMaxChars   int     `json:"distill_max_chars,omitempty"`   // fact-sheet character budget (default 4000)
	DistillRetainDays int     `json:"distill_retain_days,omitempty"` // always keep at least N days (default 3)
	DistillModel      string  `json:"distill_model,omitempty"`       // empty = main model
	DistillPrompt     string  `json:"distill_prompt,omitempty"`      // empty = built-in default
}

// Defaults for a fresh checkout talking to the home LAN.
func Defaults() Settings {
	return Settings{
		Upstream:          "http://192.168.10.2:3000",
		RerankURL:         "http://127.0.0.1:11437",
		MCPURL:            "http://192.168.10.2:8199",
		MCPCollection:     "",
		MCPEnabled:        false,
		MCPTopK:           10,
		MCPTimeout:        120,
		MCPThreshold:      -1,
		CurrentChar:       "Leer乐儿",
		UserName:          "RoyChong",
		DistillInterval:   8,
		DistillMaxChars:   4000,
		DistillRetainDays: 3,
	}
}

// Load reads data/settings.json; missing file yields Defaults().
func Load(root string) Settings {
	s := Defaults()
	b, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil {
		return s
	}
	var f Settings
	if json.Unmarshal(b, &f) != nil {
		return s
	}
	if f.Upstream != "" {
		s.Upstream = f.Upstream
	}
	if f.APIKey != "" {
		s.APIKey = f.APIKey
	}
	if f.RerankURL != "" {
		s.RerankURL = f.RerankURL
	}
	if f.MCPURL != "" {
		s.MCPURL = f.MCPURL
	}
	if f.MCPCollection != "" || explicitEmpty(b, "mcp_collection") {
		s.MCPCollection = f.MCPCollection
	}
	s.MCPEnabled = f.MCPEnabled
	if f.MCPTopK > 0 {
		s.MCPTopK = f.MCPTopK
	}
	if f.MCPTimeout > 0 {
		s.MCPTimeout = f.MCPTimeout
	}
	if f.MCPThreshold >= 0 {
		s.MCPThreshold = f.MCPThreshold
	}
	if f.CurrentChar != "" {
		s.CurrentChar = f.CurrentChar
	}
	if f.UserName != "" {
		s.UserName = f.UserName
	}
	if f.NtfyURL != "" {
		s.NtfyURL = f.NtfyURL
	}
	if f.NtfyTopic != "" {
		s.NtfyTopic = f.NtfyTopic
	}
	s.DistillEnabled = f.DistillEnabled
	if f.DistillInterval > 0 {
		s.DistillInterval = f.DistillInterval
	}
	if f.DistillMaxChars > 0 {
		s.DistillMaxChars = f.DistillMaxChars
	}
	if f.DistillRetainDays > 0 {
		s.DistillRetainDays = f.DistillRetainDays
	}
	if f.DistillModel != "" {
		s.DistillModel = f.DistillModel
	}
	if f.DistillPrompt != "" {
		s.DistillPrompt = f.DistillPrompt
	}
	return s
}

// explicitEmpty reports whether a JSON key was present (even as "").
func explicitEmpty(raw []byte, key string) bool {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// Save writes data/settings.json (0600: it may hold an API key).
func Save(root string, s Settings) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(root, "settings.json"), b, 0o600)
}

// Mask returns a safe hint like "••••f83A" for UI display.
func Mask(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "••••"
	}
	return "••••" + key[len(key)-4:]
}
