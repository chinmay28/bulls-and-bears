// Package tsmomentum is the time_series_momentum strategy: every
// rebalance_days bars each risk asset is held if its own trailing
// lookback-bar return is positive and rests in the haven (the universe's
// last symbol) otherwise. No sleeve looks at another asset or at the haven's
// return, which is what separates it from dual_momentum; the threshold is
// zero and not a parameter.
//
// The definition is docs/CONTRACTS.md, implemented to the letter, because
// the same text is implemented in Python for research and a golden file
// checks the two against each other row by row (docs/PLAN.md §4.3).
package tsmomentum

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/indicator"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/sleeves"
)

// Name is what a spec's `strategy` field says to get this implementation.
const Name = "time_series_momentum"

func init() { strategy.Register(Name, New) }

// Params is the spec's params block, typed.
type Params struct {
	Lookback      int
	RebalanceDays int
}

// TSMomentum is the strategy for one universe. Immutable once built; the
// sleeve states are replayed from history on every call, never stored.
type TSMomentum struct {
	risk   []string
	haven  string
	params Params
	gross  float64
}

// Signal is one aligned bar: per risk asset the trailing return (Defined
// false while the window is short) and the sleeve's state after this bar,
// whether the bar was a rebalance bar, and the target weights.
type Signal struct {
	Date      time.Time
	Return    map[string]float64
	Defined   map[string]bool
	State     map[string]int
	Rebalance bool
	Weights   map[string]float64
}

// New is the strategy.Factory.
func New(universe []string, params strategy.Params, sizing strategy.Sizing) (strategy.Strategy, error) {
	return Parse(universe, params, sizing)
}

// Parse validates and builds a *TSMomentum. Every check is a spec error,
// not a default.
func Parse(universe []string, params strategy.Params, sizing strategy.Sizing) (*TSMomentum, error) {
	risk, haven, err := sleeves.Universe("tsmomentum", universe)
	if err != nil {
		return nil, err
	}
	if err := sleeves.Known("tsmomentum", params, "lookback", "rebalance_days"); err != nil {
		return nil, err
	}
	lb, err := sleeves.WholeNumber("tsmomentum", "lookback", params["lookback"], 1)
	if err != nil {
		return nil, err
	}
	rb, err := sleeves.WholeNumber("tsmomentum", "rebalance_days", params["rebalance_days"], 1)
	if err != nil {
		return nil, err
	}
	if !(sizing.GrossLeverage > 0) || math.IsInf(sizing.GrossLeverage, 0) {
		return nil, fmt.Errorf("tsmomentum: gross_leverage must be > 0, got %v", sizing.GrossLeverage)
	}
	return &TSMomentum{risk: risk, haven: haven, params: Params{Lookback: lb, RebalanceDays: rb}, gross: sizing.GrossLeverage}, nil
}

// Universe is the spec's order: the risk assets, then the haven.
func (m *TSMomentum) Universe() []string { return append(append([]string(nil), m.risk...), m.haven) }

// Params is what the strategy was built with.
func (m *TSMomentum) Params() Params { return m.params }

// Targets returns the weights for the last aligned bar. pos is ignored on
// purpose: the state is replayed from history alone.
func (m *TSMomentum) Targets(hist map[string][]bars.Bar, _ []strategy.Position) (map[string]float64, error) {
	sig, err := m.Signals(hist)
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("tsmomentum: %v share no dates", m.Universe())
	}
	return sig[len(sig)-1].Weights, nil
}

// Signals replays the strategy over the aligned history, one Signal per
// aligned bar, oldest first.
func (m *TSMomentum) Signals(hist map[string][]bars.Bar) ([]Signal, error) {
	dates, px, err := sleeves.Align("tsmomentum", m.Universe(), m.haven, hist)
	if err != nil {
		return nil, err
	}
	n := len(dates)
	k := len(m.risk)
	out := make([]Signal, n)
	lb, rb := m.params.Lookback, m.params.RebalanceDays
	for t := range out {
		out[t] = Signal{Date: dates[t], Return: map[string]float64{}, Defined: map[string]bool{},
			State: map[string]int{}, Weights: map[string]float64{}, Rebalance: t >= lb && (t-lb)%rb == 0}
	}
	inRisk := make([]int, n)
	for _, sym := range m.risk {
		ret := indicator.RollingReturn(px[sym], lb)
		s := 0
		for t := 0; t < n; t++ {
			if out[t].Rebalance {
				s = 0
				if ret.Defined[t] && ret.Value[t] > 0 {
					s = 1
				}
			}
			out[t].Return[sym], out[t].Defined[sym], out[t].State[sym] = ret.Value[t], ret.Defined[t], s
			if s == 1 {
				out[t].Weights[sym] = m.gross / float64(k)
				inRisk[t]++
			} else {
				out[t].Weights[sym] = 0
			}
		}
	}
	for t := range out {
		out[t].Weights[m.haven] = m.gross * float64(k-inRisk[t]) / float64(k)
	}
	return out, nil
}

// Weights is the backtest engine's view of Signals.
func (m *TSMomentum) Weights(hist map[string][]bars.Bar) ([]map[string]float64, error) {
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

// String names the strategy and its parameters, for logs.
func (m *TSMomentum) String() string {
	return fmt.Sprintf("%s(%s | %s; lookback %d, every %d)", Name, strings.Join(m.risk, ","), m.haven, m.params.Lookback, m.params.RebalanceDays)
}

var _ = sort.Strings
