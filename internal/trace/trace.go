// Package trace records proxy conversations as JSONL so that Codex/Claude
// request shapes and upstream stream shapes can be analysed offline.
//
// Traces are written one file per conversation, at 0600. Secrets are removed
// before anything is written: configured secret values are replaced with
// "<redacted>" wherever they appear, and credential-bearing headers are
// replaced wholesale.
package trace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record kinds.
const (
	KindInbound  = "inbound_request"
	KindUpstream = "upstream_request"
	KindChunk    = "upstream_chunk"
	KindEvent    = "outbound_event"
	KindError    = "error"
	KindNote     = "note"
)

const redacted = "<redacted>"

// sensitiveHeaders are dropped entirely rather than redacted: their names alone
// are enough to be useful in a trace, and their values are never needed.
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"x-api-key":           true,
	"api-key":             true,
	"cookie":              true,
	"set-cookie":          true,
}

// Record is one JSONL line.
type Record struct {
	TS      string            `json:"ts"`
	Conv    string            `json:"conv"`
	Seq     int               `json:"seq"`
	Kind    string            `json:"kind"`
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	Text    string            `json:"text,omitempty"`
}

// Writer appends Records to per-conversation JSONL files. The zero value and a
// nil *Writer are both usable: every method is a no-op, so callers never need
// to branch on whether tracing is enabled.
type Writer struct {
	mu     sync.Mutex
	dir    string
	seq    map[string]int
	files  map[string]*os.File
	secret []string
}

// Open prepares dir and returns a Writer. An empty dir returns (nil, nil) so
// callers can treat "tracing disabled" as a nil Writer.
func Open(dir string, secrets ...string) (*Writer, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("trace dir: %w", err)
	}
	w := &Writer{dir: dir, seq: map[string]int{}, files: map[string]*os.File{}}
	for _, s := range secrets {
		if len(strings.TrimSpace(s)) >= 8 {
			w.secret = append(w.secret, s)
		}
	}
	return w, nil
}

// Dir reports the trace directory, or "" when tracing is disabled.
func (w *Writer) Dir() string {
	if w == nil {
		return ""
	}
	return w.dir
}

// Log appends one record. conv groups a conversation into a single file; an
// empty conv is recorded as "unknown".
func (w *Writer) Log(conv, kind string, r Record) {
	if w == nil {
		return
	}
	if strings.TrimSpace(conv) == "" {
		conv = "unknown"
	}
	conv = safeName(conv)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq[conv]++
	r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	r.Conv = conv
	r.Seq = w.seq[conv]
	r.Kind = kind
	r.Body = w.scrubJSON(r.Body)
	r.Text = w.scrubText(r.Text)
	for k, v := range r.Headers {
		r.Headers[k] = w.scrubText(v)
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	f, err := w.file(conv)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Sync()
}

// LogHeaders records an inbound or upstream request with redacted headers.
func (w *Writer) LogHeaders(conv, kind, method, path string, h http.Header, body []byte) {
	if w == nil {
		return
	}
	w.Log(conv, kind, Record{Method: method, Path: path, Headers: RedactHeaders(h), Body: body})
}

// RedactHeaders renders headers for tracing, replacing credential values.
func RedactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vals := range h {
		if sensitiveHeaders[strings.ToLower(k)] {
			out[k] = redacted
			continue
		}
		out[k] = strings.Join(vals, ", ")
	}
	return out
}

// Close flushes and closes every open conversation file.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var first error
	for _, f := range w.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	w.files = map[string]*os.File{}
	return first
}

// Conversations lists the conversation ids recorded so far, sorted.
func (w *Writer) Conversations() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.seq))
	for id := range w.seq {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (w *Writer) file(conv string) (*os.File, error) {
	if f, ok := w.files[conv]; ok {
		return f, nil
	}
	path := filepath.Join(w.dir, conv+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	w.files[conv] = f
	return f, nil
}

func (w *Writer) scrubText(s string) string {
	for _, secret := range w.secret {
		s = strings.ReplaceAll(s, secret, redacted)
	}
	return s
}

func (w *Writer) scrubJSON(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return b
	}
	return json.RawMessage(w.scrubText(string(b)))
}

var (
	unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)
	dotRuns    = regexp.MustCompile(`\.{2,}`)
)

// safeName turns a client-supplied conversation id into a file name that
// cannot traverse out of the trace directory.
func safeName(s string) string {
	s = unsafeName.ReplaceAllString(s, "_")
	s = dotRuns.ReplaceAllString(s, ".")
	s = strings.TrimLeft(s, ".")
	if s == "" {
		s = "unknown"
	}
	if len(s) > 96 {
		s = s[:96]
	}
	return s
}
