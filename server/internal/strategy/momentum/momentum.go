// Package momentum is the dual_momentum strategy: every rebalance_days bars
// the risk assets are ranked by their trailing lookback-bar return, the top
// top_k of those that beat the haven's own trailing return are held, and
// the rest of the book sits in the haven (the universe's last symbol).
// Between rebalances the holdings stand.
//
// The definition is docs/CONTRACTS.md, implemented to the letter, because the
// same text is implemented in Python for research and a golden file checks
// the two against each other row by row (docs/PLAN.md §4.3). The ranking is
// a stable sort, so ties keep universe order on both sides.
package momentum

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// Name is what a spec's `strategy` field says to get this implementation.
const Name = "dual_momentum"

func init() { strategy.Register(Name, New) }

// Params is the spec's params block, typed.
type Params struct {
	Lookback      int
	TopK          int
	RebalanceDays int
}

// Momentum is the strategy for one universe.
type Momentum struct {
	risk   []string
	haven  string
	params Params
	gross  float64
}

// Signal is one aligned bar: every symbol's trailing return (Defined false
// while the window is short), which risk assets are held after this bar,
// and the target weights for every symbol.
type Signal struct {
	Date    time.Time
	Return  map[string]float64
	Defined map[string]bool
	Held    map[string]bool
	Weights map[string]float64
}

// New is the strategy.Factory.
func New(universe []string, params strategy.Params, sizing strategy.Sizing) (strategy.Strategy, error) {
	return Parse(universe, params, sizing)
}

// Parse validates and builds a *Momentum. Every check is a spec error, not
// a default.
func Parse(universe []string, params strategy.Params, sizing strategy.Sizing) (*Momentum, error) {
	if len(universe) < 2 {
		return nil, fmt.Errorf("momentum: universe needs at least one risk asset and the haven, got %v", universe)
	}
	seen := map[string]bool{}
	for _, s := range universe {
		if s == "" {
			return nil, fmt.Errorf("momentum: universe has an empty symbol: %v", universe)
		}
		if seen[s] {
			return nil, fmt.Errorf("momentum: universe names %q twice", s)
		}
		seen[s] = true
	}
	known := map[string]bool{"lookback": true, "top_k": true, "rebalance_days": true}
	var unknown, missing []string
	for name := range params {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	for name := range known {
		if _, ok := params[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(unknown)
	sort.Strings(missing)
	if len(unknown) > 0 {
		return nil, fmt.Errorf("momentum: unknown params %s", strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("momentum: missing params %s", strings.Join(missing, ", "))
	}
	lb, err := wholeNumber("lookback", params["lookback"], 1)
	if err != nil {
		return nil, err
	}
	topK, err := wholeNumber("top_k", params["top_k"], 1)
	if err != nil {
		return nil, err
	}
	reb, err := wholeNumber("rebalance_days", params["rebalance_days"], 1)
	if err != nil {
		return nil, err
	}
	if topK > len(universe)-1 {
		return nil, fmt.Errorf("momentum: top_k %d exceeds the %d risk assets", topK, len(universe)-1)
	}
	if !(sizing.GrossLeverage > 0) || math.IsInf(sizing.GrossLeverage, 0) {
		return nil, fmt.Errorf("momentum: gross_leverage must be > 0, got %v", sizing.GrossLeverage)
	}
	return &Momentum{
		risk: append([]string(nil), universe[:len(universe)-1]...), haven: universe[len(universe)-1],
		params: Params{Lookback: lb, TopK: topK, RebalanceDays: reb}, gross: sizing.GrossLeverage,
	}, nil
}

func wholeNumber(name string, v float64, min int) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
		return 0, fmt.Errorf("momentum: %s must be a whole number, got %v", name, v)
	}
	if v < float64(min) {
		return 0, fmt.Errorf("momentum: %s must be >= %d, got %v", name, min, v)
	}
	return int(v), nil
}

// Universe is the spec's order: the risk assets, then the haven.
func (m *Momentum) Universe() []string { return append(append([]string(nil), m.risk...), m.haven) }

// Params is what the strategy was built with.
func (m *Momentum) Params() Params { return m.params }

// Targets returns the weights for the last aligned bar. pos is ignored on
// purpose: the holdings are replayed from history alone.
func (m *Momentum) Targets(hist map[string][]bars.Bar, _ []strategy.Position) (map[string]float64, error) {
	sig, err := m.Signals(hist)
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("momentum: %v share no dates", m.Universe())
	}
	return sig[len(sig)-1].Weights, nil
}

// Signals replays the rebalance schedule over the aligned history, one
// Signal per aligned bar, oldest first.
func (m *Momentum) Signals(hist map[string][]bars.Bar) ([]Signal, error) {
	universe := m.Universe()
	dates, px, err := align(universe, m.haven, hist)
	if err != nil {
		return nil, err
	}
	n := len(dates)
	L := m.params.Lookback
	topK := m.params.TopK
	out := make([]Signal, n)
	var chosen []string
	for t := 0; t < n; t++ {
		sig := Signal{Date: dates[t], Return: map[string]float64{}, Defined: map[string]bool{},
			Held: map[string]bool{}, Weights: map[string]float64{}}
		for _, sym := range universe {
			if t >= L {
				sig.Return[sym] = px[sym][t]/px[sym][t-L] - 1
				sig.Defined[sym] = true
			}
		}
		if t >= L && (t-L)%m.params.RebalanceDays == 0 {
			chosen = pick(m.risk, sig.Return, m.haven, topK)
		}
		held := 0
		for _, sym := range m.risk {
			sig.Weights[sym] = 0
		}
		for _, sym := range chosen {
			sig.Held[sym] = true
			sig.Weights[sym] = m.gross / float64(topK)
			held++
		}
		sig.Weights[m.haven] = m.gross * float64(topK-held) / float64(topK)
		out[t] = sig
	}
	return out, nil
}

// pick is the risk assets to hold: those beating the haven, best first, at
// most topK. Ties keep universe order, as the Python side's stable sort does.
func pick(risk []string, ret map[string]float64, haven string, topK int) []string {
	var ahead []string
	for _, s := range risk {
		if ret[s] > ret[haven] {
			ahead = append(ahead, s)
		}
	}
	sort.SliceStable(ahead, func(i, j int) bool { return ret[ahead[i]] > ret[ahead[j]] })
	if len(ahead) > topK {
		ahead = ahead[:topK]
	}
	return ahead
}

// Weights is the backtest engine's view of Signals.
func (m *Momentum) Weights(hist map[string][]bars.Bar) ([]map[string]float64, error) {
	sig, err := m.Signals(hist)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]float64, len(sig))
	for i, s := range sig {
		out[i] = s.Weights
	}
	return out, nil
}

// align inner-joins every symbol's series on calendar date, in the haven's
// order. Done here rather than borrowed so the strategy's answer depends on
// nothing but this file and the contract.
func align(universe []string, haven string, hist map[string][]bars.Bar) ([]time.Time, map[string][]float64, error) {
	byDay := make(map[string]map[day]float64, len(universe))
	for _, sym := range universe {
		series, ok := hist[sym]
		if !ok {
			return nil, nil, fmt.Errorf("momentum: no bars for %s", sym)
		}
		idx := make(map[day]float64, len(series))
		for _, b := range series {
			idx[dayOf(b.Date)] = b.AdjClose
		}
		byDay[sym] = idx
	}
	var (
		dates []time.Time
		prev  day
	)
	px := make(map[string][]float64, len(universe))
	for _, bar := range hist[haven] {
		d := dayOf(bar.Date)
		shared := true
		for _, sym := range universe {
			if _, ok := byDay[sym][d]; !ok {
				shared = false
				break
			}
		}
		if !shared {
			continue
		}
		if len(dates) > 0 && !prev.before(d) {
			return nil, nil, fmt.Errorf("momentum: %s bars are not strictly increasing by date at %s", haven, bar.Date.Format("2006-01-02"))
		}
		prev = d
		dates = append(dates, bar.Date)
		for _, sym := range universe {
			px[sym] = append(px[sym], byDay[sym][d])
		}
	}
	return dates, px, nil
}

type day struct {
	y int
	m time.Month
	d int
}

func dayOf(t time.Time) day {
	y, m, d := t.UTC().Date()
	return day{y, m, d}
}

func (a day) before(b day) bool {
	if a.y != b.y {
		return a.y < b.y
	}
	if a.m != b.m {
		return a.m < b.m
	}
	return a.d < b.d
}
