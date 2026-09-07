// Package pairs is the pairs_zscore strategy: long or short the spread
// between two cointegrated symbols when its rolling z-score is stretched,
// flat again when it has reverted or overstayed.
//
// The definition is docs/CONTRACTS.md, implemented to the letter, because the
// same text is implemented in Python for research and a golden file checks
// the two against each other row by row (docs/PLAN.md §4.3). Anything here
// that reads as pedantic — the order the state machine evaluates exits and
// entries, computing each window from scratch — is there so the two
// implementations cannot drift apart in the last digits.
package pairs

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
const Name = "pairs_zscore"

func init() { strategy.Register(Name, New) }

// Params is the spec's params block, typed. Lookback and MaxHoldDays are
// whole numbers of bars; the spec carries them as numbers and the constructor
// refuses fractions rather than rounding them.
type Params struct {
	HedgeRatio  float64
	Lookback    int
	EntryZ      float64
	ExitZ       float64
	MaxHoldDays int
}

// Pairs is the strategy for one pair. It is immutable once built; the
// position state is replayed from history on every call, never stored.
type Pairs struct {
	a, b   string
	params Params
	gross  float64
}

// Signal is one aligned bar's full story: the z-score (ZDefined false while
// the window is short or its sd is zero), the spread position after this bar
// was applied, and the target weights that follow from it. Targets is the
// last Signal's Weights; the app's "signal now" gauge wants the Z.
type Signal struct {
	Date     time.Time
	Z        float64
	ZDefined bool
	// State is the spread position: +1 long the spread (long A, short B),
	// -1 short it, 0 flat.
	State   int
	Weights map[string]float64
}

// New is the strategy.Factory: it validates the spec's universe, params and
// sizing and returns the strategy as the runtime's interface. Callers that
// want Signals or Z use Parse, which returns the concrete type.
func New(universe []string, params strategy.Params, sizing strategy.Sizing) (strategy.Strategy, error) {
	return Parse(universe, params, sizing)
}

// Parse validates and builds a *Pairs. Every check is a spec error, not a
// default: a typo in a param name must be refused, not silently ignored,
// because the spec is the only place the numbers the runtime trades on are
// written down.
func Parse(universe []string, params strategy.Params, sizing strategy.Sizing) (*Pairs, error) {
	if len(universe) != 2 {
		return nil, fmt.Errorf("pairs: universe must be exactly two symbols, got %v", universe)
	}
	a, b := universe[0], universe[1]
	if a == "" || b == "" {
		return nil, fmt.Errorf("pairs: universe has an empty symbol: %v", universe)
	}
	if a == b {
		return nil, fmt.Errorf("pairs: universe names the same symbol twice: %q", a)
	}

	known := map[string]bool{"hedge_ratio": true, "lookback": true, "entry_z": true, "exit_z": true, "max_hold_days": true}
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
		return nil, fmt.Errorf("pairs: unknown params %s", strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("pairs: missing params %s", strings.Join(missing, ", "))
	}

	lookback, err := wholeNumber("lookback", params["lookback"], 2)
	if err != nil {
		return nil, err
	}
	maxHold, err := wholeNumber("max_hold_days", params["max_hold_days"], 1)
	if err != nil {
		return nil, err
	}
	h := params["hedge_ratio"]
	if math.IsNaN(h) || math.IsInf(h, 0) {
		return nil, fmt.Errorf("pairs: hedge_ratio must be a finite number, got %v", h)
	}
	entry, exit := params["entry_z"], params["exit_z"]
	if math.IsNaN(entry) || math.IsNaN(exit) || exit < 0 || !(entry > exit) {
		return nil, fmt.Errorf("pairs: need entry_z > exit_z >= 0, got entry_z=%v exit_z=%v", entry, exit)
	}
	if !(sizing.GrossLeverage > 0) || math.IsInf(sizing.GrossLeverage, 0) {
		return nil, fmt.Errorf("pairs: gross_leverage must be > 0, got %v", sizing.GrossLeverage)
	}

	return &Pairs{
		a: a, b: b,
		params: Params{HedgeRatio: h, Lookback: lookback, EntryZ: entry, ExitZ: exit, MaxHoldDays: maxHold},
		gross:  sizing.GrossLeverage,
	}, nil
}

// wholeNumber reads a bar count out of a float param, refusing fractions and
// values under min.
func wholeNumber(name string, v float64, min int) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
		return 0, fmt.Errorf("pairs: %s must be a whole number, got %v", name, v)
	}
	if v < float64(min) {
		return 0, fmt.Errorf("pairs: %s must be >= %d, got %v", name, min, v)
	}
	return int(v), nil
}

// Universe is the pair, in spec order: A then B.
func (p *Pairs) Universe() []string { return []string{p.a, p.b} }

// Params is what the strategy was built with.
func (p *Pairs) Params() Params { return p.params }

// Targets returns the weights for the last aligned bar.
//
// pos is ignored on purpose: the contract says the state is replayed from
// history alone, so that the backtest, the dry-run and live trading all get
// the same answer from the same bars. A broker position that disagrees with
// the replayed state is the executor's problem to reconcile (it will trade
// towards these targets), not the signal's to accommodate.
func (p *Pairs) Targets(hist map[string][]bars.Bar, _ []strategy.Position) (map[string]float64, error) {
	sig, err := p.Signals(hist)
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("pairs: %s and %s share no dates", p.a, p.b)
	}
	return sig[len(sig)-1].Weights, nil
}

// Z is the z-score as of the last aligned bar; ok is false where it is
// undefined. An empty alignment is an error rather than a quiet "undefined",
// because no data and a short window are different situations.
func (p *Pairs) Z(hist map[string][]bars.Bar) (z float64, ok bool, err error) {
	sig, err := p.Signals(hist)
	if err != nil {
		return 0, false, err
	}
	if len(sig) == 0 {
		return 0, false, fmt.Errorf("pairs: %s and %s share no dates", p.a, p.b)
	}
	last := sig[len(sig)-1]
	return last.Z, last.ZDefined, nil
}

// Signals replays the strategy over the aligned history and returns one
// Signal per aligned bar, oldest first. This is the whole strategy; Targets
// and Z are views of its last element, and the parity test checks every
// element against the Python implementation.
func (p *Pairs) Signals(hist map[string][]bars.Bar) ([]Signal, error) {
	dates, pa, pb, err := p.align(hist)
	if err != nil {
		return nil, err
	}
	n := len(dates)
	L := p.params.Lookback
	h := p.params.HedgeRatio

	spread := make([]float64, n)
	for i := range spread {
		spread[i] = pa[i] - h*pb[i]
	}

	out := make([]Signal, n)
	s, held := 0, 0
	for t := 0; t < n; t++ {
		z, ok := zscore(spread, t, L)
		s, held = step(s, held, z, ok, p.params)

		// Weights split G across the legs by notional so the dollars long
		// equal the dollars short: n_A = price_A, n_B = h * price_B.
		// The evaluation order (s*G*n/gross, left to right) is the same
		// expression the Python side writes, so the rounding agrees.
		nA, nB := pa[t], h*pb[t]
		gross := nA + nB
		w := map[string]float64{p.a: 0, p.b: 0}
		if s != 0 {
			if !(gross > 0) {
				return nil, fmt.Errorf("pairs: non-positive gross notional %v on %s", gross, dates[t].Format("2006-01-02"))
			}
			fs := float64(s)
			w[p.a] = fs * p.gross * nA / gross
			w[p.b] = -fs * p.gross * nB / gross
		}
		out[t] = Signal{Date: dates[t], Z: z, ZDefined: ok, State: s, Weights: w}
	}
	return out, nil
}

// step applies one bar of the state machine from docs/CONTRACTS.md. Exits
// are evaluated before entries and never share a bar with one: a bar that
// closes a long because z crossed back above -exit_z does not open a short
// even if z also cleared +entry_z, because the exit branch returns.
func step(s, held int, z float64, defined bool, pr Params) (int, int) {
	if !defined {
		return 0, 0
	}
	switch s {
	case +1:
		if z >= -pr.ExitZ || held >= pr.MaxHoldDays {
			return 0, 0
		}
		return s, held + 1
	case -1:
		if z <= pr.ExitZ || held >= pr.MaxHoldDays {
			return 0, 0
		}
		return s, held + 1
	}
	if z <= -pr.EntryZ {
		return +1, 0
	}
	if z >= pr.EntryZ {
		return -1, 0
	}
	return 0, 0
}

// zscore is the z-score of spread[t] against the trailing window of L values
// ending at t, with the sample standard deviation (ddof = 1). Undefined
// while fewer than L values exist or the sd is exactly zero.
//
// The window is summed from scratch every bar. That is O(n·L), which is
// nothing at daily frequency, and it avoids running-sum updates whose
// rounding depends on how far the window has slid. pandas computes its
// rolling std with a Welford-style online update; for prices in the tens to
// hundreds and windows of tens of bars the plain two-pass mean-then-squared-
// deviations in float64 agrees with it to well inside the 1e-9 the golden
// file allows, so the simple form is the right one here.
func zscore(spread []float64, t, L int) (float64, bool) {
	if t+1 < L {
		return 0, false
	}
	win := spread[t+1-L : t+1]
	sum := 0.0
	for _, x := range win {
		sum += x
	}
	mean := sum / float64(L)
	ss := 0.0
	for _, x := range win {
		d := x - mean
		ss += d * d
	}
	sd := math.Sqrt(ss / float64(L-1))
	if sd == 0 {
		return 0, false
	}
	return (spread[t] - mean) / sd, true
}

// align inner-joins the two series on date and returns the shared dates
// (in A's order) with each symbol's adjclose. A date only one symbol has is
// dropped before anything is computed. The alignment is done here rather
// than borrowed from package bars so that the strategy's answer depends on
// nothing but this file and the contract.
func (p *Pairs) align(hist map[string][]bars.Bar) ([]time.Time, []float64, []float64, error) {
	sa, ok := hist[p.a]
	if !ok {
		return nil, nil, nil, fmt.Errorf("pairs: no bars for %s", p.a)
	}
	sb, ok := hist[p.b]
	if !ok {
		return nil, nil, nil, fmt.Errorf("pairs: no bars for %s", p.b)
	}

	byDay := make(map[day]float64, len(sb))
	for _, bar := range sb {
		byDay[dayOf(bar.Date)] = bar.AdjClose
	}

	var (
		dates  []time.Time
		pa, pb []float64
		prev   day
	)
	for _, bar := range sa {
		d := dayOf(bar.Date)
		closeB, ok := byDay[d]
		if !ok {
			continue
		}
		// The rolling window assumes time runs forward through the slice;
		// a series out of order would make every z-score wrong quietly.
		if len(dates) > 0 && !prev.before(d) {
			return nil, nil, nil, fmt.Errorf("pairs: %s bars are not strictly increasing by date at %s", p.a, bar.Date.Format("2006-01-02"))
		}
		prev = d
		dates = append(dates, bar.Date)
		pa = append(pa, bar.AdjClose)
		pb = append(pb, closeB)
	}
	return dates, pa, pb, nil
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
// aligned bar, in order. It is what lets a backtest run in O(n) rather than
// re-deriving the state from a growing prefix.
func (p *Pairs) Weights(hist map[string][]bars.Bar) ([]map[string]float64, error) {
	sig, err := p.Signals(hist)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]float64, len(sig))
	for i, s := range sig {
		out[i] = s.Weights
	}
	return out, nil
}
