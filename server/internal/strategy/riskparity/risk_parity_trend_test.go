package riskparity

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
	return strategy.Params{"trend_lookback": 200, "vol_lookback": 63, "rebalance_days": 21}
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-12 }

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
			edit: func(p strategy.Params) { p["band"] = 0 }, wantErr: "unknown params band"},
		{name: "missing param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { delete(p, "vol_lookback") }, wantErr: "missing params vol_lookback"},
		{name: "trend of one", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["trend_lookback"] = 1 }, wantErr: "trend_lookback must be >= 2"},
		{name: "fractional vol", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["vol_lookback"] = 2.5 }, wantErr: "whole number"},
		{name: "zero rebalance", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["rebalance_days"] = 0 }, wantErr: "rebalance_days must be >= 1"},
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

func mustParse(t *testing.T, universe []string, trend, vol, rb int, gross float64) *RiskParity {
	t.Helper()
	r, err := Parse(universe, strategy.Params{"trend_lookback": float64(trend), "vol_lookback": float64(vol), "rebalance_days": float64(rb)}, strategy.Sizing{GrossLeverage: gross})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The same cases as research/tests/test_risk_parity.py.
func TestWeightsAtPoolsActiveSleevesByInverseVol(t *testing.T) {
	w := WeightsAt([]string{"A", "B", "C"}, "H", []string{"A", "B"}, map[string]float64{"A": 0.01, "B": 0.03}, 3, 0.9)
	if !near(w["A"], 0.45) || !near(w["B"], 0.15) || w["C"] != 0 || !near(w["H"], 0.3) {
		t.Errorf("weights = %v", w)
	}
	w = WeightsAt([]string{"A"}, "H", nil, nil, 1, 1)
	if w["A"] != 0 || w["H"] != 1 {
		t.Errorf("no active: %v", w)
	}
}

func TestEligibilityNeedsTrendAndPositiveVol(t *testing.T) {
	r := mustParse(t, []string{"A", "B", "C", "H"}, 2, 2, 1, 1)
	sig, err := r.Signals(hist(map[string][]float64{
		"A": {10, 11, 12, 13, 14}, "B": {5, 5, 5, 5, 5}, "C": {14, 13, 12, 11, 10}, "H": {1, 1, 1, 1, 1},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range sig {
		wantReb := i >= 2
		if s.Rebalance != wantReb || s.Eligible["A"] != wantReb || s.Eligible["B"] || s.Eligible["C"] {
			t.Errorf("bar %d: rebalance %v eligible %v", i, s.Rebalance, s.Eligible)
		}
		wantA, wantH := 0.0, 1.0
		if i >= 2 {
			wantA, wantH = 1.0/3, 2.0/3
		}
		if !near(s.Weights["A"], wantA) || !near(s.Weights["H"], wantH) || s.Weights["B"] != 0 || s.Weights["C"] != 0 {
			t.Errorf("bar %d: weights %v", i, s.Weights)
		}
	}
}

func TestWeightsChangeContinuouslyAndHoldBetweenRebalances(t *testing.T) {
	r := mustParse(t, []string{"A", "B", "H"}, 2, 2, 2, 1)
	sig, _ := r.Signals(hist(map[string][]float64{
		"A": {10, 11, 12.5, 13, 14.5, 15, 16.5}, "B": {10, 10.5, 11, 11.2, 11.5, 11.7, 12}, "H": {1, 1, 1, 1, 1, 1, 1},
	}))
	if !(sig[2].Weights["B"] > sig[2].Weights["A"] && sig[2].Weights["A"] > 0 && sig[2].Weights["H"] == 0) {
		t.Errorf("bar 2: %v", sig[2].Weights)
	}
	same := func(a, b map[string]float64) bool { return a["A"] == b["A"] && a["B"] == b["B"] && a["H"] == b["H"] }
	if !same(sig[3].Weights, sig[2].Weights) || !same(sig[5].Weights, sig[4].Weights) || same(sig[4].Weights, sig[2].Weights) {
		t.Errorf("weights do not hold between rebalances: %v %v %v %v", sig[2].Weights, sig[3].Weights, sig[4].Weights, sig[5].Weights)
	}
	for i, s := range sig {
		sum := 0.0
		for _, w := range s.Weights {
			if w < 0 {
				t.Errorf("bar %d: negative weight %v", i, s.Weights)
			}
			sum += w
		}
		if !near(sum, 1) {
			t.Errorf("bar %d: weights sum %v", i, sum)
		}
	}
}

func TestAllIneligibleRestsInTheHaven(t *testing.T) {
	r := mustParse(t, []string{"A", "B", "H"}, 3, 2, 1, 0.8)
	h := hist(map[string][]float64{"A": {14, 13, 12, 11, 10}, "B": {9, 8, 7, 6, 5}, "H": {1, 1, 1, 1, 1}})
	sig, _ := r.Signals(h)
	for i, s := range sig {
		if s.Weights["A"] != 0 || s.Weights["B"] != 0 || !near(s.Weights["H"], 0.8) {
			t.Errorf("bar %d: %v", i, s.Weights)
		}
	}
	if sig[1].Defined["A"] || !sig[2].Defined["A"] {
		t.Error("definedness is wrong")
	}
	w, err := r.Targets(h, nil)
	if err != nil || !near(w["H"], 0.8) {
		t.Errorf("targets = %v, %v", w, err)
	}
	if _, err := r.Targets(map[string][]bars.Bar{"A": h["A"]}, nil); err == nil {
		t.Error("want an error for a missing symbol")
	}
}

func TestParity(t *testing.T) {
	paritytest.Run(t, "../../../../golden/risk_parity_trend", Name,
		func(u []string, p strategy.Params, s strategy.Sizing) (paritytest.Weighter, error) {
			return Parse(u, p, s)
		})
}
