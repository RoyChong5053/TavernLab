// Package memory defines the unified MemorySource interface (Plan v2 P2).
//
// Vectra (local/portable) and MCP->Qdrant (high-end, LOQ only) are just
// two backends behind this interface. Distilled memory is a third.
package memory

import "context"

// Hit is one retrieved chunk.
type Hit struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	Score     float64 `json:"score"`
	Rerank    float64 `json:"rerank,omitempty"`
	Source    string  `json:"source,omitempty"`
	Timestamp string  `json:"timestamp,omitempty"`
}

// Source is anything a prompt block can pull context from.
type Source interface {
	Name() string
	Search(ctx context.Context, query string, topK int) ([]Hit, error)
	// Insert/Delete may be unsupported (e.g. read-only Vectra adapter in P0).
	Insert(ctx context.Context, text string) error
	Delete(ctx context.Context, id string) error
}

// Registry routes collection names to backends.
type Registry struct {
	backends map[string]Source
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{backends: map[string]Source{}} }

// Register adds a backend under a name.
func (r *Registry) Register(name string, s Source) { r.backends[name] = s }

// Get returns a backend or nil.
func (r *Registry) Get(name string) Source { return r.backends[name] }
