// Command leer-chat is the TavernLab Go thin core (Plan v2 P0).
//
//   - Serves embedded vanilla-JS frontend (llama.cpp-style UX, rewritten).
//   - Prompt Engine: blocks -> Context Budget Engine -> raw -> one-api.
//   - Audit-first: every generation saved to data/audit/<id>.json.
//   - Portable: all state under data/ (rclone --copy-links friendly).
package main

import (
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
	"strings"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/expression"
	"github.com/RoyChong5053/TavernLab/internal/proxy"
	"github.com/RoyChong5053/TavernLab/internal/store"
)

//go:embed web
var webFS embed.FS

type Config struct {
	Port          int
	DataRoot      string
	Upstream      string // one-api base URL
	APIKey        string
	DefaultBlocks []engine.Block
	Ctx           engine.ContextConfig
}

func main() {
	port := flag.Int("port", 8080, "listen port")
	data := flag.String("data", "data", "data root (rclone this dir)")
	upstream := flag.String("upstream", "http://127.0.0.1:3000", "one-api base URL")
	flag.Parse()

	cfg := Config{
		Port: *port, DataRoot: *data, Upstream: *upstream,
		APIKey:        os.Getenv("ONEAPI_KEY"),
		DefaultBlocks: engine.DefaultBlocks(),
		Ctx:           engine.DefaultConfig(),
	}
	st := store.New(cfg.DataRoot)
	client := &http.Client{Timeout: 10 * time.Minute}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "time": time.Now().Format(time.RFC3339), "upstream": cfg.Upstream})
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
	mux.HandleFunc("/api/assemble", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Blocks  []engine.Block       `json:"blocks"`
			Context engine.ContextConfig `json:"context"`
			Chat    []map[string]string  `json:"chat"`
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
		blocks = injectChat(blocks, in.Chat)
		res := engine.Assemble(blocks, ctx)
		writeJSON(w, res)
	})

	// Chat completions: assemble then forward to one-api, save audit.
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in struct {
			Model    any                `json:"model"`
			Messages []map[string]any   `json:"messages"`
			Stream   bool               `json:"stream"`
			Session  string             `json:"session"`
			Blocks   []engine.Block     `json:"blocks"`
			Context  engine.ContextConfig `json:"context"`
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
		// If caller passed raw messages (classic path), wrap as chat block content.
		if len(in.Messages) > 0 {
			blocks = injectRawMessages(blocks, in.Messages)
		}
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

		if in.Stream {
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", 500)
				return
			}
			status, rebuilt, _ := proxy.Forward(client, cfg.Upstream, cfg.APIKey, upBody, true, w, flusher)
			_ = status
			_ = st.SaveAudit(toAudit(auditID, model, res, upBody, rebuilt, nil))
			return
		}
		status, respBody, usage := proxy.Forward(client, cfg.Upstream, cfg.APIKey, upBody, false, nil, nil)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
		_ = st.SaveAudit(toAudit(auditID, model, res, upBody, respBody, usage))
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
		if len(parts) == 2 && parts[1] == "avatar" && r.Method == "PUT" {
			// multipart file=...  or raw body; save as avatar.webp (browser
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
		writeJSON(w, map[string]any{"name": name, "avatar_url": avatarURL, "expressions": exprs})
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

	// Static frontend.
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	log.Printf("leer-chat listening on %s (upstream=%s data=%s)", addr, cfg.Upstream, cfg.DataRoot)
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

func toAudit(id string, model any, res engine.AssembleResult, upBody, respBody []byte, usage map[string]any) store.Audit {
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
	}
}
