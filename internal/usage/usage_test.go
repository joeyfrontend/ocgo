package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCostBillsCachedTokensAtTheCachedRate(t *testing.T) {
	m, ok := Lookup("glm-5.3-flash")
	if !ok {
		t.Fatal("glm-5.3-flash missing from the catalog")
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	// 1M input, 0.5M of it cached, 1M output.
	got := Cost(m, 1_000_000, 500_000, 1_000_000, now)
	want := 0.5*0.15 + 0.5*0.03 + 1.0*0.50
	if !closeTo(got, want) {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestCostNeverBillsNegativeInput(t *testing.T) {
	m, _ := Lookup("glm-5.3")
	got := Cost(m, 100, 500, 0, time.Now())
	if got < 0 {
		t.Fatalf("cost = %v, want >= 0", got)
	}
	if !closeTo(got, 500*0.26/1e6) {
		t.Fatalf("cost = %v, want the cached rate applied to all tokens", got)
	}
}

func TestPricesForSelectsSizeTier(t *testing.T) {
	m, ok := Lookup("grok-4.7")
	if !ok {
		t.Fatal("grok-4.7 missing")
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) // Saturday: no peak windows
	if p := PricesFor(m, 100_000, now); p.Input != 2.00 {
		t.Fatalf("small input should use the base tier: %+v", p)
	}
	if p := PricesFor(m, 300_000, now); p.Input != 4.00 {
		t.Fatalf("large input should use the upper tier: %+v", p)
	}
}

func TestPricesForSelectsPeakWindow(t *testing.T) {
	m, ok := Lookup("deepseek-v4-pro")
	if !ok {
		t.Fatal("deepseek-v4-pro missing")
	}
	// 2026-10-09 is a Friday. Peak windows are 01:00-04:00 and 06:00-10:00 UTC.
	peak := time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	offPeak := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	weekend := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)

	if p := PricesFor(m, 1000, peak); p.Input != 1.32 {
		t.Fatalf("peak price = %v, want 1.32", p.Input)
	}
	if p := PricesFor(m, 1000, offPeak); p.Input != 0.66 {
		t.Fatalf("off-peak price = %v, want 0.66", p.Input)
	}
	if p := PricesFor(m, 1000, weekend); p.Input != 0.66 {
		t.Fatalf("weekend price = %v, want the off-peak rate", p.Input)
	}
}

func TestLimitsAreFractionsOfTheMonthlyAllowance(t *testing.T) {
	m, _ := Lookup("glm-5.3-flash")
	limits := Limits(m)
	if !closeTo(limits[Window5h], 12) {
		t.Fatalf("5h limit = %v, want 12", limits[Window5h])
	}
	if !closeTo(limits[WindowWeek], 30) {
		t.Fatalf("week limit = %v, want 30", limits[WindowWeek])
	}
	if !closeTo(limits[WindowMonth], 60) {
		t.Fatalf("month limit = %v, want 60", limits[WindowMonth])
	}
}

func TestSpendUsesRollingWindows(t *testing.T) {
	l, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, e := range []Entry{
		{TS: now.Add(-1 * time.Hour), Model: "glm-5.3-flash", Cost: 1},
		{TS: now.Add(-6 * time.Hour), Model: "glm-5.3-flash", Cost: 2},
		{TS: now.Add(-48 * time.Hour), Model: "glm-5.3-flash", Cost: 4},
		{TS: now.Add(-40 * 24 * time.Hour), Model: "glm-5.3-flash", Cost: 100},
		{TS: now.Add(-1 * time.Hour), Model: "glm-5.3", Cost: 50},
	} {
		if err := l.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	spend := l.Spend("glm-5.3-flash", now)
	if !closeTo(spend[Window5h], 1) {
		t.Fatalf("5h spend = %v, want 1", spend[Window5h])
	}
	if !closeTo(spend[WindowWeek], 7) {
		t.Fatalf("week spend = %v, want 7", spend[WindowWeek])
	}
	// The 40-day-old entry is outside every window and outside retention.
	if !closeTo(spend[WindowMonth], 7) {
		t.Fatalf("month spend = %v, want 7", spend[WindowMonth])
	}
	other := l.Spend("glm-5.3", now)
	if !closeTo(other[Window5h], 50) {
		t.Fatalf("spend must be per model, got %v", other[Window5h])
	}
}

func TestCheckBlocksWhenAWindowIsExhausted(t *testing.T) {
	l, _ := Open("")
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	// The 5-hour allowance for glm-5.3-flash is $12.
	if err := l.Record(Entry{TS: now.Add(-time.Minute), Model: "glm-5.3-flash", Cost: 12}); err != nil {
		t.Fatal(err)
	}
	v := l.Check("glm-5.3-flash", now)
	if v.Allowed {
		t.Fatalf("an exhausted window must block: %+v", v)
	}
	if v.Window != Window5h {
		t.Fatalf("binding window = %s, want 5h", v.Window)
	}
	if v.Message == "" {
		t.Fatal("a refusal must explain itself")
	}
}

func TestCheckWarnsBeforeTheCap(t *testing.T) {
	l, _ := Open("")
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if err := l.Record(Entry{TS: now.Add(-time.Minute), Model: "glm-5.3-flash", Cost: 11}); err != nil {
		t.Fatal(err)
	}
	v := l.Check("glm-5.3-flash", now)
	if !v.Allowed {
		t.Fatalf("91%% of a window should still be allowed: %+v", v)
	}
	if !v.Warning {
		t.Fatalf("expected a warning at 91%%: %+v", v)
	}
}

func TestCheckAllowsBelowTheWarnThreshold(t *testing.T) {
	l, _ := Open("")
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if err := l.Record(Entry{TS: now.Add(-time.Minute), Model: "glm-5.3-flash", Cost: 1}); err != nil {
		t.Fatal(err)
	}
	v := l.Check("glm-5.3-flash", now)
	if !v.Allowed || v.Warning {
		t.Fatalf("expected a clean pass: %+v", v)
	}
}

func TestCheckIgnoresUnknownModels(t *testing.T) {
	l, _ := Open("")
	if v := l.Check("not-a-real-model", time.Now()); !v.Allowed {
		t.Fatalf("unknown model should be allowed: %+v", v)
	}
}

// The free previews cost nothing and have no allowance, so however much is
// spent elsewhere they must never be refused — and they must not be billed.
func TestFreeModelsAreUnmeteredAndCostNothing(t *testing.T) {
	l, _ := Open("")
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"longcat-2.5-preview-free", "step-5-preview-free"} {
		m, ok := Lookup(id)
		if !ok {
			t.Fatalf("%s missing from the catalog", id)
		}
		if !m.Free || m.Monthly != 0 {
			t.Fatalf("%s should be free: %+v", id, m)
		}
		if cost := Cost(m, 10_000_000, 0, 1_000_000, now); cost != 0 {
			t.Fatalf("%s billed %v for a large request", id, cost)
		}
		if err := l.Record(Entry{TS: now, Model: id, Cost: 0}); err != nil {
			t.Fatal(err)
		}
		if v := l.Check(id, now); !v.Allowed {
			t.Fatalf("%s should never be refused: %+v", id, v)
		}
	}
}

// Every id the API advertises must be priced or explicitly free, or the
// spend guard silently fails to meter it.
func TestCatalogCoversTheServedModels(t *testing.T) {
	served := []string{
		"minimax-m3", "minimax-m2.7", "minimax-m2.5", "kimi-k3", "kimi-k2.7-code",
		"kimi-k2.6", "kimi-k2.5", "longcat-2.0", "longcat-2.5-preview-free",
		"step-5-preview-free", "glm-5.2", "glm-5.3-flash", "glm-5.3", "glm-5.1",
		"glm-5", "deepseek-v4-pro", "deepseek-v4-flash", "deepseek-flash",
		"deepseek-v4.1-flash", "deepseek-v4-flash-vision-exp", "qwen3.7-max",
		"qwen3.8-max", "qwen3.8-flash", "qwen3.7-plus", "qwen3.6-plus",
		"qwen3.5-plus", "mimo-v2-pro", "mimo-v2-omni", "mimo-v2.6-pro",
		"mimo-v2.6-flash", "mimo-v2.5-pro", "mimo-v2.5", "hy4-preview", "hy3",
		"hy3-preview", "claude-haiku-5-5", "gpt-5.6-luna", "grok-4.5", "grok-4.7",
		"grok-4.6", "muse-spark-1.3-contributor", "muse-spark-1.2-contributor",
		"gpt-6-luna", "space-bunny",
	}
	for _, id := range served {
		if _, ok := Lookup(id); !ok {
			t.Errorf("%s is served by the API but absent from the price catalog", id)
		}
	}
}

func TestNilLedgerIsUsable(t *testing.T) {
	var l *Ledger
	if err := l.Record(Entry{Model: "glm-5.3-flash", Cost: 1}); err != nil {
		t.Fatal(err)
	}
	if v := l.Check("glm-5.3-flash", time.Now()); !v.Allowed {
		t.Fatalf("nil ledger should allow: %+v", v)
	}
	if l.Spend("glm-5.3-flash", time.Now())[Window5h] != 0 {
		t.Fatal("nil ledger should report no spend")
	}
	if l.Path() != "" {
		t.Fatal("nil ledger has no path")
	}
}

func TestLedgerPersistsAndIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := l.Record(Entry{TS: now, Model: "glm-5.3-flash", Input: 10, Output: 2, Cost: 0.5}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("ledger mode = %o, want 600", perm)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Spend("glm-5.3-flash", now)[Window5h]; !closeTo(got, 0.5) {
		t.Fatalf("reloaded spend = %v, want 0.5", got)
	}
	if len(reopened.Entries()) != 1 {
		t.Fatalf("reloaded %d entries, want 1", len(reopened.Entries()))
	}
}

func TestLedgerMissingFileIsNotAnError(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("a missing ledger should be the first run, got %v", err)
	}
	if len(l.Entries()) != 0 {
		t.Fatal("a fresh ledger should be empty")
	}
}

func TestLedgerCorruptFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("a corrupt ledger should be reported, not silently discarded")
	}
}

func TestTotalsGroupByModel(t *testing.T) {
	l, _ := Open("")
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, e := range []Entry{
		{TS: now.Add(-time.Minute), Model: "glm-5.3-flash", Cost: 1},
		{TS: now.Add(-time.Minute), Model: "glm-5.3", Cost: 3},
		{TS: now.Add(-time.Minute), Model: "glm-5.3", Cost: 3},
	} {
		if err := l.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	totals := l.Totals(now)
	if !closeTo(totals["glm-5.3"][Window5h], 6) {
		t.Fatalf("glm-5.3 5h total = %v, want 6", totals["glm-5.3"][Window5h])
	}
	if !closeTo(totals["glm-5.3-flash"][Window5h], 1) {
		t.Fatalf("glm-5.3-flash 5h total = %v, want 1", totals["glm-5.3-flash"][Window5h])
	}
}

func TestCatalogHasTheModelsTheBridgeDefaultsTo(t *testing.T) {
	for _, id := range []string{"glm-5.3-flash", "glm-5.3"} {
		m, ok := Lookup(id)
		if !ok {
			t.Fatalf("%s missing from the catalog", id)
		}
		if m.Monthly <= 0 {
			t.Fatalf("%s has no monthly allowance", id)
		}
	}
}
