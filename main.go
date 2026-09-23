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
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/gif"  // register decoders for image.DecodeConfig
	_ "image/jpeg" // (media may be png/jpg/gif; header-only, cheap)
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/distill"
	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/expression"
	"github.com/RoyChong5053/TavernLab/internal/mcp"
	"github.com/RoyChong5053/TavernLab/internal/memory"
	"github.com/RoyChong5053/TavernLab/internal/obs"
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
	if v, ok := patch["mcp_timeout"].(float64); ok && v > 0 {
		r.cur.MCPTimeout = int(v)
	}
	if v, ok := patch["mcp_threshold"].(float64); ok {
		r.cur.MCPThreshold = v
	}
	if v, ok := patch["current_char"].(string); ok && v != "" {
		r.cur.CurrentChar = v
	}
	if v, ok := patch["user_name"].(string); ok && v != "" {
		r.cur.UserName = v
	}
	if v, ok := patch["ntfy_url"]; ok {
		if s, ok := v.(string); ok {
			r.cur.NtfyURL = s
		}
	}
	if v, ok := patch["ntfy_topic"]; ok {
		if s, ok := v.(string); ok {
			r.cur.NtfyTopic = s
		}
	}
	if v, ok := patch["distill_enabled"].(bool); ok {
		r.cur.DistillEnabled = v
	}
	if v, ok := patch["distill_interval"].(float64); ok && v > 0 {
		r.cur.DistillInterval = int(v)
	}
	if v, ok := patch["distill_max_chars"].(float64); ok && v > 0 {
		r.cur.DistillMaxChars = int(v)
	}
	if v, ok := patch["distill_retain_days"].(float64); ok && v > 0 {
		r.cur.DistillRetainDays = int(v)
	}
	if v, ok := patch["distill_model"]; ok {
		if s, ok := v.(string); ok {
			r.cur.DistillModel = s
		}
	}
	if v, ok := patch["distill_prompt"]; ok {
		if s, ok := v.(string); ok {
			r.cur.DistillPrompt = s
		}
	}
	_ = settings.Save(root, r.cur) // best-effort; key stays usable in memory regardless
	return r.cur
}

func main() {
	port := flag.Int("port", 8888, "listen port")
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
	st.MigrateLegacyChats()
	client := &http.Client{Timeout: 10 * time.Minute}
	events = newHub()
	loadCalibration(cfg.DataRoot)

	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		s := rt.get()
		writeJSON(w, map[string]any{"ok": true, "time": time.Now().Format(time.RFC3339), "upstream": s.Upstream, "key_set": s.APIKey != ""})
	})

	// Runtime logs (ring buffer) + live SSE stream, backed by internal/obs.
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			limit := atoiDefault(r.URL.Query().Get("limit"), 200)
			writeJSON(w, map[string]any{"level": string(obs.GetLevel()), "entries": obs.Recent(limit)})
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			var in struct {
				Level string `json:"level"`
			}
			_ = json.Unmarshal(b, &in)
			if in.Level != "" {
				obs.SetLevel(obs.Level(in.Level))
			}
			writeJSON(w, map[string]any{"ok": true, "level": string(obs.GetLevel())})
		default:
			http.Error(w, "method not allowed", 405)
		}
	})
	mux.HandleFunc("/api/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		ch, unsub := obs.Subscribe()
		defer unsub()
		_, _ = io.WriteString(w, ": connected\n\n")
		flusher.Flush()
		ctx := r.Context()
		keep := time.NewTicker(25 * time.Second)
		defer keep.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				b, _ := json.Marshal(e)
				_, _ = fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
				flusher.Flush()
			case <-keep.C:
				_, _ = io.WriteString(w, ": ping\n\n")
				flusher.Flush()
			}
		}
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
				"mcp_enabled": s.MCPEnabled, "mcp_topk": s.MCPTopK, "mcp_timeout": s.MCPTimeout, "mcp_threshold": s.MCPThreshold,
				"current_char": s.CurrentChar, "user_name": s.UserName,
				"ntfy_url": s.NtfyURL, "ntfy_topic": s.NtfyTopic,
				"distill_enabled": s.DistillEnabled, "distill_interval": s.DistillInterval,
				"distill_max_chars": s.DistillMaxChars, "distill_retain_days": s.DistillRetainDays,
				"distill_model":  s.DistillModel,
				"distill_prompt": s.DistillPrompt, "distill_prompt_default": distill.DefaultPrompt,
				"estimate_scale": engine.TextScale(),
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

	// Distilled Memory: current fact sheet + run-now trigger.
	mux.HandleFunc("/api/distilled", func(w http.ResponseWriter, r *http.Request) {
		s := rt.get()
		session := store.CleanSession(firstNonEmpty(r.URL.Query().Get("session"), s.CurrentChar, "main"))
		sheet := distill.Load(cfg.DataRoot, session)
		prompt := s.DistillPrompt
		if prompt == "" {
			prompt = distill.DefaultPrompt
		}
		writeJSON(w, map[string]any{
			"session": session, "sheet": sheet, "meta": distill.LoadMeta(cfg.DataRoot, session),
			"enabled": s.DistillEnabled, "interval": s.DistillInterval,
			"max_chars": s.DistillMaxChars, "retain_days": s.DistillRetainDays,
			"model":  s.DistillModel,
			"prompt": prompt, "default_prompt": distill.DefaultPrompt,
		})
	})
	mux.HandleFunc("/api/distilled/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		var in struct {
			Session string `json:"session"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		s := rt.get()
		session := store.CleanSession(firstNonEmpty(in.Session, s.CurrentChar, "main"))
		sheet, err := runDistill(r.Context(), st, client, cfg.DataRoot, s, session, true)
		if err != nil {
			obs.Warn("distill failed", map[string]any{"session": session, "error": err.Error()})
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "sheet": sheet})
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
		hits, err := searchMCP(r.Context(), s, in.Query)
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
				writeJSON(w, normalizeBlocks(cfg.DefaultBlocks))
				return
			}
			var blocks []engine.Block
			if json.Unmarshal(b, &blocks) != nil {
				writeJSON(w, normalizeBlocks(cfg.DefaultBlocks))
				return
			}
			writeJSON(w, normalizeBlocks(blocks))
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
			return normalizeBlocks(cfg.DefaultBlocks)
		}
		var blocks []engine.Block
		if err := json.Unmarshal(b, &blocks); err != nil {
			return normalizeBlocks(cfg.DefaultBlocks)
		}
		return normalizeBlocks(blocks)
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
		if len(ctx.Tiers) == 0 && ctx.MaxTokens == 0 {
			ctx = cfg.Ctx
		}
		session := firstNonEmpty(in.Session, rt.get().CurrentChar, "main")
		turns := engineTurns(in.Chat)
		if in.Session != "" {
			if all, err := st.LoadAll(in.Session); err == nil {
				turns = chatToTurns(cfg.DataRoot, session, all)
			}
		}
		blocks = renderBlocks(cfg.DataRoot, session, rt.get().UserName, blocks)
		memInfo := resolveMCP(r.Context(), rt.get(), blocks, turns)
		res := engine.Assemble(engine.Input{Blocks: blocks, Cfg: ctx, Turns: turns})
		out := map[string]any{
			"messages": res.Messages, "blocks": res.Blocks, "total_tokens": res.TotalTok,
			"budget_tokens": res.BudgetTok, "tier": res.Tier, "overflow": res.Overflow,
			"dropped": res.Dropped, "prompt_text": res.PromptText,
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
			Text     string               `json:"text"`   // new path: single fresh user message
			Images   []string             `json:"images"` // data URLs / raw base64 for the fresh message
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
		if len(ctx.Tiers) == 0 && ctx.MaxTokens == 0 {
			ctx = cfg.Ctx
		}
		session := store.CleanSession(firstNonEmpty(in.Session, s.CurrentChar, "main"))
		// New path: frontend sends only the fresh message; full context slides
		// server-side out of the (possibly thousands of turns) JSONL so the DOM
		// window size never affects what the model sees.
		turns := engineTurns(in.Chat)
		var upImages []string
		userText := strings.TrimSpace(in.Text)
		persisted := false
		if userText != "" || len(in.Images) > 0 {
			// New path: the frontend sends only the fresh message. Images may
			// arrive with or without a caption (image-only is allowed).
			paths, dataURLs, _ := saveImages(cfg.DataRoot, session, in.Images)
			upImages = dataURLs
			msg, _ := st.AppendChat(session, "user", userText, paths...)
			events.publish(session, msg)
			all, _ := st.LoadAll(session)
			turns = chatToTurns(cfg.DataRoot, session, all)
			persisted = true
		} else if len(in.Messages) > 0 {
			// classic OpenAI path: convert caller messages to turns
			turns = rawToTurns(in.Messages)
		}
		blocks = renderBlocks(cfg.DataRoot, session, s.UserName, blocks)
		memInfo := resolveMCP(r.Context(), s, blocks, turns)
		res := engine.Assemble(engine.Input{Blocks: blocks, Cfg: ctx, Turns: turns})

		// Build upstream body from assembled messages (attach fresh images).
		upMsgs := buildUpMessages(res.Messages, upImages)
		model := in.Model
		if model == nil || model == "" {
			model = "default"
		}
		upBody := streamUpBody(model, upMsgs, in.Stream)
		auditID := time.Now().Format("20060102-150405.000")

		// Legacy path (no Text, no Images): user turn arrived inside in.Chat.
		if !persisted && userText == "" {
			if q := lastUserText(turns); q != "" {
				msg, _ := st.AppendChat(session, "user", q)
				events.publish(session, msg)
			}
		}

		if in.Stream {
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", 500)
				return
			}
			status, rebuilt, usage := proxy.Forward(client, s.Upstream, s.APIKey, upBody, true, w, flusher)
			_ = st.SaveAudit(toAudit(auditID, model, res, upBody, rebuilt, usage, memInfo))
			reply := strings.TrimSpace(rebuiltText(rebuilt))
			logTurn("chat.stream", status, model, session, res, reply, memInfo, usagePromptTokens(usage))
			updateCalibration(cfg.DataRoot, res.TotalTok, usagePromptTokens(usage))
			if reply != "" {
				msg, _ := st.AppendChat(session, "assistant", reply)
				events.publish(session, msg)
				publishNtfy(s, msg)
			}
			maybeDistill(st, client, cfg.DataRoot, s, session)
			return
		}
		status, respBody, usage := proxy.Forward(client, s.Upstream, s.APIKey, upBody, false, nil, nil)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
		_ = st.SaveAudit(toAudit(auditID, model, res, upBody, respBody, usage, memInfo))
		reply := strings.TrimSpace(replyText(respBody))
		logTurn("chat", status, model, session, res, reply, memInfo, usagePromptTokens(usage))
		updateCalibration(cfg.DataRoot, res.TotalTok, usagePromptTokens(usage))
		if status >= 400 {
			obs.Warn("upstream error", map[string]any{"path": "/v1/chat/completions", "status": status, "body": excerpt(string(respBody), 300)})
		}
		if reply != "" {
			msg, _ := st.AppendChat(session, "assistant", reply)
			events.publish(session, msg)
			publishNtfy(s, msg)
		}
		maybeDistill(st, client, cfg.DataRoot, s, session)
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

	// Characters: list/create/detail/delete + card + avatar.
	// Self-contained layout: data/characters/<name>/{card.json,avatar.*,chat.jsonl,media/,archive/}
	mux.HandleFunc("/api/characters", func(w http.ResponseWriter, r *http.Request) {
		root := filepath.Join(cfg.DataRoot, "characters")
		switch r.Method {
		case "GET":
			es, _ := os.ReadDir(root)
			out := []map[string]any{}
			for _, e := range es {
				if !e.IsDir() {
					continue
				}
				base := filepath.Join(root, e.Name())
				out = append(out, map[string]any{
					"name":        e.Name(),
					"description": loadCharCard(base).Description,
					"avatar_url":  charAvatarURL(cfg.DataRoot, e.Name()),
					"avatar_px":   charAvatarPx(base),
				})
			}
			writeJSON(w, map[string]any{"characters": out})
		case "POST":
			var in struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &in); err != nil {
				http.Error(w, "bad json", 400)
				return
			}
			name, ok := store.SafeCharName(in.Name)
			if !ok {
				http.Error(w, "invalid character name", 400)
				return
			}
			base := filepath.Join(root, name)
			if _, err := os.Stat(base); err == nil {
				http.Error(w, "character already exists", 409)
				return
			}
			if err := os.MkdirAll(base, 0o755); err != nil {
				http.Error(w, "create failed", 500)
				return
			}
			if err := saveCharCard(base, CharCard{Name: name, Description: in.Description, Created: time.Now().Format(time.RFC3339)}); err != nil {
				http.Error(w, "create failed", 500)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "name": name})
		default:
			http.Error(w, "method not allowed", 405)
		}
	})
	mux.HandleFunc("/api/characters/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/characters/")
		parts := strings.SplitN(rest, "/", 2)
		name := parts[0]
		if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
			http.Error(w, "bad name", 400)
			return
		}
		base := filepath.Join(cfg.DataRoot, "characters", name)
		sub := ""
		if len(parts) == 2 {
			sub = parts[1]
		}

		switch {
		case sub == "" && r.Method == "GET":
			card := loadCharCard(base)
			writeJSON(w, map[string]any{
				"name": name, "description": card.Description, "created": card.Created,
				"avatar_url": charAvatarURL(cfg.DataRoot, name), "avatar_px": charAvatarPx(base),
				"expressions": listExpressions(base),
			})
		case sub == "" && r.Method == "DELETE":
			// Default: keep chat data. ?chats=1 explicitly removes chat too.
			if r.URL.Query().Get("chats") == "1" {
				_ = os.RemoveAll(base)
			} else {
				_ = os.Remove(filepath.Join(base, "card.json"))
				for _, a := range []string{"avatar.webp", "avatar.png", "avatar.jpg", "avatar.gif", "avatar.jpeg"} {
					_ = os.Remove(filepath.Join(base, a))
				}
			}
			writeJSON(w, map[string]any{"ok": true, "chats_kept": r.URL.Query().Get("chats") != "1"})
		case sub == "card" && r.Method == "PUT":
			var in struct {
				Description string `json:"description"`
			}
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &in); err != nil {
				http.Error(w, "bad json", 400)
				return
			}
			_ = os.MkdirAll(base, 0o755)
			card := loadCharCard(base)
			card.Name = name
			card.Description = in.Description
			if card.Created == "" {
				card.Created = time.Now().Format(time.RFC3339)
			}
			if err := saveCharCard(base, card); err != nil {
				http.Error(w, "save failed", 500)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "card": card})
		case sub == "avatar" && r.Method == "PUT":
			// multipart file=... or raw body; saved as avatar.<ext>.
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
				n := strings.ToLower(h.Filename)
				switch {
				case strings.HasSuffix(n, ".png"):
					fname = "avatar.png"
				case strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".jpeg"):
					fname = "avatar.jpg"
				case strings.HasSuffix(n, ".gif"):
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
			for _, alt := range []string{"avatar.webp", "avatar.png", "avatar.jpg", "avatar.jpeg", "avatar.gif"} {
				if alt != fname {
					_ = os.Remove(filepath.Join(base, alt))
				}
			}
			writeJSON(w, map[string]any{"ok": true, "avatar_url": "/chars/" + name + "/" + fname})
		case sub == "meta" && r.Method == "PUT":
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
		default:
			http.Error(w, "not found", 404)
		}
	})
	// Static avatar/media only: chat.jsonl / card.json / archive stay private.
	mux.HandleFunc("/chars/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/chars/")
		if rel == "" || strings.Contains(rel, "..") {
			http.Error(w, "bad path", 400)
			return
		}
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".webp", ".png", ".jpg", ".jpeg", ".gif":
		default:
			http.Error(w, "not found", 404)
			return
		}
		http.ServeFile(w, r, filepath.Join(cfg.DataRoot, "characters", filepath.FromSlash(rel)))
	})

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
	// Sliding window over data/characters/<char>/chat.jsonl (newest last).
	// `before` = messages already shown (for "load earlier").
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		session := store.CleanSession(firstNonEmpty(r.URL.Query().Get("session"), rt.get().CurrentChar))
		limit := atoiDefault(r.URL.Query().Get("limit"), 10)
		before := atoiDefault(r.URL.Query().Get("before"), 0)
		msgs, total, err := st.Tail(session, limit, before)
		if err != nil {
			http.Error(w, "history read failed", 500)
			return
		}
		writeJSON(w, map[string]any{"session": session, "total": total, "messages": msgs, "has_more": total-before-limit > 0})
	})

	// Live events: GET /api/events?session=<char> (SSE). Foreground clients
	// subscribe and receive every appended message in real time; the app's
	// sync button remains the fallback when the connection drops.
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		session := store.CleanSession(firstNonEmpty(r.URL.Query().Get("session"), rt.get().CurrentChar, "main"))
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		_, _ = io.WriteString(w, ": connected\n\n")
		flusher.Flush()

		ch := events.subscribe(session)
		defer events.unsubscribe(session, ch)
		ctx := r.Context()
		keep := time.NewTicker(25 * time.Second)
		defer keep.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-ch:
				b, _ := json.Marshal(m)
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				flusher.Flush()
			case <-keep.C:
				_, _ = io.WriteString(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	})

	// Export timeline md (same shape as raw_chat_timeline_process.py output so it
	// feeds the RAG pipeline directly): GET /api/export?session=<char>&user=RoyChong
	// &archives=1 also prepends every archived floor (full lifetime, for RAG).
	mux.HandleFunc("/api/export", func(w http.ResponseWriter, r *http.Request) {
		session := store.CleanSession(firstNonEmpty(r.URL.Query().Get("session"), rt.get().CurrentChar))
		userName := strings.TrimSpace(r.URL.Query().Get("user"))
		if userName == "" {
			userName = firstNonEmpty(rt.get().UserName, "user")
		}
		var all []store.ChatMessage
		if r.URL.Query().Get("archives") == "1" {
			names, _ := st.ListArchives(session)
			for i := len(names) - 1; i >= 0; i-- { // oldest floor first
				if msgs, err := st.LoadArchive(session, names[i]); err == nil {
					all = append(all, msgs...)
				}
			}
		}
		cur, err := st.LoadAll(session)
		if err != nil {
			http.Error(w, "export read failed", 500)
			return
		}
		all = append(all, cur...)
		md := timelineMD(session, userName, all)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+session+" (timeline).md\"")
		_, _ = w.Write([]byte(md))
	})

	// Archive ("new chat" in ST terms): POST /api/archive {"session":"..."}
	// Seals <char>/chat.jsonl into <char>/archive/<ts>.jsonl and starts fresh.
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
		name, err := st.Archive(firstNonEmpty(in.Session, rt.get().CurrentChar))
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
					"families":       []string{"tavernlab"},
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
				Role    string   `json:"role"`
				Content string   `json:"content"`
				Images  []string `json:"images"`
			} `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		model := firstNonEmpty(in.Model, "auto-gemini")
		session := store.CleanSession(firstNonEmpty(s.CurrentChar, "tavernlab"))
		// The app mirrors the server: only the newest user message matters;
		// full context is slid server-side out of the character's JSONL.
		var freshText string
		var freshImages []string
		for i := len(in.Messages) - 1; i >= 0; i-- {
			// A fresh turn may carry text, images, or both (image-only sends
			// are valid: the user may just want the model to look at a picture).
			if in.Messages[i].Role == "user" &&
				(strings.TrimSpace(in.Messages[i].Content) != "" || len(in.Messages[i].Images) > 0) {
				freshText = in.Messages[i].Content
				freshImages = in.Messages[i].Images
				break
			}
		}
		var upImages []string
		if strings.TrimSpace(freshText) != "" || len(freshImages) > 0 {
			paths, dataURLs, _ := saveImages(cfg.DataRoot, session, freshImages)
			upImages = dataURLs
			msg, _ := st.AppendChat(session, "user", strings.TrimSpace(freshText), paths...)
			events.publish(session, msg)
		}
		all, _ := st.LoadAll(session)
		turns := chatToTurns(cfg.DataRoot, session, all)
		blocks := renderBlocks(cfg.DataRoot, session, s.UserName, loadBlocks())
		memInfo := resolveMCP(r.Context(), s, blocks, turns)
		res := engine.Assemble(engine.Input{Blocks: blocks, Cfg: cfg.Ctx, Turns: turns})
		upMsgs := buildUpMessages(res.Messages, upImages)
		upBody := streamUpBody(model, upMsgs, in.Stream)
		auditID := time.Now().Format("20060102-150405.000")

		if !in.Stream {
			status, respBody, usage := proxy.Forward(client, s.Upstream, s.APIKey, upBody, false, nil, nil)
			reply := replyText(respBody)
			_ = st.SaveAudit(toAudit(auditID, model, res, upBody, respBody, usage, memInfo))
			logTurn("app.request", status, model, session, res, reply, memInfo, usagePromptTokens(usage))
			updateCalibration(cfg.DataRoot, res.TotalTok, usagePromptTokens(usage))
			if status >= 400 {
				obs.Warn("upstream error", map[string]any{"path": "/api/chat", "status": status, "body": excerpt(string(respBody), 300)})
			}
			if strings.TrimSpace(reply) != "" {
				msg, _ := st.AppendChat(session, "assistant", reply)
				events.publish(session, msg)
			}
			maybeDistill(st, client, cfg.DataRoot, s, session)
			w.WriteHeader(status)
			writeJSON(w, map[string]any{
				"model": model, "created_at": time.Now().Format(time.RFC3339),
				"message":     map[string]any{"role": "assistant", "content": reply},
				"done_reason": "stop", "done": true,
			})
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", 500)
			return
		}
		status, full, usage := forwardOllamaStream(client, s.Upstream, s.APIKey, upBody, model, w, flusher)
		rebuilt, _ := json.Marshal(map[string]any{
			"choices":        []map[string]any{{"message": map[string]any{"role": "assistant", "content": full}}},
			"stream_rebuilt": true, "via": "ollama-shim",
		})
		_ = st.SaveAudit(toAudit(auditID, model, res, upBody, rebuilt, usage, memInfo))
		logTurn("app.stream", status, model, session, res, strings.TrimSpace(full), memInfo, usagePromptTokens(usage))
		updateCalibration(cfg.DataRoot, res.TotalTok, usagePromptTokens(usage))
		if strings.TrimSpace(full) != "" {
			msg, _ := st.AppendChat(session, "assistant", full)
			events.publish(session, msg)
		}
		maybeDistill(st, client, cfg.DataRoot, s, session)
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
	obs.Info("tavernlab listening", map[string]any{"addr": addr, "upstream": cfg.Upstream, "data": cfg.DataRoot})
	log.Printf("tavernlab listening on %s (upstream=%s data=%s)", addr, cfg.Upstream, cfg.DataRoot)
	log.Fatal(http.ListenAndServe(addr, obs.Middleware(mux)))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// injectChat renders {{chat_history}} into the chat block from simple turns.
// Deprecated: chat history is now passed as structured turns to the engine,
// but kept for the classic /v1 messages path.
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
	actual := 0
	if usage != nil {
		if v, ok := usage["prompt_tokens"].(float64); ok {
			actual = int(v)
		}
	}
	return store.Audit{
		ID: id, Time: time.Now().Format(time.RFC3339), Model: mName,
		Tier: res.Tier, Overflow: res.Overflow,
		Budget: res.BudgetTok, TotalTok: res.TotalTok, Estimate: res.TotalTok, Actual: actual,
		Blocks:  rows,
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
func chatToTurns(root, session string, msgs []store.ChatMessage) []engine.Message {
	turns := make([]engine.Message, 0, len(msgs))
	for _, m := range msgs {
		role := m.Role
		if role != "user" && role != "assistant" && role != "system" {
			continue // e.g. distilled_memory: recorded in JSONL, injected as a block
		}
		turns = append(turns, engine.Message{
			Role:        role,
			Content:     m.Text,
			ImageTokens: imageTokensForMedia(root, session, m.Images),
		})
	}
	return turns
}

// defaultImageTokens is the conservative per-image fallback when a stored file
// is missing or its dimensions cannot be decoded.
const defaultImageTokens = 1024

// geminiImageTokens approximates Gemini's image tokenisation: <=384px costs a
// flat 258; larger images are cropped into 768x768 tiles, 258 tokens each.
func geminiImageTokens(w, h int) int {
	if w <= 0 || h <= 0 {
		return defaultImageTokens
	}
	if w <= 384 && h <= 384 {
		return 258
	}
	tiles := ((w + 767) / 768) * ((h + 767) / 768)
	if tiles < 1 {
		tiles = 1
	}
	return 258 * tiles
}

// imageTokensForMedia sums the estimated token cost of a message's stored
// images. Only the image header is read (image.DecodeConfig), so this stays
// cheap even when called for every historical turn on each request.
func imageTokensForMedia(root, session string, rel []string) int {
	if len(rel) == 0 {
		return 0
	}
	base := filepath.Join(root, "characters", store.CleanSession(session))
	total := 0
	for _, r := range rel {
		cfg, err := decodeImageConfig(filepath.Join(base, r))
		if err != nil {
			total += defaultImageTokens
			continue
		}
		total += geminiImageTokens(cfg.Width, cfg.Height)
	}
	return total
}

func decodeImageConfig(path string) (image.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	return cfg, err
}

// engineTurns converts legacy {"role","content"} maps to engine turns.
func engineTurns(chat []map[string]string) []engine.Message {
	turns := make([]engine.Message, 0, len(chat))
	for _, m := range chat {
		role := m["role"]
		if role != "user" && role != "assistant" && role != "system" {
			role = "user"
		}
		turns = append(turns, engine.Message{Role: role, Content: m["content"]})
	}
	return turns
}

// rawToTurns converts classic OpenAI messages to engine turns.
func rawToTurns(msgs []map[string]any) []engine.Message {
	turns := make([]engine.Message, 0, len(msgs))
	for _, m := range msgs {
		role, _ := m["role"].(string)
		if role != "user" && role != "assistant" && role != "system" {
			role = "user"
		}
		content, _ := m["content"].(string)
		turns = append(turns, engine.Message{Role: role, Content: content})
	}
	return turns
}

// buildUpMessages converts assembled messages to OpenAI maps and attaches
// image data URLs to the final user message (multimodal content parts).
func buildUpMessages(msgs []engine.Message, imageDataURLs []string) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{"role": m.Role, "content": m.Content})
	}
	if len(imageDataURLs) == 0 {
		return out
	}
	for i := len(out) - 1; i >= 0; i-- {
		if out[i]["role"] != "user" {
			continue
		}
		parts := []map[string]any{{"type": "text", "text": out[i]["content"]}}
		for _, u := range imageDataURLs {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
		}
		out[i]["content"] = parts
		break
	}
	return out
}

// saveImages persists base64/data-URL images under the character package and
// returns package-relative paths plus normalised data URLs for the upstream.
func saveImages(root, session string, imgs []string) (paths, dataURLs []string, err error) {
	if len(imgs) == 0 {
		return nil, nil, nil
	}
	st := store.New(root)
	for _, raw := range imgs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		mime := ""
		b64 := raw
		if strings.HasPrefix(raw, "data:") {
			if i := strings.Index(raw, ";base64,"); i >= 0 {
				mime = raw[5:i]
				b64 = raw[i+8:]
			} else if i := strings.Index(raw, ","); i >= 0 {
				mime = raw[5:i]
				b64 = raw[i+1:]
			}
		}
		data, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if decErr != nil {
			continue
		}
		// No declared mime (raw base64): sniff magic bytes so JPEG/WebP/GIF
		// aren't silently mislabeled as PNG.
		if mime == "" {
			mime = sniffImageMime(data)
		}
		ext := "png"
		if j := strings.Index(mime, "/"); j >= 0 {
			ext = mime[j+1:]
		}
		if ext == "jpeg" {
			ext = "jpg"
		}
		p, werr := st.SaveMedia(session, ext, data)
		if werr != nil {
			continue
		}
		paths = append(paths, p)
		dataURLs = append(dataURLs, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data))
	}
	return paths, dataURLs, nil
}

// sniffImageMime returns an image mime from magic bytes, defaulting to PNG.
func sniffImageMime(data []byte) string {
	if ct := http.DetectContentType(data); strings.HasPrefix(ct, "image/") {
		return ct
	}
	return "image/png"
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
		if m.Role != "user" && m.Role != "assistant" {
			continue // distilled_memory rows are memory, not dialogue
		}
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
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
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

// streamUpBody builds the upstream body, adding stream_options.include_usage
// for streams so OpenAI-compatible providers return token usage in the final
// chunk (needed for estimate self-calibration and overflow detection).
func streamUpBody(model any, msgs []map[string]any, stream bool) []byte {
	body := map[string]any{"model": model, "messages": msgs, "stream": stream}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	b, _ := json.Marshal(body)
	return b
}

// forwardOllamaStream POSTs an SSE chat body upstream and relays it as Ollama
// NDJSON chunks ({"message":{"content":...},"done":false} … {"done":true}).
// Returns upstream status, full assistant text (for audit/JSONL), and usage
// when the provider reports it.
func forwardOllamaStream(client *http.Client, upstream, apiKey string, upBody []byte, model string, w http.ResponseWriter, flusher http.Flusher) (int, string, map[string]any) {
	req, err := http.NewRequest("POST", strings.TrimRight(upstream, "/")+"/v1/chat/completions", bytes.NewReader(upBody))
	if err != nil {
		return 500, "", nil
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 502, "", nil
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
				"message":     map[string]any{"role": "assistant", "content": ""},
				"done_reason": "stop", "done": true,
			})
		}
		_, _ = w.Write(append(line, '\n'))
		flusher.Flush()
	}
	var full strings.Builder
	var usage map[string]any
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
			Usage map[string]any `json:"usage"`
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
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	emit("", true)
	return resp.StatusCode, full.String(), usage
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
func lastUserText(chat []engine.Message) string {
	for i := len(chat) - 1; i >= 0; i-- {
		if chat[i].Role == "user" && strings.TrimSpace(chat[i].Content) != "" {
			return chat[i].Content
		}
	}
	return ""
}

func mcpTimeout(s settings.Settings) time.Duration {
	seconds := s.MCPTimeout
	if seconds <= 0 {
		seconds = 120
	}
	return time.Duration(seconds) * time.Second
}

func searchMCP(ctx context.Context, s settings.Settings, query string) ([]memory.Hit, error) {
	timeout := mcpTimeout(s)
	searchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hits, err := mcp.NewWithTimeout(firstNonEmpty(s.MCPURL, "http://192.168.10.2:8199"), timeout).
		Search(searchCtx, query, s.MCPCollection, s.MCPTopK, s.MCPThreshold)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("mcp search timeout (%s): %w", timeout, err)
	}
	return hits, err
}

// resolveMCP fills enabled mcp-source blocks by searching rag-mcp-server with
// the latest user utterance. Fail-open: errors are recorded, chat continues.
// Returns an audit-friendly summary (also served by /api/assemble preview).
func resolveMCP(ctx context.Context, s settings.Settings, blocks []engine.Block, chat []engine.Message) map[string]any {
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
	hits, err := searchMCP(ctx, s, query)
	if err != nil {
		info["error"] = err.Error()
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

// logTurn emits one structured line per generation so the Logs page shows
// exactly what happened even when the client gets nothing.
func logTurn(kind string, status int, model any, session string, res engine.AssembleResult, reply string, mem map[string]any, actual int) {
	fields := map[string]any{
		"kind": kind, "status": status, "model": fmt.Sprint(model), "session": session,
		"tier": res.Tier, "tokens": res.TotalTok, "overflow": res.Overflow,
		"reply_len": len(reply),
	}
	if actual > 0 {
		fields["actual"] = actual
	}
	if mem != nil {
		fields["mcp_hits"] = mem["hits"]
		if e, ok := mem["error"]; ok {
			fields["mcp_error"] = e
		}
	}
	lvl := obs.LevelInfo
	if status >= 400 || reply == "" {
		lvl = obs.LevelWarn
	}
	// Real prompt exceeded the reserved input budget (now that images are
	// counted and the estimate self-calibrates, this is a genuine overflow).
	if res.Overflow || (actual > 0 && actual > res.BudgetTok) {
		lvl = obs.LevelWarn
		fields["over_budget"] = true
	}
	obs.Log(lvl, "turn", fields)
}

var (
	distillMu   sync.Mutex
	distillBusy = map[string]bool{}
	events      = newHub()
)

// maybeDistill fires a background distillation once enough fresh user turns
// have accumulated. Never blocks the chat path.
func maybeDistill(st *store.Store, client *http.Client, root string, s settings.Settings, session string) {
	if !s.DistillEnabled {
		return
	}
	interval := s.DistillInterval
	if interval <= 0 {
		interval = 8
	}
	all, err := st.LoadAll(session)
	if err != nil {
		return
	}
	meta := distill.LoadMeta(root, session)
	if meta.LastIndex > len(all) {
		meta.LastIndex = 0
	}
	turns := 0
	for _, m := range all[meta.LastIndex:] {
		if m.Role == "user" {
			turns++
		}
	}
	if turns < interval {
		return
	}
	distillMu.Lock()
	busy := distillBusy[session]
	if !busy {
		distillBusy[session] = true
	}
	distillMu.Unlock()
	if busy {
		return
	}
	go func() {
		defer func() {
			distillMu.Lock()
			delete(distillBusy, session)
			distillMu.Unlock()
		}()
		if _, err := runDistill(context.Background(), st, client, root, s, session, false); err != nil {
			obs.Warn("distill failed", map[string]any{"session": session, "error": err.Error()})
		}
	}()
}

// runDistill performs one distillation pass and records the result.
func runDistill(ctx context.Context, st *store.Store, client *http.Client, root string, s settings.Settings, session string, force bool) (string, error) {
	interval := s.DistillInterval
	if interval <= 0 {
		interval = 8
	}
	maxChars := s.DistillMaxChars
	if maxChars <= 0 {
		maxChars = 4000
	}
	retainDays := s.DistillRetainDays
	if retainDays <= 0 {
		retainDays = 3
	}
	all, err := st.LoadAll(session)
	if err != nil {
		return "", err
	}
	meta := distill.LoadMeta(root, session)
	if meta.LastIndex > len(all) {
		meta.LastIndex = 0
	}
	fresh := all[meta.LastIndex:]
	timeline := distill.FormatTimeline(session, s.UserName, fresh)
	// A forced run re-distills the tail even when the cursor is already at the
	// end (e.g. only distilled_memory rows are new), so "立即蒸馏" never no-ops.
	if timeline == "" && force && len(all) > 0 {
		start := len(all) - interval*2
		if start < 0 {
			start = 0
		}
		fresh = all[start:]
		timeline = distill.FormatTimeline(session, s.UserName, fresh)
	}
	if timeline == "" {
		return "", fmt.Errorf("没有可蒸馏的新消息")
	}
	prev := distill.Load(root, session)
	promptTpl := s.DistillPrompt
	if promptTpl == "" {
		promptTpl = distill.DefaultPrompt
	}
	prompt := applyMacros(promptTpl, time.Now(), firstNonEmpty(s.UserName, "user"))
	prompt = strings.ReplaceAll(prompt, "{{maxchars}}", fmt.Sprint(maxChars))
	prompt = strings.ReplaceAll(prompt, "{{retain_days}}", strconv.Itoa(retainDays))
	prompt = strings.ReplaceAll(prompt, "{{words}}", fmt.Sprint(maxChars))
	model := firstNonEmpty(s.DistillModel, "auto-gemini")
	upBody, _ := json.Marshal(map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]any{
			{"role": "system", "content": prompt},
			{"role": "user", "content": distill.BuildUser(prev, timeline)},
		},
	})
	status, respBody, _ := proxy.Forward(client, s.Upstream, s.APIKey, upBody, false, nil, nil)
	if status >= 400 {
		return "", fmt.Errorf("上游 HTTP %d: %s", status, excerpt(string(respBody), 200))
	}
	sheet := strings.TrimSpace(replyText(respBody))
	if sheet == "" {
		return "", fmt.Errorf("上游返回空事实表")
	}
	if !distill.IsValid(sheet) {
		return "", fmt.Errorf("蒸馏输出无法解析（保留旧事实表）")
	}
	// Code-side retention guard: restore anything the model dropped, then trim
	// by whole oldest days but never below retainDays. The LLM never deletes.
	final := distill.EnforceWindow(distill.Merge(prev, sheet), maxChars, retainDays)
	if err := distill.Save(root, session, final); err != nil {
		return "", err
	}
	meta.LastRun = time.Now().Format(time.RFC3339)
	meta.LastIndex = len(all)
	meta.Runs++
	meta.UserTurns = 0
	meta.LastStatus = "ok"
	_ = distill.SaveMeta(root, session, meta)
	// Record the sheet itself in the chat JSONL as its own role so the
	// injected memory is auditable alongside the conversation.
	if msg, err := st.AppendChat(session, "distilled_memory", final); err == nil {
		events.publish(session, msg)
	}
	obs.Info("distill ok", map[string]any{
		"session": session, "chars": len(final), "days": distill.DayCount(final),
		"retain_days": retainDays, "runs": meta.Runs, "model": model,
	})
	return final, nil
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
