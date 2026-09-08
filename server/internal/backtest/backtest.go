// Package backtest replays bars through a strategy and a cost model and
// produces an equity curve — the same arithmetic, bar for bar, as the Python
// engine in research/tt/backtest/engine.py (docs/CONTRACTS.md, "Backtest
// engine" and "Execution"), so the two agree to floating-point noise and the
// golden equity curve can say so.
//
// Two fills, chosen by the spec's execution block: the legacy loop reads the
// targets at a bar's close and fills them at that same close; next_open fills
// them at the following bar's adjusted open. Either way the curve is marked
// at every close.
//
// It is deliberately not the paper broker: the broker fills at bid and ask
// with a clock, this fills at a bar's price with none. What the two share is
// the strategy and the cost model, which is the point of the parity check.
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

// Fill is a spec's execution.fill_at.
type Fill string

// The fills the contract defines.
const (
	// FillSameClose fills the targets read at a bar's close at that close.
	FillSameClose Fill = "same_close_legacy"
	// FillNextOpen fills them at the next bar's adjusted open.
	FillNextOpen Fill = "next_open"
)

// ParseFill turns a spec's fill_at into a Fill, refusing anything else.
func ParseFill(s string) (Fill, error) {
	switch Fill(s) {
	case FillSameClose, FillNextOpen:
		return Fill(s), nil
	}
	return "", fmt.Errorf("backtest: unknown fill_at %q", s)
}

// Costs is the spec's cost model.
type Costs struct {
	CommissionUSD float64
	SlippageBps   float64
}

// Point is one bar of the equity curve, at that bar's close.
type Point struct {
	Date   time.Time
	Equity float64
}

// Trade is one rebalance leg: a signed quantity at the fill price.
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
// model applied to the notional traded: the same_close_legacy fill. dates
// and prices are the aligned series (bars.Align), prices keyed by symbol.
func Run(dates []time.Time, prices map[string][]float64, symbols []string, weights Weights, costs Costs) Result {
	n := len(dates)
	b := newBook(len(symbols))
	res := Result{Equity: make([]Point, n)}
	for t := 0; t < n; t++ {
		p := column(prices, symbols, t)
		res.Trades = b.rebalance(weights(t), p, symbols, dates[t], costs, res.Trades)
		res.Equity[t] = Point{Date: dates[t], Equity: b.value(p)}
	}
	return summarize(res)
}

// RunNextOpen fills the targets read at bar t's close at bar t+1's adjusted
// open (docs/CONTRACTS.md, Execution): nothing trades on the first bar, the
// last bar's target is never filled, and the curve is marked at every close.
// closes and opens are the aligned adjclose and adjopen series by symbol.
func RunNextOpen(dates []time.Time, closes, opens map[string][]float64, symbols []string, weights Weights, costs Costs) Result {
	n := len(dates)
	b := newBook(len(symbols))
	res := Result{Equity: make([]Point, n)}
	var pending map[string]float64
	for t := 0; t < n; t++ {
		if pending != nil {
			res.Trades = b.rebalance(pending, column(opens, symbols, t), symbols, dates[t], costs, res.Trades)
		}
		res.Equity[t] = Point{Date: dates[t], Equity: b.value(column(closes, symbols, t))}
		pending = weights(t)
	}
	return summarize(res)
}

// book is cash and shares, in symbol order.
type book struct {
	cash   float64
	shares []float64
}

func newBook(n int) *book { return &book{cash: StartingEquity, shares: make([]float64, n)} }

func (b *book) value(p []float64) float64 { return b.cash + dot(b.shares, p) }

// rebalance moves the book to w at prices p, charging the cost model, and
// appends the legs to trades. The arithmetic and its order are the
// contract's: equity, target shares, delta, traded notional, cost, cash.
func (b *book) rebalance(w map[string]float64, p []float64, symbols []string, date time.Time, costs Costs, trades []Trade) []Trade {
	equity := b.value(p)
	traded, orders, delta := 0.0, 0, make([]float64, len(symbols))
	for i, s := range symbols {
		target := w[s] * equity / p[i]
		delta[i] = target - b.shares[i]
		if delta[i] != 0 {
			traded += math.Abs(delta[i]) * p[i]
			orders++
			trades = append(trades, Trade{Date: date, Symbol: s, Qty: delta[i], Price: p[i]})
		}
		b.shares[i] = target
	}
	cost := traded*costs.SlippageBps/10_000 + costs.CommissionUSD*float64(orders)
	b.cash = b.cash - dot(delta, p) - cost
	return trades
}

func column(series map[string][]float64, symbols []string, t int) []float64 {
	p := make([]float64, len(symbols))
	for i, s := range symbols {
		p[i] = series[s][t]
	}
	return p
}

func summarize(res Result) Result {
	curve := make([]float64, len(res.Equity))
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

// Strategy runs a strategy over a whole history with the legacy fill:
// align, weights per bar, then Run. hist must hold every symbol in the
// strategy's universe.
func Strategy(st strategy.Strategy, hist map[string][]bars.Bar, costs Costs) (Result, error) {
	return StrategyFromFill(st, hist, time.Time{}, costs, FillSameClose)
}

// StrategyFrom is Strategy with a warm-up: the strategy sees the whole
// history, so its state and rolling windows are ready, but the book opens
// and the equity curve starts at the first bar on or after from. A zero from
// starts at the first bar.
func StrategyFrom(st strategy.Strategy, hist map[string][]bars.Bar, from time.Time, costs Costs) (Result, error) {
	return StrategyFromFill(st, hist, from, costs, FillSameClose)
}

// StrategyFromFill is StrategyFrom with the fill chosen by the spec. Under
// FillNextOpen the run opens at from with nothing pending, so its first fill
// is at the second bar's open, as the contract says.
func StrategyFromFill(st strategy.Strategy, hist map[string][]bars.Bar, from time.Time, costs Costs, fill Fill) (Result, error) {
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
	closes := map[string][]float64{}
	opens := map[string][]float64{}
	for s, series := range aligned {
		c := make([]float64, len(series))
		o := make([]float64, len(series))
		for i, b := range series {
			c[i] = b.AdjClose
			if fill == FillNextOpen {
				o[i] = bars.AdjustedOpen(b)
			}
		}
		closes[s], opens[s] = c, o
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
	window := func(series map[string][]float64) map[string][]float64 {
		out := map[string][]float64{}
		for s, px := range series {
			out[s] = px[start:]
		}
		return out
	}
	weights := func(t int) map[string]float64 { return perBar[start+t] }
	switch fill {
	case FillNextOpen:
		return RunNextOpen(dates[start:], window(closes), window(opens), symbols, weights, costs), nil
	case FillSameClose:
		return Run(dates[start:], window(closes), symbols, weights, costs), nil
	}
	return Result{}, fmt.Errorf("backtest: unknown fill %q", fill)
}
