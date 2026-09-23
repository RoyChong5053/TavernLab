// Package obs is TavernLab's tiny runtime-observability layer: a structured
// log with an in-memory ring buffer (for the /api/logs page) plus HTTP
// middleware that records method/path/status/duration and recovers panics.
//
// No external deps: the whole data/ dir stays rclone-friendly.
package obs

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Level is a log severity.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Entry is one log record, JSON-serializable for /api/logs.
type Entry struct {
	Seq    int64          `json:"seq"`
	Time   string         `json:"time"`
	Level  Level          `json:"level"`
	Msg    string         `json:"msg"`
	Fields map[string]any `json:"fields,omitempty"`
}

const ringCap = 1000

var (
	mu       sync.Mutex
	seq      int64
	ring     = make([]Entry, 0, ringCap)
	subs     = map[int]chan Entry{}
	subSeq   int
	minLevel = LevelDebug
)

// SetLevel sets the minimum level emitted to stdout (ring keeps everything).
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	minLevel = l
}

// GetLevel returns the current stdout threshold.
func GetLevel() Level {
	mu.Lock()
	defer mu.Unlock()
	return minLevel
}

func levelRank(l Level) int {
	switch l {
	case LevelDebug:
		return 0
	case LevelInfo:
		return 1
	case LevelWarn:
		return 2
	case LevelError:
		return 3
	}
	return 1
}

// Log records one entry. Fields may be nil.
func Log(l Level, msg string, fields map[string]any) {
	mu.Lock()
	seq++
	e := Entry{Seq: seq, Time: time.Now().Format(time.RFC3339), Level: l, Msg: msg, Fields: fields}
	ring = append(ring, e)
	if len(ring) > ringCap {
		ring = ring[len(ring)-ringCap:]
	}
	emit := levelRank(l) >= levelRank(minLevel)
	chans := make([]chan Entry, 0, len(subs))
	for _, ch := range subs {
		chans = append(chans, ch)
	}
	mu.Unlock()

	if emit {
		if fields == nil {
			log.Printf("[%s] %s", l, msg)
		} else {
			b, _ := json.Marshal(fields)
			log.Printf("[%s] %s %s", l, msg, b)
		}
	}
	for _, ch := range chans {
		select {
		case ch <- e:
		default:
		}
	}
}

// Debug/Info/Warn/Error are convenience wrappers.
func Debug(msg string, f map[string]any) { Log(LevelDebug, msg, f) }
func Info(msg string, f map[string]any)  { Log(LevelInfo, msg, f) }
func Warn(msg string, f map[string]any)  { Log(LevelWarn, msg, f) }
func Error(msg string, f map[string]any) { Log(LevelError, msg, f) }

// Recent returns the newest n entries (newest last).
func Recent(n int) []Entry {
	mu.Lock()
	defer mu.Unlock()
	if n <= 0 || n > len(ring) {
		n = len(ring)
	}
	out := make([]Entry, n)
	copy(out, ring[len(ring)-n:])
	return out
}

// Since returns every entry with Seq > after.
func Since(after int64) []Entry {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Entry, 0, 64)
	for _, e := range ring {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out
}

// Subscribe returns a channel of live entries plus an unsubscribe func.
func Subscribe() (chan Entry, func()) {
	mu.Lock()
	defer mu.Unlock()
	subSeq++
	id := subSeq
	ch := make(chan Entry, 64)
	subs[id] = ch
	return ch, func() {
		mu.Lock()
		defer mu.Unlock()
		if c, ok := subs[id]; ok {
			delete(subs, id)
			close(c)
		}
	}
}

// statusRecorder captures status + byte count while preserving Flusher
// (SSE needs it) and Hijacker.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Middleware logs every request and recovers panics.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				Error("panic", map[string]any{"path": r.URL.Path, "panic": fmt.Sprint(p)})
				if rec.status == 0 {
					http.Error(rec, "internal error", 500)
				}
			}
			status := rec.status
			if status == 0 {
				status = 200
			}
			lvl := LevelInfo
			if status >= 500 {
				lvl = LevelError
			} else if status >= 400 {
				lvl = LevelWarn
			}
			Log(lvl, "http", map[string]any{
				"method": r.Method,
				"path":   r.URL.Path,
				"query":  r.URL.RawQuery,
				"status": status,
				"ms":     time.Since(start).Milliseconds(),
				"bytes":  rec.bytes,
				"remote": r.RemoteAddr,
			})
		}()
		next.ServeHTTP(rec, r)
	})
}

// SortNewestFirst is a helper for callers that want entries ordered newest-first.
func SortNewestFirst(es []Entry) {
	sort.Slice(es, func(i, j int) bool { return es[i].Seq > es[j].Seq })
}
