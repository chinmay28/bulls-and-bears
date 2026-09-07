// Package backtest replays bars through a strategy and a cost model and
// produces an equity curve — the same arithmetic, bar for bar, as the Python
// engine in research/tt/backtest/engine.py (docs/CONTRACTS.md, "Backtest
// engine"), so the two agree to floating-point noise and the golden equity
// curve can say so.
//
// It is deliberately not the paper broker: the broker fills at bid and ask
// with a clock, this fills at the close with none. What the two share is the
// strategy and the cost model, which is the point of the parity check.
package backtest

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/metrics"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// StartingEquity is what every backtest opens with.
const StartingEquity = 10_000.0

// Costs is the spec's cost model.
type Costs struct {
	CommissionUSD float64
	SlippageBps   float64
}

// Point is one bar of the equity curve, after that bar's rebalance.
type Point struct {
	Date   time.Time
	Equity float64
}

// Trade is one rebalance leg: a signed quantity at the bar's close.
type Trade struct {
	Date   time.Time
	Symbol string
	Qty    float64
	Price  float64
}

// Result is the curve, the trades and the numbers.
type Result struct {
	Equity  []Point
	Trades  []Trade
	Summary metrics.Summary
	// SummaryErr is set when the Sharpe is undefined (a curve that never
	// moved); the drawdown numbers are still filled in.
	SummaryErr error
}

// Weights is a strategy's targets for every aligned bar, as the Python
// side's replay produces them: one map per bar, symbol -> signed weight.
type Weights func(t int) map[string]float64

// Run rebalances to weights(t) at each aligned bar's adjclose with the cost
// model applied to the notional traded. dates and prices are the aligned
// series (bars.Align), prices keyed by symbol.
func Run(dates []time.Time, prices map[string][]float64, symbols []string, weights Weights, costs Costs) Result {
	n := len(dates)
	cash := StartingEquity
	shares := make([]float64, len(symbols))
	res := Result{Equity: make([]Point, n)}
	for t := 0; t < n; t++ {
		p := make([]float64, len(symbols))
		for i, s := range symbols {
			p[i] = prices[s][t]
		}
		equity := cash + dot(shares, p)
		w := weights(t)
		traded, orders, delta := 0.0, 0, make([]float64, len(symbols))
		for i, s := range symbols {
			target := w[s] * equity / p[i]
			delta[i] = target - shares[i]
			if delta[i] != 0 {
				traded += math.Abs(delta[i]) * p[i]
				orders++
				res.Trades = append(res.Trades, Trade{Date: dates[t], Symbol: s, Qty: delta[i], Price: p[i]})
			}
			shares[i] = target
		}
		cost := traded*costs.SlippageBps/10_000 + costs.CommissionUSD*float64(orders)
		cash = cash - dot(delta, p) - cost
		res.Equity[t] = Point{Date: dates[t], Equity: cash + dot(shares, p)}
	}
	curve := make([]float64, n)
	for i, pt := range res.Equity {
		curve[i] = pt.Equity
	}
	res.Summary, res.SummaryErr = metrics.Summarize(curve)
	return res
}

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// Replayer is a strategy that can report its targets for every bar of a
// history at once, which is what a backtest wants and what pairs.Signals
// does. A strategy without it is replayed by calling Targets on a growing
// prefix, which is correct and O(n²).
type Replayer interface {
	Weights(hist map[string][]bars.Bar) ([]map[string]float64, error)
}

// Strategy runs a strategy over a whole history: align, weights per bar,
// then Run. hist must hold every symbol in the strategy's universe.
func Strategy(st strategy.Strategy, hist map[string][]bars.Bar, costs Costs) (Result, error) {
	return StrategyFrom(st, hist, time.Time{}, costs)
}

// StrategyFrom is Strategy with a warm-up: the strategy sees the whole
// history, so its state and rolling windows are ready, but the book opens
// and the equity curve starts at the first bar on or after from. A zero from
// starts at the first bar.
func StrategyFrom(st strategy.Strategy, hist map[string][]bars.Bar, from time.Time, costs Costs) (Result, error) {
	universe := st.Universe()
	sub := map[string][]bars.Bar{}
	for _, s := range universe {
		series, ok := hist[s]
		if !ok || len(series) == 0 {
			return Result{}, fmt.Errorf("backtest: no bars for %s", s)
		}
		sub[s] = series
	}
	dates, aligned := bars.Align(sub)
	if len(dates) == 0 {
		return Result{}, fmt.Errorf("backtest: %v share no dates", universe)
	}
	prices := map[string][]float64{}
	for s, series := range aligned {
		px := make([]float64, len(series))
		for i, b := range series {
			px[i] = b.AdjClose
		}
		prices[s] = px
	}

	var perBar []map[string]float64
	if r, ok := st.(Replayer); ok {
		w, err := r.Weights(aligned)
		if err != nil {
			return Result{}, err
		}
		perBar = w
	} else {
		perBar = make([]map[string]float64, len(dates))
		for t := range dates {
			prefix := map[string][]bars.Bar{}
			for s, series := range aligned {
				prefix[s] = series[:t+1]
			}
			w, err := st.Targets(prefix, nil)
			if err != nil {
				return Result{}, fmt.Errorf("backtest: bar %s: %w", dates[t].Format("2006-01-02"), err)
			}
			perBar[t] = w
		}
	}
	if len(perBar) != len(dates) {
		return Result{}, fmt.Errorf("backtest: strategy gave %d bars of weights for %d dates", len(perBar), len(dates))
	}
	symbols := append([]string(nil), universe...)
	sort.Strings(symbols)

	start := 0
	for start < len(dates) && dates[start].Before(from) {
		start++
	}
	if start == len(dates) {
		return Result{}, fmt.Errorf("backtest: no bars on or after %s", from.Format("2006-01-02"))
	}
	window := map[string][]float64{}
	for s, px := range prices {
		window[s] = px[start:]
	}
	return Run(dates[start:], window, symbols, func(t int) map[string]float64 { return perBar[start+t] }, costs), nil
}
