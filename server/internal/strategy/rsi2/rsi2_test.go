package rsi2

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/paritytest"
)

func goodParams() strategy.Params {
	return strategy.Params{"trend_lookback": 200, "rsi_entry": 5, "rsi_exit": 70, "max_hold_days": 5}
}

func TestParseValidation(t *testing.T) {
	cases := []struct {
		name     string
		universe []string
		edit     func(strategy.Params)
		gross    float64
		wantErr  string
	}{
		{name: "good", universe: []string{"SPY", "QQQ", "GLD"}, gross: 1},
		{name: "haven alone", universe: []string{"GLD"}, gross: 1, wantErr: "at least one risk asset"},
		{name: "repeated symbol", universe: []string{"SPY", "SPY", "GLD"}, gross: 1, wantErr: "twice"},
		{name: "unknown param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["rsi_period"] = 2 }, wantErr: "unknown params rsi_period"},
		{name: "missing param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { delete(p, "rsi_exit") }, wantErr: "missing params rsi_exit"},
		{name: "trend of one", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["trend_lookback"] = 1 }, wantErr: "trend_lookback must be >= 2"},
		{name: "entry above exit", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["rsi_entry"] = 70 }, wantErr: "rsi_entry < rsi_exit"},
		{name: "exit above 100", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["rsi_exit"] = 101 }, wantErr: "rsi_exit <= 100"},
		{name: "negative entry", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["rsi_entry"] = -1 }, wantErr: "0 <= rsi_entry"},
		{name: "fractional hold", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["max_hold_days"] = 2.5 }, wantErr: "whole number"},
		{name: "zero gross", universe: []string{"SPY", "GLD"}, gross: 0, wantErr: "gross_leverage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goodParams()
			if tc.edit != nil {
				tc.edit(p)
			}
			r, err := Parse(tc.universe, p, strategy.Sizing{GrossLeverage: tc.gross})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := strings.Join(r.Universe(), ","); got != strings.Join(tc.universe, ",") {
					t.Errorf("Universe() = %q", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func hist(series map[string][]float64) map[string][]bars.Bar {
	out := map[string][]bars.Bar{}
	for sym, px := range series {
		for i, p := range px {
			out[sym] = append(out[sym], bars.Bar{Date: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC), Close: p, AdjClose: p})
		}
	}
	return out
}

func mustParse(t *testing.T, universe []string, trend int, entry, exit float64, hold int, gross float64) *RSI2 {
	t.Helper()
	r, err := Parse(universe, strategy.Params{"trend_lookback": float64(trend), "rsi_entry": entry, "rsi_exit": exit, "max_hold_days": float64(hold)}, strategy.Sizing{GrossLeverage: gross})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func states(sig []Signal, sym string) []int {
	out := make([]int, len(sig))
	for i, s := range sig {
		out[i] = s.State[sym]
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ones(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

// The same cases as research/tests/test_rsi2.py.
func TestBuysAPullbackInAnUptrendAndSellsTheBounce(t *testing.T) {
	a := []float64{10, 11, 12, 13, 14, 15, 14.2, 13.9, 14.8, 15.5}
	r := mustParse(t, []string{"A", "H"}, 6, 30, 60, 10, 1)
	sig, err := r.Signals(hist(map[string][]float64{"A": a, "H": ones(10)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := states(sig, "A"); !equal(got, make([]int, 10)) {
		t.Errorf("entry 30: states = %v, want none", got)
	}
	if want := 100 - 100/(1+0.25/0.35); math.Abs(sig[7].RSI["A"]-want) > 1e-9 {
		t.Errorf("rsi[7] = %v, want %v", sig[7].RSI["A"], want)
	}
	r = mustParse(t, []string{"A", "H"}, 6, 45, 60, 10, 1)
	sig, _ = r.Signals(hist(map[string][]float64{"A": a, "H": ones(10)}))
	if got := states(sig, "A"); !equal(got, []int{0, 0, 0, 0, 0, 0, 0, 1, 0, 0}) {
		t.Errorf("entry 45: states = %v", got)
	}
	if !(sig[8].RSI["A"] > 60) || !(a[7] > sig[7].SMA["A"]) {
		t.Errorf("rsi[8] = %v, sma[7] = %v", sig[8].RSI["A"], sig[7].SMA["A"])
	}
}

func TestTimeStopAndTrendBreakExit(t *testing.T) {
	r := mustParse(t, []string{"A", "H"}, 6, 45, 99, 2, 1)
	a := []float64{10, 11, 12, 13, 14, 15, 14.2, 13.9, 14.3, 14.6, 14.9, 15.2}
	sig, _ := r.Signals(hist(map[string][]float64{"A": a, "H": ones(12)}))
	if got := states(sig, "A"); !equal(got, []int{0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 0, 0}) {
		t.Errorf("time stop: states = %v", got)
	}
	r = mustParse(t, []string{"A", "H"}, 6, 45, 99, 10, 1)
	a = []float64{10, 11, 12, 13, 14, 15, 14.2, 13.9, 12.0, 12.5}
	sig, _ = r.Signals(hist(map[string][]float64{"A": a, "H": ones(10)}))
	if got := states(sig, "A"); !equal(got, []int{0, 0, 0, 0, 0, 0, 0, 1, 0, 0}) {
		t.Errorf("trend break: states = %v", got)
	}
	if !(a[8] < sig[8].SMA["A"]) || math.Abs(sig[8].RSI["A"]-10) > 1e-9 {
		t.Errorf("bar 8: price %v sma %v rsi %v", a[8], sig[8].SMA["A"], sig[8].RSI["A"])
	}
}

func TestThresholdsAreStrictAndUndefinedInputsRest(t *testing.T) {
	r := mustParse(t, []string{"A", "H"}, 2, 50, 60, 5, 1)
	sig, _ := r.Signals(hist(map[string][]float64{"A": {10, 11, 10, 11.5}, "H": ones(4)}))
	if sig[2].RSI["A"] != 50 || sig[2].State["A"] != 0 {
		t.Errorf("bar 2: rsi %v state %d", sig[2].RSI["A"], sig[2].State["A"])
	}
	if sig[1].Defined["A"] || sig[1].State["A"] != 0 || !sig[2].Defined["A"] {
		t.Errorf("definedness: %+v %+v", sig[1], sig[2])
	}
}

func TestWeightsAndTargets(t *testing.T) {
	r := mustParse(t, []string{"A", "B", "H"}, 6, 45, 60, 5, 0.8)
	h := hist(map[string][]float64{"A": {10, 11, 12, 13, 14, 15, 14.2, 13.9}, "B": {5, 5, 5, 5, 5, 5, 5, 5}, "H": ones(8)})
	sig, _ := r.Signals(h)
	for i, s := range sig {
		if sum := s.Weights["A"] + s.Weights["B"] + s.Weights["H"]; math.Abs(sum-0.8) > 1e-12 {
			t.Errorf("bar %d: weights sum %v", i, sum)
		}
	}
	w, err := r.Targets(h, nil)
	if err != nil || math.Abs(w["A"]-0.4) > 1e-12 || w["B"] != 0 || math.Abs(w["H"]-0.4) > 1e-12 {
		t.Errorf("targets = %v, %v", w, err)
	}
	if _, err := r.Targets(map[string][]bars.Bar{"A": h["A"]}, nil); err == nil {
		t.Error("want an error for a missing symbol")
	}
}

func TestParity(t *testing.T) {
	paritytest.Run(t, "../../../../golden/rsi2_reversion", Name,
		func(u []string, p strategy.Params, s strategy.Sizing) (paritytest.Weighter, error) {
			return Parse(u, p, s)
		})
}
