package donchian

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/paritytest"
)

func goodParams() strategy.Params { return strategy.Params{"entry_lookback": 55, "exit_lookback": 20} }

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
			edit: func(p strategy.Params) { delete(p, "exit_lookback") }, wantErr: "missing params exit_lookback"},
		{name: "entry of one", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["entry_lookback"] = 1 }, wantErr: "entry_lookback must be >= 2"},
		{name: "fractional exit", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["exit_lookback"] = 2.5 }, wantErr: "whole number"},
		{name: "exit not shorter", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["exit_lookback"] = 55 }, wantErr: "exit_lookback < entry_lookback"},
		{name: "zero gross", universe: []string{"SPY", "GLD"}, gross: 0, wantErr: "gross_leverage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goodParams()
			if tc.edit != nil {
				tc.edit(p)
			}
			d, err := Parse(tc.universe, p, strategy.Sizing{GrossLeverage: tc.gross})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := strings.Join(d.Universe(), ","); got != strings.Join(tc.universe, ",") {
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

// ohlc builds bars whose adjusted prices are the arguments; factor is
// close/adjclose per bar (1 where nil).
func ohlc(close, high, low []float64, factor []float64) []bars.Bar {
	out := make([]bars.Bar, len(close))
	for i := range close {
		f := 1.0
		if factor != nil {
			f = factor[i]
		}
		h, l := close[i], close[i]
		if high != nil {
			h, l = high[i], low[i]
		}
		out[i] = bars.Bar{Date: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC),
			Open: close[i] * f, High: h * f, Low: l * f, Close: close[i] * f, AdjClose: close[i]}
	}
	return out
}

func flat(n int) []bars.Bar {
	c := make([]float64, n)
	for i := range c {
		c[i] = 1
	}
	return ohlc(c, nil, nil, nil)
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

// The same cases as research/tests/test_donchian.py.
func TestBreakoutEntersAboveAndExitsBelowThePriorChannels(t *testing.T) {
	d, _ := Parse([]string{"A", "H"}, strategy.Params{"entry_lookback": 3, "exit_lookback": 2}, strategy.Sizing{GrossLeverage: 1})
	hist := map[string][]bars.Bar{
		"A": ohlc([]float64{10, 11, 10, 12, 11.5, 11, 9, 9.5}, []float64{10.5, 11.5, 10.5, 12.5, 12, 11.5, 9.5, 10}, []float64{9.5, 10.5, 9.5, 11.5, 11, 10.5, 8.5, 9}, nil),
		"H": flat(8),
	}
	sig, err := d.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	if sig[2].UpperDefined["A"] || sig[3].Upper["A"] != 11.5 || sig[5].Lower["A"] != 11 {
		t.Errorf("channels: %+v %+v %+v", sig[2], sig[3], sig[5])
	}
	if got := states(sig, "A"); !equal(got, []int{0, 0, 0, 1, 1, 1, 0, 0}) {
		t.Errorf("states = %v", got)
	}
}

func TestTheCurrentBarIsNotInItsOwnChannel(t *testing.T) {
	d, err := Parse([]string{"A", "H"}, strategy.Params{"entry_lookback": 3, "exit_lookback": 2}, strategy.Sizing{GrossLeverage: 1})
	if err != nil {
		t.Fatal(err)
	}
	hist := map[string][]bars.Bar{"A": ohlc([]float64{10, 10, 10, 11}, []float64{10.5, 10.5, 10.5, 11.5}, []float64{9.5, 9.5, 9.5, 10.5}, nil), "H": flat(4)}
	sig, _ := d.Signals(hist)
	if got := states(sig, "A"); !equal(got, []int{0, 0, 0, 1}) {
		t.Errorf("states = %v", got)
	}
}

func TestASplitCannotFakeABreakout(t *testing.T) {
	d, _ := Parse([]string{"A", "H"}, strategy.Params{"entry_lookback": 3, "exit_lookback": 2}, strategy.Sizing{GrossLeverage: 1})
	c := []float64{10, 10, 10, 10, 10, 10}
	h := []float64{10.2, 10.2, 10.2, 10.2, 10.2, 10.2}
	l := []float64{9.8, 9.8, 9.8, 9.8, 9.8, 9.8}
	hist := map[string][]bars.Bar{"A": ohlc(c, h, l, []float64{1, 1, 1, 2, 2, 2}), "H": flat(6)}
	sig, _ := d.Signals(hist)
	for i, s := range sig {
		if s.State["A"] != 0 {
			t.Errorf("bar %d: entered on a split", i)
		}
	}
	if math.Abs(sig[4].Upper["A"]-10.2) > 1e-12 {
		t.Errorf("upper = %v, want 10.2 (adjusted)", sig[4].Upper["A"])
	}
}

func TestEqualityNeverTransitionsAndWeightsSumToGross(t *testing.T) {
	d, err := Parse([]string{"A", "B", "H"}, strategy.Params{"entry_lookback": 3, "exit_lookback": 2}, strategy.Sizing{GrossLeverage: 0.8})
	if err != nil {
		t.Fatal(err)
	}
	hist := map[string][]bars.Bar{
		"A": ohlc([]float64{10, 10, 10, 10.5, 10.5}, []float64{10.5, 10.5, 10.5, 10.5, 10.5}, []float64{9.5, 9.5, 9.5, 9.5, 9.5}, nil),
		"B": ohlc([]float64{5, 5, 5, 6, 6}, []float64{5.5, 5.5, 5.5, 6.5, 6.5}, []float64{4.5, 4.5, 4.5, 4.5, 4.5}, nil),
		"H": flat(5),
	}
	sig, _ := d.Signals(hist)
	if got := states(sig, "A"); !equal(got, []int{0, 0, 0, 0, 0}) {
		t.Errorf("A states = %v", got)
	}
	if got := states(sig, "B"); !equal(got, []int{0, 0, 0, 1, 1}) {
		t.Errorf("B states = %v", got)
	}
	for i, s := range sig {
		if sum := s.Weights["A"] + s.Weights["B"] + s.Weights["H"]; math.Abs(sum-0.8) > 1e-12 {
			t.Errorf("bar %d: weights sum %v", i, sum)
		}
	}
	w, err := d.Targets(hist, nil)
	if err != nil || math.Abs(w["B"]-0.4) > 1e-12 || math.Abs(w["H"]-0.4) > 1e-12 {
		t.Errorf("targets = %v, %v", w, err)
	}
}

func TestParity(t *testing.T) {
	paritytest.Run(t, "../../../../golden/donchian_breakout", Name,
		func(u []string, p strategy.Params, s strategy.Sizing) (paritytest.Weighter, error) {
			return Parse(u, p, s)
		})
}
