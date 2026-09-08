package trend

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

func goodParams() strategy.Params { return strategy.Params{"lookback": 200, "band": 0.01} }

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
		{name: "empty symbol", universe: []string{"", "GLD"}, gross: 1, wantErr: "empty symbol"},
		{name: "unknown param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["entry_z"] = 2 }, wantErr: "unknown params entry_z"},
		{name: "missing param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { delete(p, "band") }, wantErr: "missing params band"},
		{name: "fractional lookback", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["lookback"] = 2.5 }, wantErr: "whole number"},
		{name: "lookback of one", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["lookback"] = 1 }, wantErr: "lookback must be >= 2"},
		{name: "band of one", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["band"] = 1 }, wantErr: "band"},
		{name: "negative band", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["band"] = -0.1 }, wantErr: "band"},
		{name: "zero gross", universe: []string{"SPY", "GLD"}, gross: 0, wantErr: "gross_leverage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goodParams()
			if tc.edit != nil {
				tc.edit(p)
			}
			tr, err := Parse(tc.universe, p, strategy.Sizing{GrossLeverage: tc.gross})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := strings.Join(tr.Universe(), ","); got != strings.Join(tc.universe, ",") {
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

func TestMovingAverage(t *testing.T) {
	sma, ok := movingAverage([]float64{10, 10, 10, 10}, 3)
	if ok[0] || ok[1] || !ok[2] || sma[2] != 10 || sma[3] != 10 {
		t.Errorf("sma = %v ok = %v", sma, ok)
	}
	sma, _ = movingAverage([]float64{1, 2, 3, 4}, 2)
	if sma[1] != 1.5 || sma[2] != 2.5 || sma[3] != 3.5 {
		t.Errorf("sma = %v", sma)
	}
	if _, ok := movingAverage([]float64{1}, 2); ok[0] {
		t.Error("defined on a short series")
	}
}

func TestStep(t *testing.T) {
	pr := Params{Lookback: 3, Band: 0.1}
	cases := []struct {
		name       string
		s          int
		price, sma float64
		defined    bool
		want       int
	}{
		{"undefined forces the haven", 1, 100, 90, false, 0},
		{"out, on the average", 0, 10, 10, true, 0},
		{"out, inside the band", 0, 10.5, 10, true, 0},
		{"out, above the band enters", 0, 11.5, 10, true, 1},
		{"in, inside the band holds", 1, 9.5, 10, true, 1},
		{"in, on the exit line holds", 1, 9, 10, true, 1},
		{"in, below the band exits", 1, 8.5, 10, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := step(tc.s, tc.price, tc.sma, tc.defined, pr); got != tc.want {
				t.Errorf("step = %d, want %d", got, tc.want)
			}
		})
	}
}

func series(start time.Time, prices ...float64) []bars.Bar {
	out := make([]bars.Bar, len(prices))
	for i, p := range prices {
		out[i] = bars.Bar{Date: start.AddDate(0, 0, i), Open: p, High: p, Low: p, Close: p, AdjClose: p, Volume: 1, Source: "test"}
	}
	return out
}

// TestReplayScenario is the Python test's scenario, bar for bar.
func TestReplayScenario(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	hist := map[string][]bars.Bar{
		"A": series(start, 10, 10, 10, 12, 12, 9, 9, 9, 13),
		"B": series(start, 5, 5, 5, 5, 5, 5, 5, 5, 5),
		"H": series(start, 1, 1, 1, 1, 1, 1, 1, 1, 1),
	}
	tr, err := Parse([]string{"A", "B", "H"}, strategy.Params{"lookback": 3, "band": 0}, strategy.Sizing{GrossLeverage: 0.8})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := tr.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{0, 0, 0, 1, 1, 0, 0, 0, 1}
	for i, s := range sig {
		if s.State["A"] != want[i] || s.State["B"] != 0 {
			t.Errorf("bar %d: states A %d B %d, want A %d B 0 (sma A %v)", i, s.State["A"], s.State["B"], want[i], s.SMA["A"])
		}
		total := s.Weights["A"] + s.Weights["B"] + s.Weights["H"]
		if math.Abs(total-0.8) > 1e-12 {
			t.Errorf("bar %d: weights sum %v", i, total)
		}
		if want[i] == 1 && math.Abs(s.Weights["A"]-0.4) > 1e-12 {
			t.Errorf("bar %d: A weight %v, want 0.4", i, s.Weights["A"])
		}
	}
	w, err := tr.Targets(hist, nil)
	if err != nil || math.Abs(w["A"]-0.4) > 1e-12 || math.Abs(w["H"]-0.4) > 1e-12 {
		t.Errorf("Targets = %v, %v", w, err)
	}
	all, _ := tr.Weights(hist)
	if len(all) != len(sig) {
		t.Errorf("Weights gave %d bars", len(all))
	}
}

func TestAlignmentAndErrors(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tr, _ := Parse([]string{"A", "H"}, goodParams(), strategy.Sizing{GrossLeverage: 1})
	sig, err := tr.Signals(map[string][]bars.Bar{"A": series(start, 1, 2, 3), "H": series(start.AddDate(0, 0, 1), 1, 1)})
	if err != nil || len(sig) != 2 {
		t.Errorf("aligned %d bars, err %v; want the two shared dates", len(sig), err)
	}
	if _, err := tr.Signals(map[string][]bars.Bar{"A": series(start, 1)}); err == nil || !strings.Contains(err.Error(), "no bars for H") {
		t.Errorf("err = %v", err)
	}
	if _, err := tr.Targets(map[string][]bars.Bar{"A": series(start, 1), "H": series(start.AddDate(0, 0, 5), 1)}, nil); err == nil || !strings.Contains(err.Error(), "share no dates") {
		t.Errorf("err = %v", err)
	}
	h := series(start, 1, 1, 1)
	h[1], h[2] = h[2], h[1]
	if _, err := tr.Signals(map[string][]bars.Bar{"A": series(start, 1, 1, 1), "H": h}); err == nil || !strings.Contains(err.Error(), "strictly increasing") {
		t.Errorf("err = %v", err)
	}
	if st, err := strategy.New(Name, []string{"SPY", "GLD"}, goodParams(), strategy.Sizing{GrossLeverage: 1}); err != nil {
		t.Fatal(err)
	} else if _, ok := st.(*Trend); !ok {
		t.Errorf("registry built a %T", st)
	}
}
