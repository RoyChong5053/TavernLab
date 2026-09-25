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

// calibration is the persisted EMA state for EstimateTokens, bucketed per
// model because one-api's auto-* groups fan out to different tokenizers
// (Gemini / DeepSeek / ...), so a single global factor swings wildly.
type calibration struct {
	Scale   float64            `json:"scale"`
	Samples int                `json:"samples"`
	Updated string             `json:"updated,omitempty"`
	Models  map[string]float64 `json:"models,omitempty"`
}

const (
	// seedScale is used when no calibration exists: the raw script heuristic
	// over-counts CJK by roughly this factor for Gemini-style tokenizers.
	seedScale  = 0.7
	minScale   = 0.7
	maxScale   = 1.3
	calibAlpha = 0.1
)

var (
	calibMu    sync.Mutex
	calibState = calibration{Scale: seedScale, Models: map[string]float64{}}
)

func calibPath(root string) string { return filepath.Join(root, "calibration.json") }

// loadCalibration applies the persisted factors (or the seed on first run).
func loadCalibration(root string) {
	b, err := os.ReadFile(calibPath(root))
	if err != nil {
		calibMu.Lock()
		calibState = calibration{Scale: seedScale, Models: map[string]float64{}, Updated: time.Now().Format(time.RFC3339)}
		c := calibState
		calibMu.Unlock()
		engine.SetTextScale(seedScale)
		saveCalibrationState(root, c)
		obs.Info("calibration seeded", map[string]any{"scale": seedScale})
		return
	}
	var c calibration
	if json.Unmarshal(b, &c) != nil || c.Scale <= 0 {
		c = calibration{Scale: seedScale}
	}
	if c.Models == nil {
		c.Models = map[string]float64{}
	}
	if c.Scale < minScale || c.Scale > maxScale {
		c.Scale = seedScale
	}
	calibMu.Lock()
	calibState = c
	calibMu.Unlock()
	engine.SetTextScale(c.Scale)
	obs.Info("calibration loaded", map[string]any{"scale": c.Scale, "samples": c.Samples, "models": len(c.Models)})
}

// setActiveModel selects the calibration bucket used by EstimateTokens for the
// duration of one request. TavernLab is a single-user server, so switching the
// engine's global factor here is good enough and keeps EstimateTokens fast.
func setActiveModel(model string) {
	if model == "" {
		model = "default"
	}
	calibMu.Lock()
	scale, ok := calibState.Models[model]
	if !ok {
		scale = calibState.Scale
	}
	calibMu.Unlock()
	if scale <= 0 {
		scale = seedScale
	}
	engine.SetTextScale(scale)
}

// updateCalibration nudges the model's factor toward the observed
// actual/estimate ratio. Out-of-range ratios (prompt caching, provider
// quirks) are ignored; the factor is clamped so it can never under-count
// badly enough to blow the provider context.
func updateCalibration(root, model string, estimate, actual int) {
	if estimate <= 0 || actual <= 0 {
		return
	}
	ratio := float64(actual) / float64(estimate)
	if ratio < 0.5 || ratio > 2.0 {
		return
	}
	if model == "" {
		model = "default"
	}
	calibMu.Lock()
	scale, ok := calibState.Models[model]
	if !ok {
		scale = calibState.Scale
		if scale <= 0 {
			scale = seedScale
		}
	}
	scale = scale*(1-calibAlpha) + ratio*calibAlpha
	if scale < minScale {
		scale = minScale
	}
	if scale > maxScale {
		scale = maxScale
	}
	calibState.Models[model] = scale
	calibState.Scale = scale
	calibState.Samples++
	calibState.Updated = time.Now().Format(time.RFC3339)
	c := calibState
	calibMu.Unlock()

	saveCalibrationState(root, c)
	obs.Debug("calibration updated", map[string]any{
		"model": model, "scale": c.Scale, "ratio": ratio, "estimate": estimate, "actual": actual, "samples": c.Samples,
	})
}

func saveCalibrationState(root string, c calibration) {
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(calibPath(root), b, 0o644)
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
