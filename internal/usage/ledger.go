package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is one recorded request.
type Entry struct {
	TS     time.Time `json:"ts"`
	Model  string    `json:"model"`
	Input  int       `json:"input"`
	Cached int       `json:"cached"`
	Output int       `json:"output"`
	Cost   float64   `json:"cost"`
}

// Verdict is the guard's answer for one model at one moment.
type Verdict struct {
	// Allowed is false when a window is exhausted.
	Allowed bool
	// Window is the binding window: the exhausted one, or the fullest.
	Window Window
	// Fraction is the share of that window's allowance already spent.
	Fraction float64
	// Limit and Spent are the dollar figures behind Fraction.
	Limit float64
	Spent float64
	// Warning is set from the warn threshold upwards.
	Warning bool
	// Message explains a refusal, and is empty when allowed.
	Message string
}

// Ledger is the persisted record of spend.
type Ledger struct {
	mu      sync.Mutex
	path    string
	entries []Entry
	// WarnAt is the fraction of a window at which a warning is raised.
	WarnAt float64
}

// retention is how much history is kept. It matches the longest window.
const retention = 30 * 24 * time.Hour

// Open loads a ledger from path. A missing file is not an error: it is the
// first run. An empty path returns an in-memory ledger.
func Open(path string) (*Ledger, error) {
	l := &Ledger{path: path, WarnAt: 0.90}
	if path == "" {
		return l, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, err
	}
	var stored struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(b, &stored); err != nil {
		return nil, fmt.Errorf("usage ledger %s is corrupt: %w", path, err)
	}
	l.entries = prune(stored.Entries, time.Now())
	return l, nil
}

// Path reports the ledger's file path, or "" for an in-memory ledger.
func (l *Ledger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Record appends an entry and persists the ledger.
func (l *Ledger) Record(e Entry) error {
	if l == nil {
		return nil
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	l.entries = prune(l.entries, time.Now())
	return l.saveLocked()
}

// Entries returns a copy of the recorded entries, oldest first.
func (l *Ledger) Entries() []Entry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	sort.Slice(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out
}

// Spend returns the dollars spent on a model in each window at time now.
func (l *Ledger) Spend(model string, now time.Time) map[Window]float64 {
	out := map[Window]float64{}
	if l == nil {
		return out
	}
	l.mu.Lock()
	entries := append([]Entry(nil), l.entries...)
	l.mu.Unlock()
	for _, w := range windows {
		cutoff := now.Add(-w.Length)
		var total float64
		for _, e := range entries {
			if e.Model == model && !e.TS.Before(cutoff) && !e.TS.After(now) {
				total += e.Cost
			}
		}
		out[w.Name] = total
	}
	return out
}

// Totals returns dollars spent per model in each window at time now.
func (l *Ledger) Totals(now time.Time) map[string]map[Window]float64 {
	out := map[string]map[Window]float64{}
	if l == nil {
		return out
	}
	l.mu.Lock()
	entries := append([]Entry(nil), l.entries...)
	l.mu.Unlock()
	models := map[string]bool{}
	for _, e := range entries {
		models[e.Model] = true
	}
	for model := range models {
		out[model] = map[Window]float64{}
		for _, w := range windows {
			cutoff := now.Add(-w.Length)
			var total float64
			for _, e := range entries {
				if e.Model == model && !e.TS.Before(cutoff) && !e.TS.After(now) {
					total += e.Cost
				}
			}
			out[model][w.Name] = total
		}
	}
	return out
}

// Check reports whether a request for a model may proceed. An unknown or
// unmetered model is always allowed, because the bridge cannot invent a
// ceiling for it.
func (l *Ledger) Check(model string, now time.Time) Verdict {
	if l == nil {
		return Verdict{Allowed: true}
	}
	m, ok := Lookup(model)
	if !ok || m.Monthly <= 0 {
		return Verdict{Allowed: true, Fraction: 0}
	}
	spend := l.Spend(model, now)
	verdict := Verdict{Allowed: true}
	for _, w := range windows {
		limit := m.Monthly * w.Share
		if limit <= 0 {
			continue
		}
		fraction := spend[w.Name] / limit
		if fraction >= 1 {
			return Verdict{
				Allowed:  false,
				Window:   w.Name,
				Fraction: fraction,
				Limit:    limit,
				Spent:    spend[w.Name],
				Message: fmt.Sprintf(
					"%s allowance exhausted for %s: $%.2f of $%.2f in the last %s. "+
						"Wait for the window to roll over, or pick another model.",
					w.Name, model, spend[w.Name], limit, w.Length),
			}
		}
		if fraction > verdict.Fraction {
			verdict.Fraction = fraction
			verdict.Window = w.Name
			verdict.Limit = limit
			verdict.Spent = spend[w.Name]
		}
	}
	warnAt := l.WarnAt
	if warnAt <= 0 {
		warnAt = 0.90
	}
	if verdict.Fraction >= warnAt {
		verdict.Warning = true
		verdict.Message = fmt.Sprintf(
			"%s is at %.0f%% of its %s allowance ($%.2f of $%.2f)",
			model, verdict.Fraction*100, verdict.Window, verdict.Spent, verdict.Limit)
	}
	return verdict
}

func (l *Ledger) saveLocked() error {
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"entries": l.entries,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// prune drops entries older than the longest window.
func prune(entries []Entry, now time.Time) []Entry {
	cutoff := now.Add(-retention)
	out := entries[:0]
	for _, e := range entries {
		if !e.TS.Before(cutoff) {
			out = append(out, e)
		}
	}
	return out
}
