// Package metrics turns an equity curve into the three numbers a strategy is
// judged by: annualized Sharpe, maximum drawdown and its longest duration.
//
// The definitions are docs/CONTRACTS.md, "Metrics", to the letter, because
// the Python side computes the same numbers from the same curves and a golden
// file holds the two to 1e-6. Everything here is a pure function of a slice.
package metrics

import (
	"errors"
	"math"
)

// TradingDays is the annualization base.
const TradingDays = 252

// DailyReturns is e_t / e_{t-1} - 1 for t >= 1.
func DailyReturns(equity []float64) []float64 {
	if len(equity) < 2 {
		return nil
	}
	out := make([]float64, len(equity)-1)
	for i := 1; i < len(equity); i++ {
		out[i-1] = equity[i]/equity[i-1] - 1
	}
	return out
}

// ErrUndefined is Sharpe's answer when there are too few returns or no
// variance to divide by.
var ErrUndefined = errors.New("metrics: sharpe is undefined")

// Sharpe is mean(r) / sd(r) * sqrt(252) with the sample standard deviation
// (ddof = 1) and a zero risk-free rate.
func Sharpe(returns []float64) (float64, error) {
	n := len(returns)
	if n < 2 {
		return 0, ErrUndefined
	}
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(n)
	ss := 0.0
	for _, r := range returns {
		d := r - mean
		ss += d * d
	}
	sd := math.Sqrt(ss / float64(n-1))
	if sd == 0 || math.IsNaN(sd) {
		return 0, ErrUndefined
	}
	return mean / sd * math.Sqrt(TradingDays), nil
}

// MaxDrawdown is min over t of e_t / max_{s<=t} e_s - 1: zero for a curve
// that never dips, negative otherwise.
func MaxDrawdown(equity []float64) float64 {
	if len(equity) == 0 {
		return 0
	}
	peak := equity[0]
	worst := 0.0
	for _, e := range equity {
		if e > peak {
			peak = e
		}
		if dd := e/peak - 1; dd < worst {
			worst = dd
		}
	}
	return worst
}

// MaxDrawdownDuration is the longest run of consecutive days on which the
// equity sat below its running high — the most days spent under water before
// a previous high was exceeded. A dip that never recovers counts to the end.
func MaxDrawdownDuration(equity []float64) int {
	if len(equity) == 0 {
		return 0
	}
	peak := equity[0]
	longest, run := 0, 0
	for _, e := range equity {
		if e > peak {
			peak = e
		}
		if e < peak {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return longest
}

// Summary bundles the three.
type Summary struct {
	Sharpe              float64 `json:"sharpe"`
	MaxDrawdown         float64 `json:"max_drawdown"`
	MaxDrawdownDuration int     `json:"max_drawdown_duration"`
	// Days is how many equity points went in.
	Days int `json:"days"`
	// TotalReturn is the last point over the first, minus one.
	TotalReturn float64 `json:"total_return"`
}

// Summarize computes every metric from one curve. A curve too short or too
// flat for a Sharpe is the error; the drawdown numbers need only one point.
func Summarize(equity []float64) (Summary, error) {
	s := Summary{Days: len(equity), MaxDrawdown: MaxDrawdown(equity), MaxDrawdownDuration: MaxDrawdownDuration(equity)}
	if len(equity) > 0 && equity[0] != 0 {
		s.TotalReturn = equity[len(equity)-1]/equity[0] - 1
	}
	sharpe, err := Sharpe(DailyReturns(equity))
	if err != nil {
		return s, err
	}
	s.Sharpe = sharpe
	return s, nil
}
