// Package trend is the sma_trend strategy: Faber's timing rule, one sleeve
// per risk asset. Each is held while its price is above its own trailing
// moving average and swapped for the haven (the universe's last symbol)
// otherwise; a band around the average keeps a price that hugs it from
// flipping the sleeve every day.
//
// The definition is docs/CONTRACTS.md, implemented to the letter, because the
// same text is implemented in Python for research and a golden file checks
// the two against each other row by row (docs/PLAN.md §4.3). The average is
// a running sum divided by the window, evaluated in the order the Python
// side evaluates it, so the two agree bit for bit and a price sitting
// exactly on its average reads the same on both sides.
package trend

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
const Name = "sma_trend"

func init() { strategy.Register(Name, New) }

// Params is the spec's params block, typed.
type Params struct {
	Lookback int
	Band     float64
}

// Trend is the strategy for one universe. Immutable once built; the sleeve
// states are replayed from history on every call, never stored.
type Trend struct {
	risk   []string
	haven  string
	params Params
	gross  float64
}

// Signal is one aligned bar: per risk asset the average (Defined false
// while the window is short) and the sleeve's state after this bar, and
// the target weights for every symbol.
type Signal struct {
	Date    time.Time
	SMA     map[string]float64
	Defined map[string]bool
	State   map[string]int
	Weights map[string]float64
}

// New is the strategy.Factory.
func New(universe []string, params strategy.Params, sizing strategy.Sizing) (strategy.Strategy, error) {
	return Parse(universe, params, sizing)
}

// Parse validates and builds a *Trend. Every check is a spec error, not a
// default.
func Parse(universe []string, params strategy.Params, sizing strategy.Sizing) (*Trend, error) {
	if len(universe) < 2 {
		return nil, fmt.Errorf("trend: universe needs at least one risk asset and the haven, got %v", universe)
	}
	seen := map[string]bool{}
	for _, s := range universe {
		if s == "" {
			return nil, fmt.Errorf("trend: universe has an empty symbol: %v", universe)
		}
		if seen[s] {
			return nil, fmt.Errorf("trend: universe names %q twice", s)
		}
		seen[s] = true
	}
	known := map[string]bool{"lookback": true, "band": true}
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
		return nil, fmt.Errorf("trend: unknown params %s", strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("trend: missing params %s", strings.Join(missing, ", "))
	}
	lb := params["lookback"]
	if math.IsNaN(lb) || math.IsInf(lb, 0) || lb != math.Trunc(lb) {
		return nil, fmt.Errorf("trend: lookback must be a whole number, got %v", lb)
	}
	if lb < 2 {
		return nil, fmt.Errorf("trend: lookback must be >= 2, got %v", lb)
	}
	band := params["band"]
	if math.IsNaN(band) || !(band >= 0) || !(band < 1) {
		return nil, fmt.Errorf("trend: need 0 <= band < 1, got %v", band)
	}
	if !(sizing.GrossLeverage > 0) || math.IsInf(sizing.GrossLeverage, 0) {
		return nil, fmt.Errorf("trend: gross_leverage must be > 0, got %v", sizing.GrossLeverage)
	}
	return &Trend{
		risk: append([]string(nil), universe[:len(universe)-1]...), haven: universe[len(universe)-1],
		params: Params{Lookback: int(lb), Band: band}, gross: sizing.GrossLeverage,
	}, nil
}

// Universe is the spec's order: the risk assets, then the haven.
func (tr *Trend) Universe() []string { return append(append([]string(nil), tr.risk...), tr.haven) }

// Params is what the strategy was built with.
func (tr *Trend) Params() Params { return tr.params }

// Targets returns the weights for the last aligned bar. pos is ignored on
// purpose: the state is replayed from history alone.
func (tr *Trend) Targets(hist map[string][]bars.Bar, _ []strategy.Position) (map[string]float64, error) {
	sig, err := tr.Signals(hist)
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("trend: %v share no dates", tr.Universe())
	}
	return sig[len(sig)-1].Weights, nil
}

// Signals replays the strategy over the aligned history, one Signal per
// aligned bar, oldest first.
func (tr *Trend) Signals(hist map[string][]bars.Bar) ([]Signal, error) {
	dates, px, err := align(tr.Universe(), tr.haven, hist)
	if err != nil {
		return nil, err
	}
	n := len(dates)
	k := len(tr.risk)
	out := make([]Signal, n)
	for t := range out {
		out[t] = Signal{Date: dates[t], SMA: map[string]float64{}, Defined: map[string]bool{},
			State: map[string]int{}, Weights: map[string]float64{}}
	}
	inRisk := make([]int, n)
	for _, sym := range tr.risk {
		sma, ok := movingAverage(px[sym], tr.params.Lookback)
		s := 0
		for t := 0; t < n; t++ {
			s = step(s, px[sym][t], sma[t], ok[t], tr.params)
			out[t].SMA[sym], out[t].Defined[sym], out[t].State[sym] = sma[t], ok[t], s
			if s == 1 {
				out[t].Weights[sym] = tr.gross / float64(k)
				inRisk[t]++
			} else {
				out[t].Weights[sym] = 0
			}
		}
	}
	for t := range out {
		out[t].Weights[tr.haven] = tr.gross * float64(k-inRisk[t]) / float64(k)
	}
	return out, nil
}

// movingAverage is (C_t - C_{t-L}) / L over the in-order cumulative sum C,
// undefined for the first L-1 bars. The same arithmetic, in the same order,
// as the Python side's np.cumsum.
func movingAverage(px []float64, L int) ([]float64, []bool) {
	n := len(px)
	sma := make([]float64, n)
	ok := make([]bool, n)
	cs := make([]float64, n)
	sum := 0.0
	for i, p := range px {
		sum += p
		cs[i] = sum
	}
	for t := L - 1; t < n; t++ {
		if t == L-1 {
			sma[t] = cs[t] / float64(L)
		} else {
			sma[t] = (cs[t] - cs[t-L]) / float64(L)
		}
		ok[t] = true
	}
	return sma, ok
}

// step applies one bar of a sleeve: strictly above the band enters, strictly
// below it exits. A price exactly on the average, or inside the band, leaves
// the sleeve where it is, so even a zero band cannot flip it every bar.
func step(s int, price, sma float64, defined bool, pr Params) int {
	if !defined {
		return 0
	}
	if s == 1 {
		if price < sma*(1-pr.Band) {
			return 0
		}
		return 1
	}
	if price > sma*(1+pr.Band) {
		return 1
	}
	return 0
}

// Weights is the backtest engine's view of Signals.
func (tr *Trend) Weights(hist map[string][]bars.Bar) ([]map[string]float64, error) {
	sig, err := tr.Signals(hist)
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
// order, and returns the shared dates with each symbol's adjclose. Done here
// rather than borrowed so the strategy's answer depends on nothing but this
// file and the contract.
func align(universe []string, haven string, hist map[string][]bars.Bar) ([]time.Time, map[string][]float64, error) {
	byDay := make(map[string]map[day]float64, len(universe))
	for _, sym := range universe {
		series, ok := hist[sym]
		if !ok {
			return nil, nil, fmt.Errorf("trend: no bars for %s", sym)
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
			return nil, nil, fmt.Errorf("trend: %s bars are not strictly increasing by date at %s", haven, bar.Date.Format("2006-01-02"))
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
