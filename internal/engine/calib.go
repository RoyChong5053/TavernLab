package engine

import (
	"math"
	"sync/atomic"
)

// textScale is an EMA calibration factor applied on top of the raw script
// heuristic so estimates converge to the upstream tokenizer. 1.0 = raw.
//
// The heuristic (EstimateTokens) over-counts CJK for Gemini-style
// SentencePiece tokenizers (~1 token/char vs a measured ~0.6-0.7). Rather
// than hard-code a per-model constant we record upstream usage.prompt_tokens
// against our estimate and let the factor drift to the observed ratio
// (see main.updateCalibration).
var textScaleBits atomic.Uint64

func init() { textScaleBits.Store(math.Float64bits(1.0)) }

// SetTextScale sets the calibration factor, clamped to a sane [0.3, 1.5].
// Non-positive / NaN input is ignored.
func SetTextScale(f float64) {
	if math.IsNaN(f) || f <= 0 {
		return
	}
	if f < 0.3 {
		f = 0.3
	}
	if f > 1.5 {
		f = 1.5
	}
	textScaleBits.Store(math.Float64bits(f))
}

// TextScale returns the current calibration factor.
func TextScale() float64 { return math.Float64frombits(textScaleBits.Load()) }

// ScaleTokens applies the calibration factor to a raw token count.
func ScaleTokens(raw int) int {
	if raw <= 0 {
		return raw
	}
	n := int(math.Round(float64(raw) * TextScale()))
	if n < 1 {
		n = 1
	}
	return n
}
