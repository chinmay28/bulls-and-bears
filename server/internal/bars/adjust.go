package bars

import "fmt"

// AdjustFactor is adjclose / close for one bar (docs/CONTRACTS.md, Adjusted
// OHLC): the split-and-dividend adjustment that turns a raw open, high or
// low onto adjclose's footing. A non-positive close is a data error the
// invariants should have caught; it panics rather than yielding a factor of
// zero, because a strategy fed one would silently see a crash.
func AdjustFactor(b Bar) float64 {
	if !(b.Close > 0) {
		panic(fmt.Sprintf("bars: %s close %v is not positive", b.Date.Format("2006-01-02"), b.Close))
	}
	return b.AdjClose / b.Close
}

// AdjustedOpen is open · f, f the AdjustFactor; the factor is computed first
// and multiplied once, as the Python side does, so the two agree bit for bit.
func AdjustedOpen(b Bar) float64 { return b.Open * AdjustFactor(b) }

// AdjustedHigh is high · f.
func AdjustedHigh(b Bar) float64 { return b.High * AdjustFactor(b) }

// AdjustedLow is low · f.
func AdjustedLow(b Bar) float64 { return b.Low * AdjustFactor(b) }
