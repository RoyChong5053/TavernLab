package distill

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRunAuditRoundTrip(t *testing.T) {
	root := t.TempDir()
	a := RunAudit{
		ID: "20261026-063858.661", Session: "Leer", Model: "auto-agent", MaxTokens: 8192,
		Request: json.RawMessage(`{"model":"auto-agent","messages":[{"role":"system","content":"hi"}]}`),
		Outcome: "ok", Delta: "[LOG]\n[26-10-2026 06:38] x",
		Attempts: []Attempt{{N: 1, Finish: "stop", Completion: 12, DeltaChars: 30}},
	}
	if err := SaveRunAudit(root, a); err != nil {
		t.Fatal(err)
	}
	ids, err := ListRunAudits(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("ids = %v, want [%s]", ids, a.ID)
	}
	// Load accepts the bare id and the full filename suffix.
	got, err := LoadRunAudit(root, a.ID+"-distill")
	if err != nil {
		t.Fatal(err)
	}
	var gotReq, wantReq map[string]any
	_ = json.Unmarshal(got.Request, &gotReq)
	_ = json.Unmarshal(a.Request, &wantReq)
	if got.Session != a.Session || got.Outcome != a.Outcome || !reflect.DeepEqual(gotReq, wantReq) {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestListRunAuditsEmpty(t *testing.T) {
	ids, err := ListRunAudits(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty", ids)
	}
}

func TestLoadRunAuditRejectsTraversal(t *testing.T) {
	if _, err := LoadRunAudit(t.TempDir(), "../settings"); err == nil {
		t.Fatal("expected an error for a traversal id")
	}
}
