// Package usage tracks spend against the OpenCode Go monthly allowance.
//
// Go meters each model separately with three rolling allowances: a 5-hour
// window of 20% of the model's monthly limit, a weekly window of 50%, and the
// monthly limit itself. Because the allowances are per model, the ledger is
// keyed by model and the guard answers per model.
package usage

import "time"

// Prices are US dollars per million tokens.
type Prices struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CachedRead float64 `json:"cached_read"`
}

// Model is one billable model.
type Model struct {
	ID string
	// Prices applies to inputs up to TierAbove tokens.
	Prices Prices
	// TierAbove, when non-zero, is the input-token count above which TierPrices
	// applies instead.
	TierAbove  int
	TierPrices Prices
	// Peak and OffPeak apply to models priced by time of day. When both are
	// zero, Prices is used regardless of the clock.
	Peak    Prices
	OffPeak Prices
	// Monthly is the model's monthly allowance in dollars. Zero means the model
	// is not metered (free during a promotion).
	Monthly float64
	// PeakHours are UTC hours (start inclusive, end exclusive) that bill at the
	// peak rate on weekdays.
	PeakHours [][2]int
}

// Window is one of the three rolling allowances.
type Window string

// The three allowances, smallest first.
const (
	Window5h    Window = "5h"
	WindowWeek  Window = "week"
	WindowMonth Window = "month"
)

// windowLength and windowShare define each allowance: the 5-hour window is 20%
// of the monthly limit and the weekly window is 50%.
var windows = []struct {
	Name   Window
	Length time.Duration
	Share  float64
}{
	{Window5h, 5 * time.Hour, 0.20},
	{WindowWeek, 7 * 24 * time.Hour, 0.50},
	{WindowMonth, 30 * 24 * time.Hour, 1.00},
}

// PeakHoursUTC lists the DeepSeek peak windows: 01:00-04:00 and 06:00-10:00
// UTC, Monday to Friday.
var peakHoursUTC = [][2]int{{1, 4}, {6, 10}}

// catalog is the OpenCode Go price list, taken from
// https://opencode.ai/docs/go/ (captured 2026-10-10). Prices are per 1M tokens.
var catalog = []Model{
	{ID: "glm-5.3-flash", Prices: Prices{0.15, 0.50, 0.03}, Monthly: 60},
	{ID: "glm-5.3", Prices: Prices{1.40, 4.40, 0.26}, Monthly: 15},
	{ID: "glm-5.2", Prices: Prices{1.40, 4.40, 0.26}, Monthly: 60},
	{ID: "glm-5.1", Prices: Prices{1.40, 4.40, 0.26}, Monthly: 60},
	{ID: "glm-5", Prices: Prices{1.40, 4.40, 0.26}, Monthly: 60},
	{ID: "kimi-k3", Prices: Prices{3.00, 15.00, 0.30}, Monthly: 15},
	{ID: "kimi-k2.7-code", Prices: Prices{0.95, 4.00, 0.19}, Monthly: 60},
	{ID: "kimi-k2.6", Prices: Prices{0.95, 4.00, 0.16}, Monthly: 60},
	{ID: "longcat-2.0", Prices: Prices{0.30, 1.20, 0.006}, Monthly: 60},
	{ID: "mimo-v2.6-flash", Prices: Prices{0.14, 0.28, 0.0028}, Monthly: 60},
	{ID: "mimo-v2.6-pro", Prices: Prices{0.435, 0.87, 0.003625}, Monthly: 15},
	{ID: "mimo-v2.5", Prices: Prices{0.14, 0.28, 0.0028}, Monthly: 60},
	{ID: "mimo-v2.5-pro", Prices: Prices{0.435, 0.87, 0.003625}, Monthly: 15},
	{ID: "minimax-m3", Prices: Prices{0.30, 1.20, 0.06}, Monthly: 60},
	{ID: "minimax-m2.7", Prices: Prices{0.30, 1.20, 0.06}, Monthly: 60},
	{ID: "muse-spark-1.3", Prices: Prices{0.10, 0.20, 0.002}, Monthly: 60},
	{ID: "muse-spark-1.2", Prices: Prices{0.10, 0.20, 0.002}, Monthly: 60},
	{
		ID: "qwen3.8-max", Prices: Prices{2.00, 6.00, 0.25}, Monthly: 15,
	},
	{ID: "qwen3.8-flash", Prices: Prices{0.15, 0.47, 0.016}, Monthly: 30},
	{
		ID: "qwen3.7-plus", Prices: Prices{0.40, 1.60, 0.04}, Monthly: 60,
		TierAbove: 256000, TierPrices: Prices{1.20, 4.80, 0.12},
	},
	{
		ID: "deepseek-v4.1-flash", Monthly: 60, PeakHours: peakHoursUTC,
		Peak: Prices{0.30, 1.20, 0.006}, OffPeak: Prices{0.15, 0.60, 0.003},
	},
	{
		ID: "deepseek-v4-pro", Monthly: 15, PeakHours: peakHoursUTC,
		Peak: Prices{1.32, 3.96, 0.044}, OffPeak: Prices{0.66, 1.98, 0.022},
	},
	{
		ID: "deepseek-v4-flash", Monthly: 30, PeakHours: peakHoursUTC,
		Peak: Prices{0.30, 1.20, 0.006}, OffPeak: Prices{0.15, 0.60, 0.003},
	},
	{
		ID: "deepseek-v4-flash-vision-exp", Monthly: 15, PeakHours: peakHoursUTC,
		Peak: Prices{0.30, 1.20, 0.006}, OffPeak: Prices{0.15, 0.60, 0.003},
	},
	{ID: "hy4-preview", Prices: Prices{0.834, 2.501, 0.042}, Monthly: 30},
	{ID: "hy3", Prices: Prices{0.14, 0.58, 0.035}, Monthly: 60},
	{ID: "space-bunny", Prices: Prices{0.15, 0.60, 0.03}, Monthly: 30},
	{
		ID: "grok-4.7", Prices: Prices{2.00, 6.00, 0.50}, Monthly: 15,
		TierAbove: 200000, TierPrices: Prices{4.00, 12.00, 1.00},
	},
	{
		ID: "grok-4.6", Prices: Prices{2.00, 6.00, 0.50}, Monthly: 15,
		TierAbove: 200000, TierPrices: Prices{4.00, 12.00, 1.00},
	},
	{
		ID: "gpt-6-luna", Prices: Prices{0.10, 0.50, 0.01}, Monthly: 15,
		TierAbove: 272000, TierPrices: Prices{0.20, 0.75, 0.02},
	},
	{
		ID: "gpt-5.6-luna", Prices: Prices{0.20, 1.20, 0.02}, Monthly: 15,
		TierAbove: 272000, TierPrices: Prices{0.40, 1.80, 0.04},
	},
	{
		ID: "claude-haiku-5.5", Prices: Prices{0.10, 0.50, 0.01}, Monthly: 15,
		TierAbove: 100000, TierPrices: Prices{0.50, 2.50, 0.05},
	},
}

// Lookup returns the price entry for a model id.
func Lookup(model string) (Model, bool) {
	for _, m := range catalog {
		if m.ID == model {
			return m, true
		}
	}
	return Model{}, false
}

// PricesFor returns the price row that applies to a request of inputTokens at
// time now. Size tiers are resolved by input size; time-of-day tiers by the
// documented UTC peak windows.
func PricesFor(m Model, inputTokens int, now time.Time) Prices {
	base := m.Prices
	if m.TierAbove > 0 && inputTokens > m.TierAbove {
		base = m.TierPrices
	}
	if m.PeakHours == nil {
		return base
	}
	if isPeak(m.PeakHours, now) {
		return m.Peak
	}
	return m.OffPeak
}

// isPeak reports whether now falls inside any peak window on a weekday.
func isPeak(hours [][2]int, now time.Time) bool {
	utc := now.UTC()
	if utc.Weekday() == time.Saturday || utc.Weekday() == time.Sunday {
		return false
	}
	hour := utc.Hour()
	for _, span := range hours {
		if hour >= span[0] && hour < span[1] {
			return true
		}
	}
	return false
}

// Cost returns the dollar cost of one request. Cached input tokens are billed
// at the cached-read rate and are not billed again at the input rate.
func Cost(m Model, input, cached, output int, now time.Time) float64 {
	p := PricesFor(m, input, now)
	billable := input - cached
	if billable < 0 {
		billable = 0
	}
	return (float64(billable)*p.Input +
		float64(cached)*p.CachedRead +
		float64(output)*p.Output) / 1e6
}

// Limits returns the dollar allowance for each window of a model.
func Limits(m Model) map[Window]float64 {
	out := make(map[Window]float64, len(windows))
	for _, w := range windows {
		out[w.Name] = m.Monthly * w.Share
	}
	return out
}

// WindowNames lists the windows smallest first.
func WindowNames() []Window {
	out := make([]Window, 0, len(windows))
	for _, w := range windows {
		out = append(out, w.Name)
	}
	return out
}
