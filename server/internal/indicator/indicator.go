// Package indicator is the rolling calculations the strategies share:
// means, sample deviations and z-scores, returns, moving averages, Donchian
// channels, realised volatility, true range and ATR, and Wilder's RSI.
//
// Each is a pure function of one series, oldest first, returning one value
// per bar and a flag saying whether the contract defines it there. The
// definitions are docs/CONTRACTS.md "Indicators", implemented to the letter
// and in the same order of operations as research/tt/indicators, and the
// fixtures under golden/indicators hold the two sides to 1e-9. Nothing here
// keeps state between calls.
package indicator

import "math"

// Series is an indicator's output: Value[t] is meaningful only where
// Defined[t] is true.
type Series struct {
	Value   []float64
	Defined []bool
}

func newSeries(n int) Series { return Series{Value: make([]float64, n), Defined: make([]bool, n)} }

// At returns the value at t and whether it is defined.
func (s Series) At(t int) (float64, bool) { return s.Value[t], s.Defined[t] }

// RollingMean is the mean over the window of L ending at t, undefined for
// t < L−1. The window is summed from scratch every bar: the same numbers to
// well inside the golden tolerance as pandas' rolling mean, and no drift.
func RollingMean(x []float64, L int) Series {
	out := newSeries(len(x))
	if L < 2 {
		return out
	}
	for t := L - 1; t < len(x); t++ {
		out.Value[t] = mean(x[t+1-L : t+1])
		out.Defined[t] = true
	}
	return out
}

// RollingSampleSD is the sample standard deviation (ddof = 1) over the
// window of L ending at t, undefined for t < L−1.
func RollingSampleSD(x []float64, L int) Series {
	out := newSeries(len(x))
	if L < 2 {
		return out
	}
	for t := L - 1; t < len(x); t++ {
		out.Value[t] = sampleSD(x[t+1-L : t+1])
		out.Defined[t] = true
	}
	return out
}

// RollingZScore is (x_t − mean_t) / sd_t, undefined while the window is
// short or the sd is exactly zero.
func RollingZScore(x []float64, L int) Series {
	out := newSeries(len(x))
	if L < 2 {
		return out
	}
	for t := L - 1; t < len(x); t++ {
		win := x[t+1-L : t+1]
		m := mean(win)
		sd := sampleSD(win)
		if sd > 0 {
			out.Value[t] = (x[t] - m) / sd
			out.Defined[t] = true
		}
	}
	return out
}

// RollingReturn is p_t / p_{t−L} − 1, undefined for t < L.
func RollingReturn(px []float64, L int) Series {
	out := newSeries(len(px))
	if L < 1 {
		return out
	}
	for t := L; t < len(px); t++ {
		out.Value[t] = px[t]/px[t-L] - 1
		out.Defined[t] = true
	}
	return out
}

// SMA is (C_t − C_{t−L}) / L from the in-order running sum, undefined for
// t < L−1: the one form both sides evaluate in exactly the same order.
func SMA(px []float64, L int) Series {
	n := len(px)
	out := newSeries(n)
	if L < 2 || n < L {
		return out
	}
	cs := make([]float64, n)
	sum := 0.0
	for i, p := range px {
		sum += p
		cs[i] = sum
	}
	for t := L - 1; t < n; t++ {
		if t == L-1 {
			out.Value[t] = cs[t] / float64(L)
		} else {
			out.Value[t] = (cs[t] - cs[t-L]) / float64(L)
		}
		out.Defined[t] = true
	}
	return out
}

// DonchianUpper is max(high_{t−L} … high_{t−1}): the prior L bars, never
// the current one. Undefined for t < L.
func DonchianUpper(high []float64, L int) Series {
	return priorExtreme(high, L, math.Max)
}

// DonchianLower is min(low_{t−L} … low_{t−1}), undefined for t < L.
func DonchianLower(low []float64, L int) Series {
	return priorExtreme(low, L, math.Min)
}

func priorExtreme(x []float64, L int, pick func(a, b float64) float64) Series {
	out := newSeries(len(x))
	if L < 1 {
		return out
	}
	for t := L; t < len(x); t++ {
		v := x[t-L]
		for _, u := range x[t-L+1 : t] {
			v = pick(v, u)
		}
		out.Value[t] = v
		out.Defined[t] = true
	}
	return out
}

// RealisedVol is the sample sd of the daily simple returns over the L
// returns ending at t, undefined for t < L. Not annualised.
func RealisedVol(px []float64, L int) Series {
	n := len(px)
	out := newSeries(n)
	if n < 2 || L < 2 {
		return out
	}
	r := make([]float64, n-1)
	for i := range r {
		r[i] = px[i+1]/px[i] - 1
	}
	sd := RollingSampleSD(r, L)
	for i := range r {
		out.Value[i+1], out.Defined[i+1] = sd.Value[i], sd.Defined[i]
	}
	return out
}

// TrueRange is max(h − l, |h − c_{t−1}|, |l − c_{t−1}|) on adjusted
// prices, undefined at t = 0.
func TrueRange(high, low, close []float64) Series {
	n := len(close)
	out := newSeries(n)
	for t := 1; t < n; t++ {
		out.Value[t] = math.Max(high[t]-low[t], math.Max(math.Abs(high[t]-close[t-1]), math.Abs(low[t]-close[t-1])))
		out.Defined[t] = true
	}
	return out
}

// ATR is Wilder's average true range over N: the plain mean of the first N
// true ranges, then (prev · (N−1) + TR) / N. Undefined for t < N.
func ATR(high, low, close []float64, N int) Series {
	tr := TrueRange(high, low, close)
	n := len(tr.Value)
	out := newSeries(n)
	if N < 1 || n <= N {
		return out
	}
	acc := 0.0
	for t := 1; t <= N; t++ {
		acc += tr.Value[t]
	}
	cur := acc / float64(N)
	out.Value[N], out.Defined[N] = cur, true
	for t := N + 1; t < n; t++ {
		cur = (cur*float64(N-1) + tr.Value[t]) / float64(N)
		out.Value[t], out.Defined[t] = cur, true
	}
	return out
}

// WilderRSI is the RSI over N daily changes, undefined for t < N. The
// arithmetic is the contract's, in its order, bar by bar, so the Python
// side agrees bit for bit.
func WilderRSI(px []float64, N int) Series {
	n := len(px)
	out := newSeries(n)
	if N < 1 || n <= N {
		return out
	}
	gain, loss := 0.0, 0.0
	for t := 1; t <= N; t++ {
		d := px[t] - px[t-1]
		if d > 0 {
			gain += d
		} else if d < 0 {
			loss += -d
		}
	}
	gain /= float64(N)
	loss /= float64(N)
	out.Value[N], out.Defined[N] = rsi(gain, loss), true
	for t := N + 1; t < n; t++ {
		d := px[t] - px[t-1]
		up, down := 0.0, 0.0
		if d > 0 {
			up = d
		} else if d < 0 {
			down = -d
		}
		gain = (gain*float64(N-1) + up) / float64(N)
		loss = (loss*float64(N-1) + down) / float64(N)
		out.Value[t], out.Defined[t] = rsi(gain, loss), true
	}
	return out
}

func rsi(gain, loss float64) float64 {
	if loss == 0 {
		if gain > 0 {
			return 100
		}
		return 50
	}
	rs := gain / loss
	return 100 - 100/(1+rs)
}

func mean(win []float64) float64 {
	sum := 0.0
	for _, v := range win {
		sum += v
	}
	return sum / float64(len(win))
}

func sampleSD(win []float64) float64 {
	m := mean(win)
	ss := 0.0
	for _, v := range win {
		d := v - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(win)-1))
}
