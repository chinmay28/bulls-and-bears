// Package ratio is the ratio_reversion strategy: each risk asset in the
// universe is held while its price ratio to the haven (the universe's last
// symbol) is stretched below the ratio's own trailing mean, and swapped for
// the haven otherwise. The book is always fully invested and never short.
//
// The definition is docs/CONTRACTS.md, implemented to the letter, because the
// same text is implemented in Python for research and a golden file checks
// the two against each other row by row (docs/PLAN.md §4.3). The z-score and
// the state machine are the pairs strategy's, applied to a log ratio instead
// of a spread and one sleeve at a time; the arithmetic is written out in the
// same order the Python side writes it so the two cannot drift in the last
// digits.
package ratio

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
const Name = "ratio_reversion"

func init() { strategy.Register(Name, New) }

// Params is the spec's params block, typed. Lookback and MaxHoldDays are
// whole numbers of bars; the constructor refuses fractions rather than
// rounding them.
type Params struct {
	Lookback    int
	EntryZ      float64
	ExitZ       float64
	MaxHoldDays int
}

// Ratio is the strategy for one universe. It is immutable once built; the
// sleeve states are replayed from history on every call, never stored.
type Ratio struct {
	risk   []string
	haven  string
	params Params
	gross  float64
}

// Signal is one aligned bar's full story: per risk asset, the z-score of its
// log ratio to the haven (ZDefined false while the window is short or its sd
// is zero) and the sleeve's state after this bar was applied (1 in the risk
// asset, 0 in the haven); and the target weights for every symbol.
type Signal struct {
	Date     time.Time
	Z        map[string]float64
	ZDefined map[string]bool
	State    map[string]int
	Weights  map[string]float64
}

// New is the strategy.Factory: it validates the spec's universe, params and
// sizing and returns the strategy as the runtime's interface. Callers that
// want Signals use Parse, which returns the concrete type.
func New(universe []string, params strategy.Params, sizing strategy.Sizing) (strategy.Strategy, error) {
	return Parse(universe, params, sizing)
}

// Parse validates and builds a *Ratio. Every check is a spec error, not a
// default: the spec is the only place the numbers the runtime trades on are
// written down, so a typo must be refused, not silently ignored.
func Parse(universe []string, params strategy.Params, sizing strategy.Sizing) (*Ratio, error) {
	if len(universe) < 2 {
		return nil, fmt.Errorf("ratio: universe needs at least one risk asset and the haven, got %v", universe)
	}
	seen := map[string]bool{}
	for _, s := range universe {
		if s == "" {
			return nil, fmt.Errorf("ratio: universe has an empty symbol: %v", universe)
		}
		if seen[s] {
			return nil, fmt.Errorf("ratio: universe names %q twice", s)
		}
		seen[s] = true
	}

	known := map[string]bool{"lookback": true, "entry_z": true, "exit_z": true, "max_hold_days": true}
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
		return nil, fmt.Errorf("ratio: unknown params %s", strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ratio: missing params %s", strings.Join(missing, ", "))
	}

	lookback, err := wholeNumber("lookback", params["lookback"], 2)
	if err != nil {
		return nil, err
	}
	maxHold, err := wholeNumber("max_hold_days", params["max_hold_days"], 1)
	if err != nil {
		return nil, err
	}
	entry, exit := params["entry_z"], params["exit_z"]
	if math.IsNaN(entry) || math.IsInf(entry, 0) || !(entry > 0) {
		return nil, fmt.Errorf("ratio: entry_z must be a finite number > 0, got %v", entry)
	}
	if math.IsNaN(exit) || math.IsInf(exit, 0) || !(exit > -entry) {
		return nil, fmt.Errorf("ratio: need exit_z > -entry_z, got entry_z=%v exit_z=%v", entry, exit)
	}
	if !(sizing.GrossLeverage > 0) || math.IsInf(sizing.GrossLeverage, 0) {
		return nil, fmt.Errorf("ratio: gross_leverage must be > 0, got %v", sizing.GrossLeverage)
	}

	risk := append([]string(nil), universe[:len(universe)-1]...)
	return &Ratio{
		risk: risk, haven: universe[len(universe)-1],
		params: Params{Lookback: lookback, EntryZ: entry, ExitZ: exit, MaxHoldDays: maxHold},
		gross:  sizing.GrossLeverage,
	}, nil
}

// wholeNumber reads a bar count out of a float param, refusing fractions and
// values under min.
func wholeNumber(name string, v float64, min int) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
		return 0, fmt.Errorf("ratio: %s must be a whole number, got %v", name, v)
	}
	if v < float64(min) {
		return 0, fmt.Errorf("ratio: %s must be >= %d, got %v", name, min, v)
	}
	return int(v), nil
}

// Universe is the spec's order: the risk assets, then the haven.
func (r *Ratio) Universe() []string {
	return append(append([]string(nil), r.risk...), r.haven)
}

// Params is what the strategy was built with.
func (r *Ratio) Params() Params { return r.params }

// Haven is the symbol the sleeves rest in.
func (r *Ratio) Haven() string { return r.haven }

// Targets returns the weights for the last aligned bar.
//
// pos is ignored on purpose: the contract says the state is replayed from
// history alone, so the backtest, the dry-run and live trading all get the
// same answer from the same bars. A broker position that disagrees is the
// executor's problem to reconcile, not the signal's to accommodate.
func (r *Ratio) Targets(hist map[string][]bars.Bar, _ []strategy.Position) (map[string]float64, error) {
	sig, err := r.Signals(hist)
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("ratio: %v share no dates", r.Universe())
	}
	return sig[len(sig)-1].Weights, nil
}

// Signals replays the strategy over the aligned history and returns one
// Signal per aligned bar, oldest first. This is the whole strategy; Targets
// is a view of its last element, and the parity test checks every element
// against the Python implementation.
func (r *Ratio) Signals(hist map[string][]bars.Bar) ([]Signal, error) {
	dates, px, err := r.align(hist)
	if err != nil {
		return nil, err
	}
	n := len(dates)
	k := len(r.risk)
	L := r.params.Lookback

	lnH := make([]float64, n)
	for t, p := range px[r.haven] {
		lnH[t] = math.Log(p)
	}

	out := make([]Signal, n)
	for t := range out {
		out[t] = Signal{
			Date: dates[t], Z: map[string]float64{}, ZDefined: map[string]bool{},
			State: map[string]int{}, Weights: map[string]float64{},
		}
	}
	inRisk := make([]int, n)
	for _, sym := range r.risk {
		x := make([]float64, n)
		for t, p := range px[sym] {
			x[t] = math.Log(p) - lnH[t]
		}
		s, held := 0, 0
		for t := 0; t < n; t++ {
			z, ok := zscore(x, t, L)
			s, held = step(s, held, z, ok, r.params)
			out[t].Z[sym], out[t].ZDefined[sym], out[t].State[sym] = z, ok, s
			// Each sleeve is G/k in the risk asset or nothing; the same
			// expression, in the same order, as the Python side.
			if s == 1 {
				out[t].Weights[sym] = r.gross / float64(k)
				inRisk[t]++
			} else {
				out[t].Weights[sym] = 0
			}
		}
	}
	for t := range out {
		flat := k - inRisk[t]
		out[t].Weights[r.haven] = r.gross * float64(flat) / float64(k)
	}
	return out, nil
}

// step applies one bar of a sleeve's state machine from docs/CONTRACTS.md.
// The exit is evaluated before the entry and never shares a bar with one.
func step(s, held int, z float64, defined bool, pr Params) (int, int) {
	if !defined {
		return 0, 0
	}
	if s == 1 {
		if z >= pr.ExitZ || held >= pr.MaxHoldDays {
			return 0, 0
		}
		return 1, held + 1
	}
	if z <= -pr.EntryZ {
		return 1, 0
	}
	return 0, 0
}

// zscore is the z-score of x[t] against the trailing window of L values
// ending at t, with the sample standard deviation (ddof = 1). Undefined while
// fewer than L values exist or the sd is exactly zero. The window is summed
// from scratch every bar, as in the pairs strategy and for the same reason:
// it agrees with pandas' rolling std to well inside the golden tolerance.
func zscore(x []float64, t, L int) (float64, bool) {
	if t+1 < L {
		return 0, false
	}
	win := x[t+1-L : t+1]
	sum := 0.0
	for _, v := range win {
		sum += v
	}
	mean := sum / float64(L)
	ss := 0.0
	for _, v := range win {
		d := v - mean
		ss += d * d
	}
	sd := math.Sqrt(ss / float64(L-1))
	if sd == 0 {
		return 0, false
	}
	return (x[t] - mean) / sd, true
}

// align inner-joins every symbol's series on date, in the haven's order, and
// returns the shared dates with each symbol's adjclose. A date any symbol
// lacks is dropped before anything is computed. The alignment is done here
// rather than borrowed from package bars so that the strategy's answer
// depends on nothing but this file and the contract.
func (r *Ratio) align(hist map[string][]bars.Bar) ([]time.Time, map[string][]float64, error) {
	universe := r.Universe()
	byDay := make(map[string]map[day]float64, len(universe))
	for _, sym := range universe {
		series, ok := hist[sym]
		if !ok {
			return nil, nil, fmt.Errorf("ratio: no bars for %s", sym)
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
	for _, bar := range hist[r.haven] {
		d := dayOf(bar.Date)
		shared := true
		for _, sym := range r.risk {
			if _, ok := byDay[sym][d]; !ok {
				shared = false
				break
			}
		}
		if !shared {
			continue
		}
		// The rolling window assumes time runs forward through the slice;
		// a series out of order would make every z-score wrong quietly.
		if len(dates) > 0 && !prev.before(d) {
			return nil, nil, fmt.Errorf("ratio: %s bars are not strictly increasing by date at %s", r.haven, bar.Date.Format("2006-01-02"))
		}
		prev = d
		dates = append(dates, bar.Date)
		for _, sym := range universe {
			px[sym] = append(px[sym], byDay[sym][d])
		}
	}
	return dates, px, nil
}

// day is a calendar date, the join key. Bars carry midnight UTC, but joining
// on the date rather than the instant keeps a bar stamped in another
// location from silently failing to match.
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

// Weights is the backtest engine's view of Signals: one weights map per
// aligned bar, in order, so a backtest runs in O(n) rather than re-deriving
// the state from a growing prefix.
func (r *Ratio) Weights(hist map[string][]bars.Bar) ([]map[string]float64, error) {
	sig, err := r.Signals(hist)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]float64, len(sig))
	for i, s := range sig {
		out[i] = s.Weights
	}
	return out, nil
}
