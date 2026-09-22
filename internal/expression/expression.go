// Package expression routes an AI reply to an avatar expression.
//
// Design: rules first (talk/serious are tones, not emotions — the
// reranker scores them flat ~0.001 across all labels, verified
// 2026-09-22 on 11437 qwen3-reranker-0.6b), then a local reranker
// zero-shot classify over joy/sad/angry/laugh, then optional LLM
// fallback. All scores go to audit for threshold tuning.
package expression

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Labels in priority order. talk/serious are RULE-based, the other
// four go through the reranker.
const (
	Joy     = "joy"
	Sad     = "sad"
	Talk    = "talk"
	Serious = "serious"
	Angry   = "angry"
	Laugh   = "laugh"
)

// Doc descriptions (ENGLISH — reranker separates EN docs far better
// than Chinese ones, verified: short EN docs gave 0.94 vs 0.0008).
var docs = map[string]string{
	Joy:   "This text expresses joy, happiness and cheerful warmth",
	Sad:   "This text expresses sadness, sorrow, crying and feeling down",
	Angry: "This text expresses anger, rage, fury and irritation",
	Laugh: "This text expresses loud laughter and hilarious amusement",
}

var docOrder = []string{Joy, Sad, Angry, Laugh}

// Tunables (from 2026-09-22 probe; tune with real Leer replies).
var (
	ScoreFloor = 0.0015 // top1 below this -> fallback
	MinRatio   = 1.5    // top1/top2 below this -> fallback
)

// Result of one classification.
type Result struct {
	Label    string             `json:"label"`
	Scores   map[string]float64 `json:"scores"`
	Fallback bool               `json:"fallback"`
	Reason   string             `json:"reason"`
}

// ruleClassify handles talk/serious without a model call.
func ruleClassify(text string) (string, bool) {
	t := text
	// serious: imperative / command tone markers
	if strings.Contains(t, "立刻") || strings.Contains(t, "命令") ||
		strings.Contains(t, "听我说") || strings.Contains(t, "不许") {
		return Serious, true
	}
	// talk: gentle everyday chat, questions, care-taking — but only
	// when no strong emotion words present.
	if strings.Contains(t, "好不好") || strings.Contains(t, "亲爱的") ||
		strings.Contains(t, "?") || strings.Contains(t, "？") {
		for _, w := range []string{"哈哈", "笑", "难过", "气死", "坑爹", "哭"} {
			if strings.Contains(t, w) {
				return "", false
			}
		}
		return Talk, true
	}
	return "", false
}

// rerankCall hits a llama.cpp /v1/rerank endpoint.
func rerankCall(client *http.Client, baseURL, query string) (map[string]float64, error) {
	documents := make([]string, 0, len(docOrder))
	for _, l := range docOrder {
		documents = append(documents, docs[l])
	}
	body, _ := json.Marshal(map[string]any{
		"query": query, "top_n": len(documents), "documents": documents,
	})
	req, err := http.NewRequest("POST", strings.TrimRight(baseURL, "/")+"/v1/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	scores := map[string]float64{}
	for _, r := range out.Results {
		if r.Index >= 0 && r.Index < len(docOrder) {
			scores[docOrder[r.Index]] = r.Score
		}
	}
	return scores, nil
}

// Classify routes text -> expression label.
func Classify(client *http.Client, baseURL, text string) Result {
	if label, ok := ruleClassify(text); ok {
		return Result{Label: label, Scores: map[string]float64{}, Reason: "rule"}
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11437"
	}
	scores, err := rerankCall(client, baseURL, text)
	if err != nil {
		return Result{Label: "", Scores: scores, Fallback: true, Reason: "rerank error: " + err.Error()}
	}
	type kv struct {
		k string
		v float64
	}
	var sorted []kv
	for k, v := range scores {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
	if len(sorted) == 0 {
		return Result{Label: "", Scores: scores, Fallback: true, Reason: "empty scores"}
	}
	top1 := sorted[0]
	ratio := 999.0
	if len(sorted) > 1 && sorted[1].v > 0 {
		ratio = top1.v / sorted[1].v
	}
	if top1.v < ScoreFloor {
		return Result{Label: "", Scores: scores, Fallback: true, Reason: "below floor"}
	}
	if ratio < MinRatio {
		return Result{Label: "", Scores: scores, Fallback: true, Reason: "low margin"}
	}
	return Result{Label: top1.k, Scores: scores, Reason: "rerank"}
}
