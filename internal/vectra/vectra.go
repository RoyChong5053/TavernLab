// Package vectra is a READ-ONLY adapter over ST-compatible Vectra data.
//
// WARNING (ChatGPT review, accepted): do NOT reimplement the Vectra writer
// in Go yet. A divergent writer risks index/metadata incompatibility and
// would break the precious `rclone --copy-links device-A -> device-B`
// lossless migration. P0/P1: read existing ST vectra JSON, write path stays
// in ST / a tiny Node helper. Native writer comes only after stability.
package vectra

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RoyChong5053/TavernLab/internal/memory"
)

// Item is a minimal ST-vectra-like record. We tolerate extra fields.
type Item struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Vector    []float32 `json:"vector"`
	Timestamp string    `json:"timestamp,omitempty"`
	Source    string    `json:"source,omitempty"`
}

// Adapter reads a collection dir: data/vectra/<collection>/{index.json,items.json}
// Accepts either a bare []Item file or {"items":[...]}.
type Adapter struct {
	name string
	dir  string
}

// New creates an adapter for data/vectra/<collection>.
func New(name, dataRoot string) *Adapter {
	return &Adapter{name: name, dir: filepath.Join(dataRoot, "vectra", name)}
}

// Name implements memory.Source.
func (a *Adapter) Name() string { return "vectra:" + a.name }

func (a *Adapter) load() ([]Item, error) {
	for _, fn := range []string{"items.json", "index.json"} {
		p := filepath.Join(a.dir, fn)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var arr []Item
		if err := json.Unmarshal(b, &arr); err == nil {
			return arr, nil
		}
		var obj struct {
			Items []Item `json:"items"`
		}
		if err := json.Unmarshal(b, &obj); err == nil {
			return obj.Items, nil
		}
	}
	return nil, nil // empty collection is fine in P0
}

// Search does keyword-overlap ranking (no embedding call in P0).
// P2 will call one-api embedding fan-out then cosine against Item.Vector.
func (a *Adapter) Search(ctx context.Context, query string, topK int) ([]memory.Hit, error) {
	items, err := a.load()
	if err != nil {
		return nil, err
	}
	if topK <= 0 {
		topK = 10
	}
	q := strings.Fields(strings.ToLower(query))
	type scored struct {
		hit memory.Hit
		s   float64
	}
	var out []scored
	for _, it := range items {
		t := strings.ToLower(it.Text)
		s := 0.0
		for _, w := range q {
			if w != "" && strings.Contains(t, w) {
				s++
			}
		}
		if s == 0 && len(items) > 0 {
			continue
		}
		// cosine bonus when both sides have vectors (future path)
		_ = math.Max
		out = append(out, scored{hit: memory.Hit{
			ID: it.ID, Text: it.Text, Score: s, Source: it.Source, Timestamp: it.Timestamp,
		}, s: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].s > out[j].s })
	if len(out) > topK {
		out = out[:topK]
	}
	hits := make([]memory.Hit, 0, len(out))
	for _, s := range out {
		hits = append(hits, s.hit)
	}
	return hits, nil
}

// Insert is disabled on purpose in P0 (see package doc).
func (a *Adapter) Insert(ctx context.Context, text string) error {
	return errReadOnly
}

// Delete is disabled on purpose in P0.
func (a *Adapter) Delete(ctx context.Context, id string) error { return errReadOnly }

type roError string

func (e roError) Error() string { return string(e) }

var errReadOnly roError = "vectra adapter is read-only in P0 (use ST or Node helper for writes)"
