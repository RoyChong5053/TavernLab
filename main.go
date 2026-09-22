// Command tavernlab is the TavernLab Go thin core (Plan v2 P0).
//
//   - Serves embedded vanilla-JS frontend (llama.cpp-style UX, rewritten).
//   - Prompt Engine: blocks -> Context Budget Engine -> raw -> one-api.
//   - Audit-first: every generation saved to data/audit/<id>.json.
//   - Portable: all state under data/ (rclone --copy-links friendly).
package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/expression"
	"github.com/RoyChong5053/TavernLab/internal/mcp"
	"github.com/RoyChong5053/TavernLab/internal/memory"
	"github.com/RoyChong5053/TavernLab/internal/proxy"
	"github.com/RoyChong5053/TavernLab/internal/settings"
	"github.com/RoyChong5053/TavernLab/internal/store"
)

//go:embed web
var webFS embed.FS

type Config struct {
	Port          int
	DataRoot      string
	Upstream      string // one-api base URL (startup snapshot; live value in runtimeSettings)
	APIKey        string
	DefaultBlocks []engine.Block
	Ctx           engine.ContextConfig
}

// runtimeSettings is the live config: edited via PUT /api/settings,
// persisted to data/settings.json (0600, git-ignored — holds the API key).
type runtimeSettings struct {
	mu  sync.RWMutex
	cur settings.Settings
}

func (r *runtimeSettings) get() settings.Settings {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cur
}

func (r *runtimeSettings) setUpstream(u string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur.Upstream = u
}

func (r *runtimeSettings) setAPIKey(k string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur.APIKey = k
}

// update merges a PUT body (empty api_key = keep) and persists.
func (r *runtimeSettings) update(root string, patch map[string]any) settings.Settings {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := patch["upstream"].(string); ok && v != "" {
		r.cur.Upstream = v
	}
	if v, ok := patch["api_key"].(string); ok && v != "" {
		r.cur.APIKey = v
	}
	if v, ok := patch["rerank_url"].(string); ok && v != "" {
		r.cur.RerankURL = v
	}
	if v, ok := patch["mcp_url"].(string); ok && v != "" {
		r.cur.MCPURL = v
	}
	if v, ok := patch["mcp_collection"]; ok {
		if s, ok := v.(string); ok {
			r.cur.MCPCollection = s
		}
	}
	if v, ok := patch["mcp_enabled"].(bool); ok {
		r.cur.MCPEnabled = v
	}
	if v, ok := patch["mcp_topk"].(float64); ok && v > 0 {
		r.cur.MCPTopK = int(v)
	}
	if v, ok := patch["mcp_threshold"].(float64); ok {
		r.cur.MCPThreshold = v
	}
	_ = settings.Save(root, r.cur) // best-effort; key stays usable in memory regardless
	return r.cur
}

func main() {
	port := flag.Int("port", 8080, "listen port")
	data := flag.String("data", "data", "data root (rclone this dir)")
	upstream := flag.String("upstream", "", "one-api base URL (overrides settings file)")
	apiKey := flag.String("apikey", "", "one-api key (overrides env ONEAPI_KEY / settings file)")
	flag.Parse()

	// Runtime settings: flags > env > data/settings.json > built-in defaults.
	file := settings.Load(*data)
	rt := &runtimeSettings{cur: file}
	if *upstream != "" {
		rt.setUpstream(*upstream)
	}
	if *apiKey != "" {
		rt.setAPIKey(*apiKey)
	} else if k := os.Getenv("ONEAPI_KEY"); k != "" {
		rt.setAPIKey(k)
	}

	cfg := Config{
		Port: *port, DataRoot: *data, Upstream: rt.get().Upstream,
		APIKey:        rt.get().APIKey,
		DefaultBlocks: engine.DefaultBlocks(),
		Ctx:           engine.DefaultConfig(),
	}
	st := store.New(cfg.DataRoot)
	client := &http.Client{Timeout: 10 * time.Minute}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		s := rt.get()
		writeJSON(w, map[string]any{"ok": true, "time": time.Now().Format(time.RFC3339), "upstream": s.Upstream, "key_set": s.APIKey != ""})
	})

	// Runtime settings (data/settings.json, 0600, git-ignored).
	// GET masks the key; PUT merges, empty api_key = keep old.
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			s := rt.get()
			writeJSON(w, map[string]any{
				"upstream": s.Upstream, "api_key_set": s.APIKey != "",
				"api_key_hint": settings.Mask(s.APIKey), "rerank_url": s.RerankURL,
				"mcp_url": s.MCPURL, "mcp_collection": s.MCPCollection,
				"mcp_enabled": s.MCPEnabled, "mcp_topk": s.MCPTopK, "mcp_threshold": s.MCPThreshold,
			})
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			var patch map[string]any
			if err := json.Unmarshal(b, &patch); err != nil {
				http.Error(w, "bad settings json", 400)
				return
			}
			s := rt.update(cfg.DataRoot, patch)
			writeJSON(w, map[string]any{"ok": true, "upstream": s.Upstream, "api_key_set": s.APIKey != "", "api_key_hint": settings.Mask(s.APIKey)})
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	// Model list, proxied through the backend so the browser never sees the key.
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		s := rt.get()
		req, err := http.NewRequest("GET", strings.TrimRight(s.Upstream, "/")+"/v1/models", nil)
		if err != nil {
			http.Error(w, "build models request failed", 500)
			return
		}
		if s.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+s.APIKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "upstream unreachable: "+err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(b)
	})

	// Memory test probe: POST {"query":"..."} -> MCP chunks (for the Memory page button).
	mux.HandleFunc("/api/memory/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		var in struct {
			Query string `json:"query"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		s := rt.get()
		hits, err := mcp.New(s.MCPURL).Search(r.Context(), in.Query, s.MCPCollection, s.MCPTopK, s.MCPThreshold)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "collection": firstNonEmpty(s.MCPCollection, "(server default)"), "hits": hits})
	})

	// Blocks CRUD (file-backed): data/presets/default.json
	mux.HandleFunc("/api/blocks", func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(cfg.DataRoot, "presets", "default.json")
		switch r.Method {
		case "GET":
			b, err := os.ReadFile(p)
			if err != nil {
				writeJSON(w, cfg.DefaultBlocks)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			var blocks []engine.Block
			if err := json.Unmarshal(b, &blocks); err != nil {
				http.Error(w, "bad blocks json", 400)
				return
			}
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, b, 0o644)
			writeJSON(w, map[string]any{"ok": true, "n": len(blocks)})
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	loadBlocks := func() []engine.Block {
		p := filepath.Join(cfg.DataRoot, "presets", "default.json")
		b, err := os.ReadFile(p)
		if err != nil {
			return cfg.DefaultBlocks
		}
		var blocks []engine.Block
		if err := json.Unmarshal(b, &blocks); err != nil {
			return cfg.DefaultBlocks
		}
		return blocks
	}

	// Dry-run assemble: blocks -> raw + per-block tokens + dropped. No LLM call.
	// Also resolves mcp blocks so Preview shows exactly what the model would get.
	mux.HandleFunc("/api/assemble", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Blocks  []engine.Block       `json:"blocks"`
			Context engine.ContextConfig `json:"context"`
			Chat    []map[string]string  `json:"chat"`
			Session string               `json:"session"` // when set, full history slides server-side
		}
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			_ = json.Unmarshal(b, &in)
		}
		blocks := in.Blocks
		if len(blocks) == 0 {
			blocks = loadBlocks()
		}
		ctx := in.Context
		if ctx.MaxTokens == 0 {
			ctx = cfg.Ctx
		}
		turns := in.Chat
		if in.Session != "" {
			if all, err := st.LoadAll(in.Session); err == nil {
				turns = chatToTurns(all)
			}
		}
		blocks = injectChat(blocks, turns)
		memInfo := resolveMCP(r.Context(), rt.get(), blocks, turns)
		res := engine.Assemble(blocks, ctx)
		out := map[string]any{
			"messages": res.Messages, "blocks": res.Blocks, "total_tokens": res.TotalTok,
			"budget_tokens": res.BudgetTok, "dropped": res.Dropped, "prompt_text": res.PromptText,
			"memory": memInfo,
		}
		writeJSON(w, out)
	})

	// Chat completions: assemble then forward to one-api, save audit.
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		s := rt.get()
		body, _ := io.ReadAll(r.Body)
		var in struct {
			Model    any                  `json:"model"`
			Messages []map[string]any     `json:"messages"`
			Stream   bool                 `json:"stream"`
			Session  string               `json:"session"`
			Text     string               `json:"text"` // new path: single fresh user message
			Blocks   []engine.Block       `json:"blocks"`
			Context  engine.ContextConfig `json:"context"`
			Chat     []map[string]string  `json:"chat"` // legacy: explicit turns (assemble/preview compat)
		}
		_ = json.Unmarshal(body, &in)
		blocks := in.Blocks
		if len(blocks) == 0 {
			blocks = loadBlocks()
		}
		ctx := in.Context
		if ctx.MaxTokens == 0 {
			ctx = cfg.Ctx
		}
		session := store.CleanSession(firstNonEmpty(in.Session, "main"))
		// New path: frontend sends only the fresh message; full context slides
		// server-side out of the (possibly thousands of turns) JSONL so the DOM
		// window size never affects what the model sees.
		turns := in.Chat
		if userText := strings.TrimSpace(in.Text); userText != "" {
			_ = st.AppendChat(session, "user", userText)
			all, _ := st.LoadAll(session)
			turns = chatToTurns(all)
		}
		blocks = injectChat(blocks, turns)
		// If caller passed raw messages (classic path), wrap as chat block content.
		if len(in.Messages) > 0 {
			blocks = injectRawMessages(blocks, in.Messages)
		}
		memInfo := resolveMCP(r.Context(), s, blocks, turns)
		res := engine.Assemble(blocks, ctx)

		// Build upstream body from assembled messages.
		model := in.Model
		if model == nil || model == "" {
			model = "default"
		}
		upBody, _ := json.Marshal(map[string]any{
			"model": model, "messages": res.Messages, "stream": in.Stream,
		})
		auditID := time.Now().Format("20060102-150405.000")
		if s, ok := model.(string); ok {
			_ = s
		}

		// Legacy path (no Text): user turn arrived inside in.Chat, persist it.
		if strings.TrimSpace(in.Text) == "" {
			if q := lastUserText(in.Chat); q != "" {
				_ = st.AppendChat(session, "user", q)
			}
		}

		if in.Stream {
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", 500)
				return
			}
			status, rebuilt, _ := proxy.Forward(client, s.Upstream, s.APIKey, upBody, true, w, flusher)
			_ = status
			_ = st.SaveAudit(toAudit(auditID, model, res, upBody, rebuilt, nil, memInfo))
			_ = st.AppendChat(session, "assistant", rebuiltText(rebuilt))
			return
		}
		status, respBody, usage := proxy.Forward(client, s.Upstream, s.APIKey, upBody, false, nil, nil)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
		_ = st.SaveAudit(toAudit(auditID, model, res, upBody, respBody, usage, memInfo))
		_ = st.AppendChat(session, "assistant", replyText(respBody))
	})

	// Expression classify: reply -> avatar label (rule + local reranker).
	// POST {"text":"...","rerank_url":"http://127.0.0.1:11437"}
	mux.HandleFunc("/api/expression/classify", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text      string `json:"text"`
			RerankURL string `json:"rerank_url"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		if in.RerankURL == "" {
			in.RerankURL = "http://127.0.0.1:11437"
		}
		writeJSON(w, expression.Classify(client, in.RerankURL, in.Text))
	})

	// Characters: list/detail/avatar(expressions come later with webp).
	// Data layout: data/characters/<name>/avatar.* + expressions/<label>.webp
	mux.HandleFunc("/api/characters", func(w http.ResponseWriter, r *http.Request) {
		root := filepath.Join(cfg.DataRoot, "characters")
		es, _ := os.ReadDir(root)
		var names []string
		for _, e := range es {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		if names == nil {
			names = []string{}
		}
		writeJSON(w, map[string]any{"characters": names})
	})
	mux.HandleFunc("/api/characters/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/characters/")
		parts := strings.SplitN(rest, "/", 2)
		name := parts[0]
		if name == "" || strings.Contains(name, "..") {
			http.Error(w, "bad name", 400)
			return
		}
		base := filepath.Join(cfg.DataRoot, "characters", name)
		if len(parts) == 2 && parts[1] == "avatar" && r.Method == "PUT" {			// multipart file=...  or raw body; save as avatar.webp (browser
			// <img> plays animated webp natively, mp4 loop comes later).
			_ = os.MkdirAll(base, 0o755)
			var src io.Reader = r.Body
			fname := "avatar.webp"
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
				f, h, err := r.FormFile("file")
				if err != nil {
					http.Error(w, "need file field", 400)
					return
				}
				defer f.Close()
				src = f
				if n := strings.ToLower(h.Filename); strings.HasSuffix(n, ".png") {
					fname = "avatar.png"
				} else if strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".jpeg") {
					fname = "avatar.jpg"
				} else if strings.HasSuffix(n, ".gif") {
					fname = "avatar.gif"
				}
			}
			dst, err := os.Create(filepath.Join(base, fname))
			if err != nil {
				http.Error(w, "save failed", 500)
				return
			}
			defer dst.Close()
			if _, err := io.Copy(dst, src); err != nil {
				http.Error(w, "save failed", 500)
				return
			}
			// remove sibling avatar.* so detail has one canonical file
			for _, alt := range []string{"avatar.webp", "avatar.png", "avatar.jpg", "avatar.gif"} {
				if alt != fname {
					_ = os.Remove(filepath.Join(base, alt))
				}
			}
			writeJSON(w, map[string]any{"ok": true, "avatar_url": "/chars/" + name + "/" + fname})
			return
		}
		if len(parts) == 2 && parts[1] == "meta" && r.Method == "PUT" {
			// Per-character prefs: {"avatar_px": 64}. Stored in
			// data/characters/<name>/meta.json (rclone-friendly).
			b, _ := io.ReadAll(r.Body)
			var meta map[string]any
			if err := json.Unmarshal(b, &meta); err != nil {
				http.Error(w, "bad meta json", 400)
				return
			}
			_ = os.MkdirAll(base, 0o755)
			cur := loadCharMeta(base)
			if v, ok := meta["avatar_px"].(float64); ok && v >= 24 && v <= 480 {
				cur["avatar_px"] = int(v)
			}
			mb, _ := json.MarshalIndent(cur, "", "  ")
			_ = os.WriteFile(filepath.Join(base, "meta.json"), mb, 0o644)
			writeJSON(w, map[string]any{"ok": true, "meta": cur})
			return
		}
		// detail
		avatarURL := ""
		for _, cand := range []string{"avatar.webp", "avatar.png", "avatar.jpg", "avatar.gif"} {
			if _, err := os.Stat(filepath.Join(base, cand)); err == nil {
				avatarURL = "/chars/" + name + "/" + cand
				break
			}
		}
		var exprs []string
		if es, err := os.ReadDir(filepath.Join(base, "expressions")); err == nil {
			for _, e := range es {
				if !e.IsDir() {
					exprs = append(exprs, "expressions/"+e.Name())
				}
			}
		}
		if exprs == nil {
			exprs = []string{}
		}
		meta := loadCharMeta(base)
		avatarPx := 48
		if v, ok := meta["avatar_px"].(float64); ok && v >= 24 {
			avatarPx = int(v)
		} else if v, ok := meta["avatar_px"].(int); ok && v >= 24 {
			avatarPx = v
		}
		writeJSON(w, map[string]any{"name": name, "avatar_url": avatarURL, "expressions": exprs, "avatar_px": avatarPx})
	})
	mux.Handle("/chars/", http.StripPrefix("/chars/", http.FileServer(http.Dir(filepath.Join(cfg.DataRoot, "characters")))))

	// Audit list + detail + replay.
	mux.HandleFunc("/api/audit", func(w http.ResponseWriter, r *http.Request) {
		ids, _ := st.ListAudits()
		writeJSON(w, map[string]any{"ids": ids})
	})
	mux.HandleFunc("/api/audit/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/audit/")
		if strings.HasSuffix(rest, "/replay") && r.Method == "POST" {
			id := strings.TrimSuffix(rest, "/replay")
			a, err := st.LoadAudit(id)
			if err != nil {
				http.Error(w, "audit not found", 404)
				return
			}
			// Optional body: {edit_blocks / edit_order} -> re-assemble only (no LLM).
			b, _ := io.ReadAll(r.Body)
			if len(b) == 0 {
				writeJSON(w, map[string]any{"audit": a, "note": "pass {raw_request} edits to re-assemble; replay with model via /v1/chat/completions"})
				return
			}
			var edit struct {
				Raw map[string]any `json:"raw_request"`
			}
			_ = json.Unmarshal(b, &edit)
			writeJSON(w, map[string]any{"audit": a, "edit_received": edit.Raw != nil})
			return
		}
		a, err := st.LoadAudit(rest)
		if err != nil {
			http.Error(w, "audit not found", 404)
			return
		}
		writeJSON(w, a)
	})

	// Chat history: GET /api/history?session=<char>&limit=10&before=0
	// Sliding window over data/chats/<session>.jsonl (newest last).
	// `before` = messages already shown (for "load earlier").
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		session := store.CleanSession(r.URL.Query().Get("session"))
		limit := atoiDefault(r.URL.Query().Get("limit"), 10)
		before := atoiDefault(r.URL.Query().Get("before"), 0)
		msgs, total, err := st.Tail(session, limit, before)
		if err != nil {
			http.Error(w, "history read failed", 500)
			return
		}
		writeJSON(w, map[string]any{"session": session, "total": total, "messages": msgs, "has_more": total-before-limit > 0})
	})

	// Export timeline md (same shape as raw_chat_timeline_process.py output so it
	// feeds the RAG pipeline directly): GET /api/export?session=<char>&user=RoyChong
	// Header: "# A & B Chat: YYYY-MM-DD ~ YYYY-MM-DD", rows: "**Name** [ts]: text".
	mux.HandleFunc("/api/export", func(w http.ResponseWriter, r *http.Request) {
		session := store.CleanSession(r.URL.Query().Get("session"))
		userName := strings.TrimSpace(r.URL.Query().Get("user"))
		if userName == "" {
			userName = "user"
		}
		msgs, err := st.LoadAll(session)
		if err != nil {
			http.Error(w, "export read failed", 500)
			return
		}
		md := timelineMD(session, userName, msgs)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+session+" (timeline).md\"")
		_, _ = w.Write([]byte(md))
	})

	// Archive ("new chat" in ST terms): POST /api/archive {"session":"..."}
	// Seals current jsonl into chats/archive/ and starts a fresh one.
	mux.HandleFunc("/api/archive", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		var in struct {
			Session string `json:"session"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		name, err := st.Archive(in.Session)
		if err != nil {
			http.Error(w, "archive failed: "+err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "archived": name})
	})

	// ---- Ollama shim for the TavernLab Flutter app (single floor) ----
	// The app speaks native Ollama API only: GET /api/tags, POST /api/chat,
	// POST /api/generate. Everything reuses the assemble+forward chain, so
	// Budget/MCP/Audit/JSONL all apply. Session is fixed to the single floor.
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		s := rt.get()
		ids := upstreamModelIDs(client, s.Upstream, s.APIKey)
		models := make([]map[string]any, 0, len(ids))
		now := time.Now().Format(time.RFC3339)
		for _, id := range ids {
			models = append(models, map[string]any{
				"name": id, "model": id, "modified_at": now,
				"size": 0, "digest": "-",
				"details": map[string]any{
					"parent_model": "", "format": "", "family": "tavernlab",
					"families": []string{"tavernlab"},
					"parameter_size": "", "quantization_level": "",
				},
			})
		}
		if models == nil {
			models = []map[string]any{}
		}
		writeJSON(w, map[string]any{"models": models})
	})

	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		s := rt.get()
		var in struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		model := firstNonEmpty(in.Model, "auto-gemini")
		turns := make([]map[string]string, 0, len(in.Messages))
		for _, m := range in.Messages {
			role := m.Role
			if role != "user" && role != "assistant" && role != "system" {
				role = "user"
			}
			turns = append(turns, map[string]string{"role": role, "content": m.Content})
		}
		if q := lastUserText(turns); q != "" {
			_ = st.AppendChat(singleFloor, "user", q)
			if all, err := st.LoadAll(singleFloor); err == nil {
				turns = chatToTurns(all)
			}
		}
		blocks := injectChat(loadBlocks(), turns)
		memInfo := resolveMCP(r.Context(), s, blocks, turns)
		res := engine.Assemble(blocks, cfg.Ctx)
		upBody, _ := json.Marshal(map[string]any{"model": model, "messages": res.Messages, "stream": in.Stream})
		auditID := time.Now().Format("20060102-150405.000")

		if !in.Stream {
			status, respBody, usage := proxy.Forward(client, s.Upstream, s.APIKey, upBody, false, nil, nil)
			reply := replyText(respBody)
			_ = st.SaveAudit(toAudit(auditID, model, res, upBody, respBody, usage, memInfo))
			_ = st.AppendChat(singleFloor, "assistant", reply)
			w.WriteHeader(status)
			writeJSON(w, map[string]any{
				"model": model, "created_at": time.Now().Format(time.RFC3339),
				"message": map[string]any{"role": "assistant", "content": reply},
				"done_reason": "stop", "done": true,
			})
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", 500)
			return
		}
		status, full := forwardOllamaStream(client, s.Upstream, s.APIKey, upBody, model, w, flusher)
		_ = status
		rebuilt, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": full}}},
			"stream_rebuilt": true, "via": "ollama-shim",
		})
		_ = st.SaveAudit(toAudit(auditID, model, res, upBody, rebuilt, nil, memInfo))
		_ = st.AppendChat(singleFloor, "assistant", full)
	})

	// POST /api/generate {"model":"...","prompt":"..."} -> {"response":title,"done":true}
	// Powers the app's AI chat titles. Fail-open: stub title from prompt head.
	mux.HandleFunc("/api/generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		s := rt.get()
		var in struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		model := firstNonEmpty(in.Model, "auto-gemini")
		title := stubTitle(in.Prompt)
		upBody, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": 256,
			"messages": []map[string]any{{"role": "user", "content": in.Prompt}},
		})
		if _, respBody, _ := proxy.Forward(client, s.Upstream, s.APIKey, upBody, false, nil, nil); len(respBody) > 0 {
			if t := strings.TrimSpace(replyText(respBody)); t != "" {
				title = firstLine(t, 60)
			}
		}
		writeJSON(w, map[string]any{
			"model": model, "created_at": time.Now().Format(time.RFC3339),
			"response": title, "done": true,
		})
	})

	// Static frontend.
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	log.Printf("tavernlab listening on %s (upstream=%s data=%s)", addr, cfg.Upstream, cfg.DataRoot)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// injectChat renders {{chat_history}} into the chat block from simple turns.
func injectChat(blocks []engine.Block, chat []map[string]string) []engine.Block {
	if len(chat) == 0 {
		return blocks
	}
	var sb strings.Builder
	for _, m := range chat {
		sb.WriteString(m["role"] + ": " + m["content"] + "\n")
	}
	out := make([]engine.Block, len(blocks))
	copy(out, blocks)
	for i := range out {
		if out[i].Source.Type == "chat" {
			out[i].Content = strings.ReplaceAll(out[i].Template, "{{chat_history}}", sb.String())
			if out[i].Content == out[i].Template {
				out[i].Content = sb.String()
			}
		}
	}
	return out
}

// injectRawMessages wraps caller-supplied messages as the chat block.
func injectRawMessages(blocks []engine.Block, msgs []map[string]any) []engine.Block {
	var sb strings.Builder
	for _, m := range msgs {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		sb.WriteString(role + ": " + content + "\n")
	}
	return injectChat(blocks, []map[string]string{{"role": "chat", "content": sb.String()}})
}

func toAudit(id string, model any, res engine.AssembleResult, upBody, respBody []byte, usage map[string]any, mem map[string]any) store.Audit {
	var raw map[string]any
	_ = json.Unmarshal(upBody, &raw)
	var resp map[string]any
	_ = json.Unmarshal(respBody, &resp)
	reply := ""
	if resp != nil {
		if ch, ok := resp["choices"].([]any); ok && len(ch) > 0 {
			if m, ok := ch[0].(map[string]any); ok {
				if msg, ok := m["message"].(map[string]any); ok {
					reply, _ = msg["content"].(string)
				}
			}
		}
	}
	mName := "default"
	if s, ok := model.(string); ok && s != "" {
		mName = s
	}
	rows := make([]store.BlockRow, 0, len(res.Blocks))
	for _, b := range res.Blocks {
		rows = append(rows, store.BlockRow{ID: b.ID, Role: b.Role, Order: b.Order, Tokens: b.Tokens, Cut: b.Truncated, Note: b.DroppedNote})
	}
	return store.Audit{
		ID: id, Time: time.Now().Format(time.RFC3339), Model: mName,
		Budget: res.BudgetTok, TotalTok: res.TotalTok, Blocks: rows,
		Dropped: res.Dropped, Raw: raw, ReplyText: reply, Upstream: usage,
		Memory: mem,
	}
}

// firstNonEmpty picks the first non-blank string (for UI fallbacks).
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// loadCharMeta reads data/characters/<name>/meta.json ({} when absent).
func loadCharMeta(base string) map[string]any {
	meta := map[string]any{}
	b, err := os.ReadFile(filepath.Join(base, "meta.json"))
	if err != nil {
		return meta
	}
	_ = json.Unmarshal(b, &meta)
	if meta == nil {
		meta = map[string]any{}
	}
	return meta
}

// chatToTurns maps stored rows to engine turns. Assistant content is passed
// through verbatim (no name prefix) — prefixing used to teach the model to
// emit "角色名: " in its own replies; attribution for export comes from
// timelineMD instead, which reads the JSONL directly.
func chatToTurns(msgs []store.ChatMessage) []map[string]string {
	turns := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		role := m.Role
		if role != "user" && role != "assistant" && role != "system" {
			role = "user"
		}
		turns = append(turns, map[string]string{"role": role, "content": m.Text})
	}
	return turns
}

// timelineMD renders stored rows in raw_chat_timeline_process.py timeline
// shape: "# A & B Chat: YYYY-MM-DD ~ YYYY-MM-DD" + "**Name** [ts]: text".
func timelineMD(session, userName string, msgs []store.ChatMessage) string {
	speakers := map[string]bool{}
	label := func(role string) string {
		if role == "assistant" {
			return session
		}
		return userName
	}
	for _, m := range msgs {
		speakers[label(m.Role)] = true
	}
	names := make([]string, 0, len(speakers))
	for n := range speakers {
		names = append(names, n)
	}
	sort.Strings(names)
	span := func(t string) string {
		if ts, err := time.Parse(time.RFC3339, t); err == nil {
			return ts.Format("2006-01-02 15:04")
		}
		return t
	}
	var sb strings.Builder
	if len(msgs) > 0 {
		start := span(msgs[0].Time)[:10]
		end := span(msgs[len(msgs)-1].Time)[:10]
		sb.WriteString("# " + strings.Join(names, " & ") + " Chat: " + start + " ~ " + end + "\n")
	} else {
		sb.WriteString("# " + session + " Chat: (empty)\n")
	}
	for _, m := range msgs {
		sb.WriteString("\n**" + label(m.Role) + "** [" + span(m.Time) + "]: " + m.Text + "\n")
	}
	return sb.String()
}

func atoiDefault(s string, d int) int {
	if s == "" {
		return d
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return d
	}
	return n
}

// singleFloor is the one conversation the Flutter app ever sees.
// Web UI shares it when settings.char matches (default Leer乐儿).
const singleFloor = "Leer乐儿"

// upstreamModelIDs lists upstream /v1/models ids ("" upstream -> empty).
func upstreamModelIDs(client *http.Client, upstream, apiKey string) []string {
	if strings.TrimSpace(upstream) == "" {
		return nil
	}
	req, err := http.NewRequest("GET", strings.TrimRight(upstream, "/")+"/v1/models", nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	var ids []string
	for _, d := range m.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// forwardOllamaStream POSTs an SSE chat body upstream and relays it as Ollama
// NDJSON chunks ({"message":{"content":...},"done":false} … {"done":true}).
// Returns upstream status + full assistant text (for audit/JSONL).
func forwardOllamaStream(client *http.Client, upstream, apiKey string, upBody []byte, model string, w http.ResponseWriter, flusher http.Flusher) (int, string) {
	req, err := http.NewRequest("POST", strings.TrimRight(upstream, "/")+"/v1/chat/completions", bytes.NewReader(upBody))
	if err != nil {
		return 500, ""
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 502, ""
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(resp.StatusCode)
	emit := func(content string, done bool) {
		line, _ := json.Marshal(map[string]any{
			"model": model, "created_at": time.Now().Format(time.RFC3339),
			"message":     map[string]any{"role": "assistant", "content": content},
			"done_reason": map[string]any(nil), "done": done,
		})
		// Ollama omits done_reason until the end; keep key absent when streaming.
		if !done {
			line, _ = json.Marshal(map[string]any{
				"model": model, "created_at": time.Now().Format(time.RFC3339),
				"message": map[string]any{"role": "assistant", "content": content},
				"done":    false,
			})
		} else {
			line, _ = json.Marshal(map[string]any{
				"model": model, "created_at": time.Now().Format(time.RFC3339),
				"message": map[string]any{"role": "assistant", "content": ""},
				"done_reason": "stop", "done": true,
			})
		}
		_, _ = w.Write(append(line, '\n'))
		flusher.Flush()
	}
	var full strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				full.WriteString(c.Delta.Content)
				emit(c.Delta.Content, false)
			}
		}
	}
	emit("", true)
	return resp.StatusCode, full.String()
}

// stubTitle falls back to the prompt head when the title LLM call fails.
func stubTitle(prompt string) string { return firstLine(prompt, 40) }

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	if s == "" {
		return "新对话"
	}
	return s
}

// lastUserText returns the latest user utterance from chat turns.
func lastUserText(chat []map[string]string) string {
	for i := len(chat) - 1; i >= 0; i-- {
		if chat[i]["role"] == "user" && strings.TrimSpace(chat[i]["content"]) != "" {
			return chat[i]["content"]
		}
	}
	return ""
}

// resolveMCP fills enabled mcp-source blocks by searching rag-mcp-server with
// the latest user utterance. Fail-open: errors are recorded, chat continues.
// Returns an audit-friendly summary (also served by /api/assemble preview).
func resolveMCP(ctx context.Context, s settings.Settings, blocks []engine.Block, chat []map[string]string) map[string]any {
	info := map[string]any{"enabled": false}
	if !s.MCPEnabled {
		return info
	}
	info["enabled"] = true
	info["collection"] = firstNonEmpty(s.MCPCollection, "(server default)")
	query := lastUserText(chat)
	info["query"] = query
	if query == "" {
		info["hits"] = 0
		return info
	}
	cli := mcp.New(firstNonEmpty(s.MCPURL, "http://192.168.10.2:8199"))
	// Bound the recall so a hung MCP never stalls a chat turn.
	type res struct {
		hits []memory.Hit
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		h, err := cli.Search(ctx, query, s.MCPCollection, s.MCPTopK, s.MCPThreshold)
		ch <- res{h, err}
	}()
	var hits []memory.Hit
	select {
	case r := <-ch:
		if r.err != nil {
			info["error"] = r.err.Error()
			info["hits"] = 0
			return info
		}
		hits = r.hits
	case <-time.After(20 * time.Second):
		info["error"] = "mcp search timeout (20s)"
		info["hits"] = 0
		return info
	}
	text := mcp.Format(hits)
	for i := range blocks {
		if blocks[i].Source.Type == "mcp" && blocks[i].Enabled {
			if strings.Contains(blocks[i].Template, "{{rag}}") {
				blocks[i].Content = strings.ReplaceAll(blocks[i].Template, "{{rag}}", text)
			} else {
				blocks[i].Content = text
			}
		}
	}
	info["hits"] = len(hits)
	if len(hits) > 0 {
		top := hits[0]
		info["top"] = map[string]any{"score": top.Score, "source": top.Source, "excerpt": excerpt(top.Text, 160)}
	}
	return info
}

func excerpt(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// replyText extracts the assistant text from a non-stream upstream body.
func replyText(respBody []byte) string {
	var resp map[string]any
	_ = json.Unmarshal(respBody, &resp)
	if resp == nil {
		return ""
	}
	if ch, ok := resp["choices"].([]any); ok && len(ch) > 0 {
		if m, ok := ch[0].(map[string]any); ok {
			if msg, ok := m["message"].(map[string]any); ok {
				s, _ := msg["content"].(string)
				return s
			}
		}
	}
	return ""
}

// rebuiltText extracts assistant text from the stream-rebuilt audit body.
func rebuiltText(rebuilt []byte) string {
	return replyText(rebuilt)
}
