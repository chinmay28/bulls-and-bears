// Package rsi2 is the rsi2_reversion strategy: Connors' two-period RSI
// pullback, long-only, one sleeve per risk asset. While the asset closes
// above its long moving average, a sleeve buys it when the two-bar RSI says
// it is oversold and sells it back for the haven (the universe's last
// symbol) once the RSI has recovered, the time stop is reached, or the close
// falls below the average. The RSI period is two and is not a parameter.
//
// The definition is docs/CONTRACTS.md, implemented to the letter, because
// the same text is implemented in Python for research and a golden file
// checks the two against each other row by row (docs/PLAN.md §4.3).
package rsi2

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
const Name = "rsi2_reversion"

// RSIPeriod is fixed by the contract.
const RSIPeriod = 2

func init() { strategy.Register(Name, New) }

// Params is the spec's params block, typed.
type Params struct {
	TrendLookback int
	RSIEntry      float64
	RSIExit       float64
	MaxHoldDays   int
}

// RSI2 is the strategy for one universe. Immutable once built; the sleeve
// states are replayed from history on every call, never stored.
type RSI2 struct {
	risk   []string
	haven  string
	params Params
	gross  float64
}

// Signal is one aligned bar: per risk asset the RSI and the average
// (Defined false while either is short), the sleeve's state after this bar,
// and the target weights for every symbol.
type Signal struct {
	Date     time.Time
	RSI, SMA map[string]float64
	Defined  map[string]bool
	State    map[string]int
	Weights  map[string]float64
}

// New is the strategy.Factory.
func New(universe []string, params strategy.Params, sizing strategy.Sizing) (strategy.Strategy, error) {
	return Parse(universe, params, sizing)
}

// Parse validates and builds a *RSI2. Every check is a spec error, not a
// default.
func Parse(universe []string, params strategy.Params, sizing strategy.Sizing) (*RSI2, error) {
	risk, haven, err := sleeves.Universe("rsi2", universe)
	if err != nil {
		return nil, err
	}
	if err := sleeves.Known("rsi2", params, "trend_lookback", "rsi_entry", "rsi_exit", "max_hold_days"); err != nil {
		return nil, err
	}
	trend, err := sleeves.WholeNumber("rsi2", "trend_lookback", params["trend_lookback"], 2)
	if err != nil {
		return nil, err
	}
	entry, exit := params["rsi_entry"], params["rsi_exit"]
	if math.IsNaN(entry) || math.IsNaN(exit) || !(0 <= entry && entry < exit && exit <= 100) {
		return nil, fmt.Errorf("rsi2: need 0 <= rsi_entry < rsi_exit <= 100, got %v and %v", entry, exit)
	}
	hold, err := sleeves.WholeNumber("rsi2", "max_hold_days", params["max_hold_days"], 1)
	if err != nil {
		return nil, err
	}
	if !(sizing.GrossLeverage > 0) || math.IsInf(sizing.GrossLeverage, 0) {
		return nil, fmt.Errorf("rsi2: gross_leverage must be > 0, got %v", sizing.GrossLeverage)
	}
	return &RSI2{risk: risk, haven: haven, params: Params{TrendLookback: trend, RSIEntry: entry, RSIExit: exit, MaxHoldDays: hold}, gross: sizing.GrossLeverage}, nil
}

// Universe is the spec's order: the risk assets, then the haven.
func (r *RSI2) Universe() []string { return append(append([]string(nil), r.risk...), r.haven) }

// Params is what the strategy was built with.
func (r *RSI2) Params() Params { return r.params }

// Targets returns the weights for the last aligned bar. pos is ignored on
// purpose: the state is replayed from history alone.
func (r *RSI2) Targets(hist map[string][]bars.Bar, _ []strategy.Position) (map[string]float64, error) {
	sig, err := r.Signals(hist)
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("rsi2: %v share no dates", r.Universe())
	}
	return sig[len(sig)-1].Weights, nil
}

// Signals replays the strategy over the aligned history, one Signal per
// aligned bar, oldest first.
func (r *RSI2) Signals(hist map[string][]bars.Bar) ([]Signal, error) {
	dates, px, err := sleeves.Align("rsi2", r.Universe(), r.haven, hist)
	if err != nil {
		return nil, err
	}
	n := len(dates)
	k := len(r.risk)
	out := make([]Signal, n)
	for t := range out {
		out[t] = Signal{Date: dates[t], RSI: map[string]float64{}, SMA: map[string]float64{},
			Defined: map[string]bool{}, State: map[string]int{}, Weights: map[string]float64{}}
	}
	inRisk := make([]int, n)
	for _, sym := range r.risk {
		rsi := indicator.WilderRSI(px[sym], RSIPeriod)
		avg := indicator.SMA(px[sym], r.params.TrendLookback)
		s, held := 0, 0
		for t := 0; t < n; t++ {
			defined := rsi.Defined[t] && avg.Defined[t]
			s, held = step(s, held, px[sym][t], rsi.Value[t], avg.Value[t], defined, r.params)
			out[t].RSI[sym], out[t].SMA[sym], out[t].Defined[sym], out[t].State[sym] = rsi.Value[t], avg.Value[t], defined, s
			if s == 1 {
				out[t].Weights[sym] = r.gross / float64(k)
				inRisk[t]++
			} else {
				out[t].Weights[sym] = 0
			}
		}
	}
	for t := range out {
		out[t].Weights[r.haven] = r.gross * float64(k-inRisk[t]) / float64(k)
	}
	return out, nil
}

// step applies one bar of a sleeve's state machine: exits before entries,
// never both. Every comparison is strict; undefined inputs force the haven.
func step(s, held int, price, rsi, avg float64, defined bool, p Params) (int, int) {
	if !defined {
		return 0, 0
	}
	if s == 1 {
		if rsi > p.RSIExit || held >= p.MaxHoldDays || price < avg {
			return 0, 0
		}
		return 1, held + 1
	}
	if price > avg && rsi < p.RSIEntry {
		return 1, 0
	}
	return 0, 0
}

// Weights is the backtest engine's view of Signals.
func (r *RSI2) Weights(hist map[string][]bars.Bar) ([]map[string]float64, error) {
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
