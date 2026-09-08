// Package riskparity is the risk_parity_trend strategy: inverse-volatility
// weights behind a trend filter, long-only, one sleeve per risk asset. Every
// rebalance_days bars the assets above their own moving average pool their
// sleeves and split them in proportion to one over their realised
// volatility; every other sleeve rests in the haven (the universe's last
// symbol). It is the first strategy whose weights are not on/off.
//
// The definition is docs/CONTRACTS.md, implemented to the letter and in the
// stated order of operations, because the same text is implemented in
// Python for research and a golden file checks the two against each other
// row by row (docs/PLAN.md §4.3).
package riskparity

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
const Name = "risk_parity_trend"

func init() { strategy.Register(Name, New) }

// Params is the spec's params block, typed.
type Params struct {
	TrendLookback int
	VolLookback   int
	RebalanceDays int
}

// RiskParity is the strategy for one universe. Immutable once built; the
// weights are replayed from history on every call, never stored.
type RiskParity struct {
	risk   []string
	haven  string
	params Params
	gross  float64
}

// Signal is one aligned bar: per risk asset the average and the volatility
// (Defined false while either window is short), whether it was eligible at
// this bar, whether the bar was a rebalance bar, and the target weights.
type Signal struct {
	Date      time.Time
	SMA, Vol  map[string]float64
	Defined   map[string]bool
	Eligible  map[string]bool
	Rebalance bool
	Weights   map[string]float64
}

// New is the strategy.Factory.
func New(universe []string, params strategy.Params, sizing strategy.Sizing) (strategy.Strategy, error) {
	return Parse(universe, params, sizing)
}

// Parse validates and builds a *RiskParity. Every check is a spec error,
// not a default.
func Parse(universe []string, params strategy.Params, sizing strategy.Sizing) (*RiskParity, error) {
	risk, haven, err := sleeves.Universe("riskparity", universe)
	if err != nil {
		return nil, err
	}
	if err := sleeves.Known("riskparity", params, "trend_lookback", "vol_lookback", "rebalance_days"); err != nil {
		return nil, err
	}
	trend, err := sleeves.WholeNumber("riskparity", "trend_lookback", params["trend_lookback"], 2)
	if err != nil {
		return nil, err
	}
	vol, err := sleeves.WholeNumber("riskparity", "vol_lookback", params["vol_lookback"], 2)
	if err != nil {
		return nil, err
	}
	rb, err := sleeves.WholeNumber("riskparity", "rebalance_days", params["rebalance_days"], 1)
	if err != nil {
		return nil, err
	}
	if !(sizing.GrossLeverage > 0) || math.IsInf(sizing.GrossLeverage, 0) {
		return nil, fmt.Errorf("riskparity: gross_leverage must be > 0, got %v", sizing.GrossLeverage)
	}
	return &RiskParity{risk: risk, haven: haven, params: Params{TrendLookback: trend, VolLookback: vol, RebalanceDays: rb}, gross: sizing.GrossLeverage}, nil
}

// Universe is the spec's order: the risk assets, then the haven.
func (r *RiskParity) Universe() []string { return append(append([]string(nil), r.risk...), r.haven) }

// Params is what the strategy was built with.
func (r *RiskParity) Params() Params { return r.params }

// Targets returns the weights for the last aligned bar. pos is ignored on
// purpose: the weights are replayed from history alone.
func (r *RiskParity) Targets(hist map[string][]bars.Bar, _ []strategy.Position) (map[string]float64, error) {
	sig, err := r.Signals(hist)
	if err != nil {
		return nil, err
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("riskparity: %v share no dates", r.Universe())
	}
	return sig[len(sig)-1].Weights, nil
}

// Signals replays the strategy over the aligned history, one Signal per
// aligned bar, oldest first.
func (r *RiskParity) Signals(hist map[string][]bars.Bar) ([]Signal, error) {
	dates, px, err := sleeves.Align("riskparity", r.Universe(), r.haven, hist)
	if err != nil {
		return nil, err
	}
	n := len(dates)
	k := len(r.risk)
	T, V, R := r.params.TrendLookback, r.params.VolLookback, r.params.RebalanceDays
	W := T
	if V > W {
		W = V
	}
	smas := map[string]indicator.Series{}
	vols := map[string]indicator.Series{}
	for _, sym := range r.risk {
		smas[sym] = indicator.SMA(px[sym], T)
		vols[sym] = indicator.RealisedVol(px[sym], V)
	}
	out := make([]Signal, n)
	cur := map[string]float64{r.haven: r.gross}
	for _, sym := range r.risk {
		cur[sym] = 0
	}
	for t := 0; t < n; t++ {
		sig := Signal{Date: dates[t], SMA: map[string]float64{}, Vol: map[string]float64{}, Defined: map[string]bool{},
			Eligible: map[string]bool{}, Weights: map[string]float64{}, Rebalance: t >= W && (t-W)%R == 0}
		var active []string
		vol := map[string]float64{}
		for _, sym := range r.risk {
			a, okA := smas[sym].At(t)
			v, okV := vols[sym].At(t)
			sig.SMA[sym], sig.Vol[sym], sig.Defined[sym] = a, v, okA && okV
			if okA && okV && px[sym][t] > a && v > 0 {
				sig.Eligible[sym] = true
				active = append(active, sym)
				vol[sym] = v
			}
		}
		if sig.Rebalance {
			cur = WeightsAt(r.risk, r.haven, active, vol, k, r.gross)
		}
		for sym, w := range cur {
			sig.Weights[sym] = w
		}
		out[t] = sig
	}
	return out, nil
}

// WeightsAt is the rebalance-bar arithmetic, in the contract's order: the
// active sleeves' budget G·n/k split by inverse volatility, the rest to the
// haven.
func WeightsAt(risk []string, haven string, active []string, vol map[string]float64, k int, gross float64) map[string]float64 {
	n := len(active)
	raw := map[string]float64{}
	sumRaw := 0.0
	for _, s := range active {
		raw[s] = 1 / vol[s]
		sumRaw += raw[s]
	}
	activeBudget := gross * float64(n) / float64(k)
	out := map[string]float64{}
	for _, s := range risk {
		if w, ok := raw[s]; ok {
			out[s] = activeBudget * w / sumRaw
		} else {
			out[s] = 0
		}
	}
	out[haven] = gross * float64(k-n) / float64(k)
	return out
}

// Weights is the backtest engine's view of Signals.
func (r *RiskParity) Weights(hist map[string][]bars.Bar) ([]map[string]float64, error) {
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
