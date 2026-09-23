package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/engine"
	"github.com/RoyChong5053/TavernLab/internal/obs"
)

// calibration is the persisted EMA state for EstimateTokens. It lives in
// data/ (git-ignored, rclone-friendly) and is updated after every upstream
// call that reports usage.prompt_tokens.
type calibration struct {
	Scale   float64 `json:"scale"`
	Samples int     `json:"samples"`
	Updated string  `json:"updated,omitempty"`
}

// seedScale is used when no calibration file exists yet: the raw script
// heuristic over-counts CJK by roughly this factor for Gemini-style
// tokenizers. EMA refines it from real usage.
const seedScale = 0.7

var (
	calibMu    sync.Mutex
	calibState = calibration{Scale: 1.0}
)

func calibPath(root string) string { return filepath.Join(root, "calibration.json") }

// loadCalibration applies the persisted factor (or the seed on first run).
func loadCalibration(root string) {
	b, err := os.ReadFile(calibPath(root))
	if err != nil {
		engine.SetTextScale(seedScale)
		calibState = calibration{Scale: engine.TextScale(), Updated: time.Now().Format(time.RFC3339)}
		if out, mErr := json.MarshalIndent(calibState, "", "  "); mErr == nil {
			_ = os.WriteFile(calibPath(root), out, 0o644)
		}
		obs.Info("calibration seeded", map[string]any{"scale": engine.TextScale()})
		return
	}
	var c calibration
	if json.Unmarshal(b, &c) != nil || c.Scale <= 0 {
		engine.SetTextScale(seedScale)
		return
	}
	engine.SetTextScale(c.Scale)
	calibMu.Lock()
	calibState = c
	calibMu.Unlock()
	obs.Info("calibration loaded", map[string]any{"scale": engine.TextScale(), "samples": c.Samples})
}

// updateCalibration nudges the factor toward the observed actual/estimate
// ratio. Out-of-range ratios (prompt caching, provider quirks) are ignored.
func updateCalibration(root string, estimate, actual int) {
	if estimate <= 0 || actual <= 0 {
		return
	}
	ratio := float64(actual) / float64(estimate)
	if ratio < 0.3 || ratio > 1.5 {
		return
	}
	calibMu.Lock()
	scale := engine.TextScale()
	const alpha = 0.2
	scale = scale*(1-alpha) + ratio*alpha
	engine.SetTextScale(scale)
	calibState = calibration{
		Scale:   engine.TextScale(),
		Samples: calibState.Samples + 1,
		Updated: time.Now().Format(time.RFC3339),
	}
	c := calibState
	calibMu.Unlock()

	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(calibPath(root), b, 0o644)
	obs.Debug("calibration updated", map[string]any{
		"scale": c.Scale, "ratio": ratio, "estimate": estimate, "actual": actual, "samples": c.Samples,
	})
}

// usagePromptTokens pulls usage.prompt_tokens out of an upstream usage map.
func usagePromptTokens(usage map[string]any) int {
	if usage == nil {
		return 0
	}
	if v, ok := usage["prompt_tokens"].(float64); ok {
		return int(v)
	}
	return 0
}
