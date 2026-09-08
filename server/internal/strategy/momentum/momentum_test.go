package momentum

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

func goodParams() strategy.Params {
	return strategy.Params{"lookback": 252, "top_k": 1, "rebalance_days": 21}
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
			edit: func(p strategy.Params) { p["band"] = 0 }, wantErr: "unknown params band"},
		{name: "missing param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { delete(p, "top_k") }, wantErr: "missing params top_k"},
		{name: "zero lookback", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["lookback"] = 0 }, wantErr: "lookback must be >= 1"},
		{name: "fractional rebalance", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["rebalance_days"] = 2.5 }, wantErr: "whole number"},
		{name: "top_k beyond the universe", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["top_k"] = 2 }, wantErr: "top_k 2 exceeds"},
		{name: "zero gross", universe: []string{"SPY", "GLD"}, gross: 0, wantErr: "gross_leverage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goodParams()
			if tc.edit != nil {
				tc.edit(p)
			}
			_, err := Parse(tc.universe, p, strategy.Sizing{GrossLeverage: tc.gross})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestPick(t *testing.T) {
	r := map[string]float64{"A": 0.10, "B": 0.05, "C": 0.20, "H": 0.06}
	if got := pick([]string{"A", "B", "C"}, r, "H", 2); strings.Join(got, ",") != "C,A" {
		t.Errorf("pick = %v", got)
	}
	if got := pick([]string{"A", "B", "C"}, r, "H", 5); strings.Join(got, ",") != "C,A" {
		t.Errorf("pick without a cap = %v", got)
	}
	r["H"] = 0.5
	if got := pick([]string{"A", "B", "C"}, r, "H", 2); len(got) != 0 {
		t.Errorf("pick against a winning haven = %v", got)
	}
	tie := map[string]float64{"A": 0.1, "B": 0.1, "H": 0}
	if got := pick([]string{"A", "B"}, tie, "H", 1); strings.Join(got, ",") != "A" {
		t.Errorf("tie = %v, want universe order", got)
	}
}

func series(start time.Time, prices ...float64) []bars.Bar {
	out := make([]bars.Bar, len(prices))
	for i, p := range prices {
		out[i] = bars.Bar{Date: start.AddDate(0, 0, i), Open: p, High: p, Low: p, Close: p, AdjClose: p, Volume: 1, Source: "test"}
	}
	return out
}

// TestHoldsBetweenRebalances is the Python test's scenario, bar for bar.
func TestHoldsBetweenRebalances(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	hist := map[string][]bars.Bar{
		"A": series(start, 1, 2, 2, 2),
		"B": series(start, 1, 1, 2, 3),
		"H": series(start, 1, 1, 1, 1),
	}
	m, err := Parse([]string{"A", "B", "H"}, strategy.Params{"lookback": 1, "top_k": 1, "rebalance_days": 2}, strategy.Sizing{GrossLeverage: 1})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := m.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	wantA := []bool{false, true, true, false}
	wantB := []bool{false, false, false, true}
	wantH := []float64{1, 0, 0, 0}
	for i, s := range sig {
		if s.Held["A"] != wantA[i] || s.Held["B"] != wantB[i] || s.Weights["H"] != wantH[i] {
			t.Errorf("bar %d: held A %v B %v, H weight %v; want %v %v %v", i, s.Held["A"], s.Held["B"], s.Weights["H"], wantA[i], wantB[i], wantH[i])
		}
	}
	if sig[0].Defined["A"] || !sig[1].Defined["A"] || math.Abs(sig[1].Return["A"]-1) > 1e-12 {
		t.Errorf("returns: %+v %+v", sig[0], sig[1])
	}
	w, err := m.Targets(hist, nil)
	if err != nil || w["B"] != 1 || w["A"] != 0 || w["H"] != 0 {
		t.Errorf("Targets = %v, %v", w, err)
	}
}

func TestTopKSplitsTheBookAndTheHavenTakesTheRest(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := Parse([]string{"A", "B", "H"}, strategy.Params{"lookback": 1, "top_k": 2, "rebalance_days": 1}, strategy.Sizing{GrossLeverage: 1})
	sig, err := m.Signals(map[string][]bars.Bar{
		"A": series(start, 1, 2, 2),
		"B": series(start, 1, 1.5, 1.4),
		"H": series(start, 1, 1, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig[1].Weights["A"] != 0.5 || sig[1].Weights["B"] != 0.5 || sig[1].Weights["H"] != 0 {
		t.Errorf("bar 1 weights = %v", sig[1].Weights)
	}
	// Bar 2: A flat (0 = haven's 0, not ahead), B down: nothing beats the haven.
	if sig[2].Weights["H"] != 1 || sig[2].Weights["A"] != 0 {
		t.Errorf("bar 2 weights = %v", sig[2].Weights)
	}
	all, _ := m.Weights(map[string][]bars.Bar{"A": series(start, 1, 2), "B": series(start, 1, 1), "H": series(start, 1, 1)})
	if len(all) != 2 {
		t.Errorf("Weights gave %d bars", len(all))
	}
}

func TestAlignmentAndErrors(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := Parse([]string{"A", "H"}, goodParams(), strategy.Sizing{GrossLeverage: 1})
	if _, err := m.Signals(map[string][]bars.Bar{"A": series(start, 1)}); err == nil || !strings.Contains(err.Error(), "no bars for H") {
		t.Errorf("err = %v", err)
	}
	if _, err := m.Targets(map[string][]bars.Bar{"A": series(start, 1), "H": series(start.AddDate(0, 0, 5), 1)}, nil); err == nil || !strings.Contains(err.Error(), "share no dates") {
		t.Errorf("err = %v", err)
	}
	if st, err := strategy.New(Name, []string{"SPY", "GLD"}, goodParams(), strategy.Sizing{GrossLeverage: 1}); err != nil {
		t.Fatal(err)
	} else if _, ok := st.(*Momentum); !ok {
		t.Errorf("registry built a %T", st)
	}
}
