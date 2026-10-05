// Package vectorize builds the auditable Markdown files that feed the RAG
// collection. One builder serves both tracks:
//
//   - auto (incremental): every N user turns, raw fresh messages + this run's
//     distilled delta + movement/location evidence are written to
//     data/characters/<char>/distilled-tmp/ and the file content is appended
//     to the recall collection via store_memory;
//   - full (insurance): the same sections over the whole chat.jsonl
//     (+ archives) for the settings-page export; used to rebuild the
//     collection if distilled output ever corrupts it.
//
// File content is exactly what gets vectorized (meta header included, kept to
// 3 lines so chunk noise is negligible). Parse failures are still written to
// disk with a status line so the user can see them; the caller decides
// whether to vectorize.
package vectorize

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RoyChong5053/TavernLab/internal/store"
)

// TmpDir returns the per-character audit directory for auto-exported files.
func TmpDir(root, char string) string {
	return filepath.Join(root, "characters", store.CleanSession(char), "distilled-tmp")
}

// IncrementalName names an auto file by time + message range so it is
// traceable back to the chat rows it covers. Lexical order == chrono order.
func IncrementalName(now time.Time, start, end int) string {
	return fmt.Sprintf("%s_msgs%d-%d.md", now.Format("20060102-150405"), start, end)
}

// speakerLabel mirrors the timeline attribution: the assistant speaks as the
// character, the user as the configured user name.
func speakerLabel(role, session, userName string) string {
	if role == "assistant" {
		return session
	}
	return userName
}

// span formats an RFC3339 timestamp like the timeline export.
func span(t string) string {
	if ts, err := time.Parse(time.RFC3339, t); err == nil {
		return ts.Format("2006-01-02 15:04")
	}
	return t
}

// rawSection renders user/assistant messages in timeline shape. Other roles
// (distilled_memory etc.) are memory, not dialogue, and are skipped here.
func rawSection(session, userName string, msgs []store.ChatMessage) string {
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		sb.WriteString("**" + speakerLabel(m.Role, session, userName) + "** [" + span(m.Time) + "]: " + text + "\n")
	}
	return strings.TrimSpace(sb.String())
}

// BuildIncremental renders one auto file: raw fresh messages + this run's
// distilled delta + movement/location evidence. deltaStatus is "ok" or
// "parse-failed"; the delta is included verbatim either way.
func BuildIncremental(session, userName string, msgs []store.ChatMessage, msgStart, msgEnd int, delta, deltaStatus, movement, location string, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("# vectorize " + session + " " + now.Format("2006-01-02 15:04") + "\n")
	sb.WriteString("session: " + session + " | msgs: " + fmt.Sprintf("%d-%d", msgStart, msgEnd) + " | distill: " + deltaStatus + "\n\n")
	sb.WriteString("## raw\n")
	if raw := rawSection(session, userName, msgs); raw != "" {
		sb.WriteString(raw + "\n")
	} else {
		sb.WriteString("(no dialogue rows in range)\n")
	}
	sb.WriteString("\n## distilled\n")
	if d := strings.TrimSpace(delta); d != "" {
		sb.WriteString(d + "\n")
	} else {
		sb.WriteString("(empty delta)\n")
	}
	sb.WriteString("\n## movement\n")
	hasMovement := false
	if m := strings.TrimSpace(movement); m != "" {
		sb.WriteString(m + "\n")
		hasMovement = true
	}
	if l := strings.TrimSpace(location); l != "" {
		sb.WriteString("[LOC] " + l + "\n")
		hasMovement = true
	}
	if !hasMovement {
		sb.WriteString("(no movement/location)\n")
	}
	return sb.String()
}

// BuildFull renders the insurance export over the whole lifetime: full raw
// timeline + current distilled sheet + latest location. archives indicates
// whether archived floors are included.
func BuildFull(session, userName string, msgs []store.ChatMessage, sheet, location string, archives bool) string {
	now := time.Now()
	var sb strings.Builder
	sb.WriteString("# " + session + " full export " + now.Format("2006-01-02 15:04") + "\n")
	sb.WriteString("session: " + session + " | msgs: " + fmt.Sprintf("%d", len(msgs)) + " | archives: " + fmt.Sprintf("%v", archives) + "\n\n")
	sb.WriteString("## raw\n")
	if raw := rawSection(session, userName, msgs); raw != "" {
		sb.WriteString(raw + "\n")
	} else {
		sb.WriteString("(empty)\n")
	}
	sb.WriteString("\n## distilled\n")
	if s := strings.TrimSpace(sheet); s != "" {
		sb.WriteString(s + "\n")
	} else {
		sb.WriteString("(no distilled record yet)\n")
	}
	sb.WriteString("\n## movement\n")
	if l := strings.TrimSpace(location); l != "" {
		sb.WriteString("[LOC] " + l + "\n")
	} else {
		sb.WriteString("(no movement/location)\n")
	}
	return sb.String()
}

// WriteTmp writes content into the character's distilled-tmp dir under name
// and returns the full path.
func WriteTmp(root, char, name, content string) (string, error) {
	dir := TmpDir(root, char)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// PruneTmp keeps the newest keep files in distilled-tmp, deleting the oldest.
// keep <= 0 keeps everything. Names sort chronologically by construction.
func PruneTmp(root, char string, keep int) error {
	if keep <= 0 {
		return nil
	}
	dir := TmpDir(root, char)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, n))
	}
	return nil
}
