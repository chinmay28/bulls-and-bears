package tsmomentum

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/paritytest"
)

func goodParams() strategy.Params { return strategy.Params{"lookback": 252, "rebalance_days": 21} }

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
			edit: func(p strategy.Params) { p["top_k"] = 1 }, wantErr: "unknown params top_k"},
		{name: "missing param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { delete(p, "rebalance_days") }, wantErr: "missing params rebalance_days"},
		{name: "zero lookback", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["lookback"] = 0 }, wantErr: "lookback must be >= 1"},
		{name: "fractional rebalance", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["rebalance_days"] = 2.5 }, wantErr: "whole number"},
		{name: "zero gross", universe: []string{"SPY", "GLD"}, gross: 0, wantErr: "gross_leverage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goodParams()
			if tc.edit != nil {
				tc.edit(p)
			}
			m, err := Parse(tc.universe, p, strategy.Sizing{GrossLeverage: tc.gross})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := strings.Join(m.Universe(), ","); got != strings.Join(tc.universe, ",") {
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

func states(t *testing.T, sig []Signal, sym string) []int {
	t.Helper()
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

// The same cases as research/tests/test_time_series_momentum.py.
func TestRisingFallingAndMixed(t *testing.T) {
	m, _ := Parse([]string{"A", "B", "H"}, strategy.Params{"lookback": 2, "rebalance_days": 1}, strategy.Sizing{GrossLeverage: 1})
	sig, err := m.Signals(hist(map[string][]float64{"A": {10, 11, 12, 13, 14}, "B": {14, 13, 12, 11, 10}, "H": {1, 1, 1, 1, 1}}))
	if err != nil {
		t.Fatal(err)
	}
	if !equal(states(t, sig, "A"), []int{0, 0, 1, 1, 1}) || !equal(states(t, sig, "B"), []int{0, 0, 0, 0, 0}) {
		t.Errorf("A %v B %v", states(t, sig, "A"), states(t, sig, "B"))
	}
	for i, want := range []float64{1, 1, 0.5, 0.5, 0.5} {
		if sig[i].Weights["H"] != want {
			t.Errorf("bar %d: haven %v, want %v", i, sig[i].Weights["H"], want)
		}
	}
}

func TestZeroReturnRestsInTheHaven(t *testing.T) {
	m, _ := Parse([]string{"A", "H"}, strategy.Params{"lookback": 1, "rebalance_days": 1}, strategy.Sizing{GrossLeverage: 1})
	sig, _ := m.Signals(hist(map[string][]float64{"A": {10, 10, 11, 11}, "H": {1, 1, 1, 1}}))
	if got := states(t, sig, "A"); !equal(got, []int{0, 0, 1, 0}) {
		t.Errorf("states = %v", got)
	}
}

func TestSignalChangesWaitForARebalanceBar(t *testing.T) {
	a := map[string][]float64{"A": {10, 11, 12, 9, 8, 10, 11, 9}, "H": {1, 1, 1, 1, 1, 1, 1, 1}}
	m, _ := Parse([]string{"A", "H"}, strategy.Params{"lookback": 2, "rebalance_days": 3}, strategy.Sizing{GrossLeverage: 1})
	sig, _ := m.Signals(hist(a))
	if got := states(t, sig, "A"); !equal(got, []int{0, 0, 1, 1, 1, 1, 1, 1}) {
		t.Errorf("every 3: states = %v", got)
	}
	if !sig[2].Rebalance || sig[3].Rebalance || !sig[5].Rebalance {
		t.Error("rebalance flags are wrong")
	}
	m, _ = Parse([]string{"A", "H"}, strategy.Params{"lookback": 2, "rebalance_days": 1}, strategy.Sizing{GrossLeverage: 1})
	sig, _ = m.Signals(hist(a))
	if got := states(t, sig, "A"); !equal(got, []int{0, 0, 1, 0, 0, 1, 1, 0}) {
		t.Errorf("every bar: states = %v", got)
	}
}

func TestShortHistoryAndHavenIndependence(t *testing.T) {
	m, _ := Parse([]string{"A", "H"}, strategy.Params{"lookback": 10, "rebalance_days": 1}, strategy.Sizing{GrossLeverage: 1})
	sig, _ := m.Signals(hist(map[string][]float64{"A": {10, 11, 12}, "H": {5, 1, 9}}))
	for i, s := range sig {
		if s.State["A"] != 0 || s.Defined["A"] || s.Weights["H"] != 1 {
			t.Errorf("bar %d: %+v", i, s)
		}
	}
	m, _ = Parse([]string{"A", "H"}, strategy.Params{"lookback": 1, "rebalance_days": 1}, strategy.Sizing{GrossLeverage: 1})
	sig, _ = m.Signals(hist(map[string][]float64{"A": {10, 11, 12}, "H": {5, 100, 1}}))
	if got := states(t, sig, "A"); !equal(got, []int{0, 1, 1}) {
		t.Errorf("states = %v", got)
	}
}

func TestGrossLeverageAndTargets(t *testing.T) {
	m, _ := Parse([]string{"A", "B", "H"}, strategy.Params{"lookback": 1, "rebalance_days": 1}, strategy.Sizing{GrossLeverage: 0.8})
	h := hist(map[string][]float64{"A": {10, 11}, "B": {5, 4}, "H": {1, 1}})
	w, err := m.Targets(h, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(w["A"]-0.4) > 1e-12 || w["B"] != 0 || math.Abs(w["H"]-0.4) > 1e-12 {
		t.Errorf("targets = %v", w)
	}
	if _, err := m.Targets(map[string][]bars.Bar{"A": h["A"]}, nil); err == nil {
		t.Error("want an error for a missing symbol")
	}
}

func TestParity(t *testing.T) {
	paritytest.Run(t, "../../../../golden/time_series_momentum", Name,
		func(u []string, p strategy.Params, s strategy.Sizing) (paritytest.Weighter, error) {
			return Parse(u, p, s)
		})
}
