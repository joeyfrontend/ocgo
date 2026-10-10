package trace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenEmptyDirDisablesTracing(t *testing.T) {
	w, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if w != nil {
		t.Fatal("expected a nil writer for an empty directory")
	}
	// A nil writer must be usable so callers never branch on tracing.
	w.Log("c", KindNote, Record{Text: "ignored"})
	w.LogHeaders("c", KindInbound, "POST", "/v1/responses", http.Header{}, nil)
	if w.Dir() != "" {
		t.Fatal("nil writer should report no directory")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRedactHeadersRemovesCredentials(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-secret-value")
	h.Set("X-Api-Key", "st-secret")
	h.Set("Cookie", "session=abc")
	h.Set("Content-Type", "application/json")
	got := RedactHeaders(h)
	for _, key := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		if got[key] != redacted {
			t.Fatalf("%s = %q, want %q", key, got[key], redacted)
		}
	}
	if got["Content-Type"] != "application/json" {
		t.Fatalf("Content-Type was mangled: %q", got["Content-Type"])
	}
}

func TestWriterScrubsSecretsFromBodies(t *testing.T) {
	dir := t.TempDir()
	const secret = "st-verysecretvalue123"
	w, err := Open(dir, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.LogHeaders("conv", KindUpstream, "POST", "/v1/chat/completions", http.Header{},
		[]byte(`{"note":"`+secret+`","keep":"this"}`))

	recs := readRecords(t, dir, "conv")
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	body := string(recs[0].Body)
	if strings.Contains(body, secret) {
		t.Fatalf("secret leaked into the trace: %s", body)
	}
	if !strings.Contains(body, "this") {
		t.Fatalf("non-secret content was lost: %s", body)
	}
}

func TestWriterShortSecretsAreIgnored(t *testing.T) {
	dir := t.TempDir()
	// A very short "secret" would scrub unrelated text, so it is skipped.
	w, err := Open(dir, "abc")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Log("conv", KindNote, Record{Text: "abcdef"})
	recs := readRecords(t, dir, "conv")
	if recs[0].Text != "abcdef" {
		t.Fatalf("short secret should not be scrubbed, got %q", recs[0].Text)
	}
}

func TestWriterGroupsByConversationAndCountsSequence(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Log("one", KindNote, Record{Text: "a"})
	w.Log("one", KindNote, Record{Text: "b"})
	w.Log("two", KindNote, Record{Text: "c"})

	one := readRecords(t, dir, "one")
	if len(one) != 2 {
		t.Fatalf("conversation one has %d records", len(one))
	}
	if one[0].Seq != 1 || one[1].Seq != 2 {
		t.Fatalf("sequence numbers = %d, %d", one[0].Seq, one[1].Seq)
	}
	if one[0].Conv != "one" || one[0].Kind != KindNote {
		t.Fatalf("record metadata wrong: %+v", one[0])
	}
	if one[0].TS == "" {
		t.Fatal("timestamp missing")
	}
	if got := w.Conversations(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("Conversations() = %v", got)
	}
}

func TestWriterUsesPrivateFilePermissions(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Log("conv", KindNote, Record{Text: "x"})
	info, err := os.Stat(filepath.Join(dir, "conv.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("trace file mode = %o, want 600", perm)
	}
}

func TestWriterSanitisesConversationName(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Log("../../escape", KindNote, Record{Text: "x"})
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d files", len(entries))
	}
	if strings.Contains(entries[0].Name(), "/") || strings.Contains(entries[0].Name(), "..") {
		t.Fatalf("conversation name escaped the directory: %s", entries[0].Name())
	}
}

func readRecords(t *testing.T, dir, conv string) []Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, conv+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}
