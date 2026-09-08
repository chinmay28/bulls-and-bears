// Package donchian is the donchian_breakout strategy: the Turtle channel
// breakout, long-only, one sleeve per risk asset. A sleeve enters on a close
// above the highest adjusted high of the prior entry_lookback bars and leaves
// for the haven (the universe's last symbol) on a close below the lowest
// adjusted low of the prior exit_lookback bars. The channels never include
// the bar being judged, and they read the adjusted high and low, so a split
// cannot fake a breakout.
//
// The definition is docs/CONTRACTS.md, implemented to the letter, because
// the same text is implemented in Python for research and a golden file
// checks the two against each other row by row (docs/PLAN.md §4.3).
package donchian

import (
	"fmt"
	"math"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/indicator"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/sleeves"
)

// Name is what a spec's `strategy` field says to get this implementation.
const Name = "donchian_breakout"

func init() { strategy.Register(Name, New) }

// Params is the spec's params block, typed.
type Params struct {
	EntryLookback int
	ExitLookback  int
}

// Donchian is the strategy for one universe. Immutable once built; the
// sleeve states are replayed from history on every call, never stored.
type Donchian struct {
	risk   []string
	haven  string
	params Params
	gross  float64
}

// Signal is one aligned bar: per risk asset the channels (UpperDefined
// false while the entry window is short) and the sleeve's state after this
// bar, and the target weights for every symbol.
type Signal struct {
	Date         time.Time
	Upper, Lower map[string]float64
	UpperDefined map[string]bool
	State        map[string]int
	Weights      map[string]float64
}

// New is the strategy.Factory.
func New(universe []string, params strategy.Params, sizing strategy.Sizing) (strategy.Strategy, error) {
	return Parse(universe, params, sizing)
}

// Parse validates and builds a *Donchian. Every check is a spec error, not
// a default.
func Parse(universe []string, params strategy.Params, sizing strategy.Sizing) (*Donchian, error) {
	risk, haven, err := sleeves.Universe("donchian", universe)
	if err != nil {
		return nil, err
	}
	if err := sleeves.Known("donchian", params, "entry_lookback", "exit_lookback"); err != nil {
		return nil, err
	}
	entry, err := sleeves.WholeNumber("donchian", "entry_lookback", params["entry_lookback"], 2)
	if err != nil {
		return nil, err
	}
	exit, err := sleeves.WholeNumber("donchian", "exit_lookback", params["exit_lookback"], 2)
	if err != nil {
		return nil, err
	}
	if exit >= entry {
		return nil, fmt.Errorf("donchian: need exit_lookback < entry_lookback, got %d and %d", exit, entry)
	}
	if !(sizing.GrossLeverage > 0) || math.IsInf(sizing.GrossLeverage, 0) {
		return nil, fmt.Errorf("donchian: gross_leverage must be > 0, got %v", sizing.GrossLeverage)
	}
	return &Donchian{risk: risk, haven: haven, params: Params{EntryLookback: entry, ExitLookback: exit}, gross: sizing.GrossLeverage}, nil
}

// Universe is the spec's order: the risk assets, then the haven.
func (d *Donchian) Universe() []string { return append(append([]string(nil), d.risk...), d.haven) }

// Params is what the strategy was built with.
func (d *Donchian) Params() Params { return d.params }

// Targets returns the weights for the last aligned bar. pos is ignored on
// purpose: the state is replayed from history alone.
func (d *Donchian) Targets(hist map[string][]bars.Bar, _ []strategy.Position) (map[string]float64, error) {
	sig, err := d.Signals(hist)
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("donchian: %v share no dates", d.Universe())
	}
	return sig[len(sig)-1].Weights, nil
}

// Signals replays the strategy over the aligned history, one Signal per
// aligned bar, oldest first.
func (d *Donchian) Signals(hist map[string][]bars.Bar) ([]Signal, error) {
	dates, aligned, err := sleeves.AlignBars("donchian", d.Universe(), d.haven, hist)
	if err != nil {
		return nil, err
	}
	n := len(dates)
	k := len(d.risk)
	out := make([]Signal, n)
	for t := range out {
		out[t] = Signal{Date: dates[t], Upper: map[string]float64{}, Lower: map[string]float64{},
			UpperDefined: map[string]bool{}, State: map[string]int{}, Weights: map[string]float64{}}
	}
	inRisk := make([]int, n)
	for _, sym := range d.risk {
		close := make([]float64, n)
		high := make([]float64, n)
		low := make([]float64, n)
		for t, b := range aligned[sym] {
			close[t] = b.AdjClose
			high[t] = bars.AdjustedHigh(b)
			low[t] = bars.AdjustedLow(b)
		}
		upper := indicator.DonchianUpper(high, d.params.EntryLookback)
		lower := indicator.DonchianLower(low, d.params.ExitLookback)
		s := 0
		for t := 0; t < n; t++ {
			s = step(s, close[t], upper.Value[t], upper.Defined[t], lower.Value[t])
			out[t].Upper[sym], out[t].Lower[sym], out[t].UpperDefined[sym], out[t].State[sym] = upper.Value[t], lower.Value[t], upper.Defined[t], s
			if s == 1 {
				out[t].Weights[sym] = d.gross / float64(k)
				inRisk[t]++
			} else {
				out[t].Weights[sym] = 0
			}
		}
	}
	for t := range out {
		out[t].Weights[d.haven] = d.gross * float64(k-inRisk[t]) / float64(k)
	}
	return out, nil
}

// step applies one bar of a sleeve: an undefined entry channel forces it
// flat; in the asset, a close strictly below the exit channel leaves; in the
// haven, a close strictly above the entry channel enters. A close exactly on
// a channel leaves the sleeve where it is.
func step(s int, close, upper float64, upperDefined bool, lower float64) int {
	if !upperDefined {
		return 0
	}
	if s == 1 {
		if close < lower {
			return 0
		}
		return 1
	}
	if close > upper {
		return 1
	}
	return 0
}

// Weights is the backtest engine's view of Signals.
func (d *Donchian) Weights(hist map[string][]bars.Bar) ([]map[string]float64, error) {
	sig, err := d.Signals(hist)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]float64, len(sig))
	for i, s := range sig {
		out[i] = s.Weights
	}
	return out, nil
}
