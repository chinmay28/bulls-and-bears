package ratio

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

func goodParams() strategy.Params {
	return strategy.Params{"lookback": 20, "entry_z": 2, "exit_z": 0.5, "max_hold_days": 8}
}

func TestParseValidation(t *testing.T) {
	cases := []struct {
		name     string
		universe []string
		edit     func(strategy.Params)
		gross    float64
		wantErr  string // substring; "" means it must build
	}{
		{name: "good", universe: []string{"SPY", "QQQ", "GLD"}, gross: 1},
		{name: "one risk asset is enough", universe: []string{"SPY", "GLD"}, gross: 1},
		{name: "haven alone", universe: []string{"GLD"}, gross: 1, wantErr: "at least one risk asset"},
		{name: "repeated symbol", universe: []string{"SPY", "SPY", "GLD"}, gross: 1, wantErr: "twice"},
		{name: "empty symbol", universe: []string{"SPY", "", "GLD"}, gross: 1, wantErr: "empty symbol"},
		{name: "unknown param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["hedge_ratio"] = 1.5 }, wantErr: "unknown params hedge_ratio"},
		{name: "missing param", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { delete(p, "exit_z") }, wantErr: "missing params exit_z"},
		{name: "fractional lookback", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["lookback"] = 20.5 }, wantErr: "lookback must be a whole number"},
		{name: "lookback of one", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["lookback"] = 1 }, wantErr: "lookback must be >= 2"},
		{name: "zero entry", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["entry_z"] = 0 }, wantErr: "entry_z must be a finite number > 0"},
		{name: "exit below minus entry", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["exit_z"] = -2 }, wantErr: "exit_z > -entry_z"},
		{name: "exit just above minus entry is fine", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["exit_z"] = -1.5 }},
		{name: "zero max hold", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["max_hold_days"] = 0 }, wantErr: "max_hold_days must be >= 1"},
		{name: "nan entry", universe: []string{"SPY", "GLD"}, gross: 1,
			edit: func(p strategy.Params) { p["entry_z"] = math.NaN() }, wantErr: "entry_z"},
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
				if got := r.Universe(); strings.Join(got, ",") != strings.Join(tc.universe, ",") {
					t.Errorf("Universe() = %v, want %v", got, tc.universe)
				}
				if r.Haven() != tc.universe[len(tc.universe)-1] {
					t.Errorf("Haven() = %q", r.Haven())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestStep(t *testing.T) {
	pr := Params{Lookback: 3, EntryZ: 1, ExitZ: 0, MaxHoldDays: 2}
	cases := []struct {
		name          string
		s, held       int
		z             float64
		defined       bool
		wantS, wantHd int
	}{
		{"undefined forces the haven", 1, 1, -5, false, 0, 0},
		{"flat, z inside the band", 0, 0, -0.5, true, 0, 0},
		{"flat, z at -entry enters", 0, 0, -1, true, 1, 0},
		{"flat, z above the mean stays out", 0, 0, 2, true, 0, 0},
		{"in, z still low holds", 1, 0, -0.5, true, 1, 1},
		{"in, z at exit leaves", 1, 0, 0, true, 0, 0},
		{"in, time stop", 1, 2, -3, true, 0, 0},
		{"in, held below the stop", 1, 1, -3, true, 1, 2},
		{"exit and no re-entry on one bar", 1, 2, -9, true, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, hd := step(tc.s, tc.held, tc.z, tc.defined, pr)
			if s != tc.wantS || hd != tc.wantHd {
				t.Errorf("step = (%d, %d), want (%d, %d)", s, hd, tc.wantS, tc.wantHd)
			}
		})
	}
}

func TestZScore(t *testing.T) {
	x := []float64{0, 1, 2, 3}
	if _, ok := zscore(x, 1, 3); ok {
		t.Error("z defined before the window fills")
	}
	// window (0,1,2): mean 1, sample sd 1 -> z = 1
	if z, ok := zscore(x, 2, 3); !ok || math.Abs(z-1) > 1e-12 {
		t.Errorf("zscore = %v, %v; want 1, true", z, ok)
	}
	if _, ok := zscore([]float64{2, 2, 2}, 2, 3); ok {
		t.Error("z defined on a flat window")
	}
}

func series(start time.Time, prices ...float64) []bars.Bar {
	out := make([]bars.Bar, len(prices))
	for i, p := range prices {
		out[i] = bars.Bar{Date: start.AddDate(0, 0, i), Open: p, High: p, Low: p, Close: p, AdjClose: p, Volume: 1, Source: "test"}
	}
	return out
}

func mustParse(t *testing.T, universe []string, params strategy.Params, gross float64) *Ratio {
	t.Helper()
	r, err := Parse(universe, params, strategy.Sizing{GrossLeverage: gross})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestReplayScenario is the Python test's scenario: A's log ratio to H sits
// at 0 for three bars (flat window: undefined), drops to -9 (enter), drifts
// lower (held twice), time-stops on the seventh bar; B's ratio never moves,
// so its sleeve stays in the haven and the haven carries the rest of G.
func TestReplayScenario(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	a := []float64{1, 1, 1, math.Exp(-9), math.Exp(-9.1), math.Exp(-9.2), math.Exp(-9.3), math.Exp(-9.3)}
	hist := map[string][]bars.Bar{
		"A": series(start, a...),
		"B": series(start, 5, 5, 5, 5, 5, 5, 5, 5),
		"H": series(start, 1, 1, 1, 1, 1, 1, 1, 1),
	}
	r := mustParse(t, []string{"A", "B", "H"}, strategy.Params{"lookback": 3, "entry_z": 1, "exit_z": 0, "max_hold_days": 2}, 0.9)
	sig, err := r.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	wantState := []int{0, 0, 0, 1, 1, 1, 0, 0}
	for t_, s := range sig {
		if s.State["A"] != wantState[t_] {
			t.Errorf("bar %d: state A = %d, want %d (z %v defined %v)", t_, s.State["A"], wantState[t_], s.Z["A"], s.ZDefined["A"])
		}
		if s.State["B"] != 0 || s.ZDefined["B"] {
			t.Errorf("bar %d: B should be undefined and flat, got state %d defined %v", t_, s.State["B"], s.ZDefined["B"])
		}
		total := s.Weights["A"] + s.Weights["B"] + s.Weights["H"]
		if math.Abs(total-0.9) > 1e-12 {
			t.Errorf("bar %d: weights sum to %v, want 0.9", t_, total)
		}
		if s.State["A"] == 1 {
			if math.Abs(s.Weights["A"]-0.45) > 1e-12 || math.Abs(s.Weights["H"]-0.45) > 1e-12 {
				t.Errorf("bar %d: weights %v, want A 0.45 H 0.45", t_, s.Weights)
			}
		} else if s.Weights["A"] != 0 || math.Abs(s.Weights["H"]-0.9) > 1e-12 {
			t.Errorf("bar %d: weights %v, want A 0 H 0.9", t_, s.Weights)
		}
	}
	if len(sig) > 3 && !(sig[3].ZDefined["A"] && sig[3].Z["A"] < -1) {
		t.Errorf("bar 3: z A = %v defined %v, want a defined value below -1", sig[3].Z["A"], sig[3].ZDefined["A"])
	}

	// Targets is the last bar; Weights is every bar.
	w, err := r.Targets(hist, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w["H"] != 0.9 || w["A"] != 0 || w["B"] != 0 {
		t.Errorf("Targets = %v", w)
	}
	all, err := r.Weights(hist)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(sig) {
		t.Errorf("Weights gave %d bars, Signals %d", len(all), len(sig))
	}
}

func TestAlignment(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	r := mustParse(t, []string{"A", "H"}, goodParams(), 1)

	t.Run("inner join drops dates a symbol lacks", func(t *testing.T) {
		hist := map[string][]bars.Bar{
			"A": series(start, 1, 2, 3, 4),
			"H": series(start.AddDate(0, 0, 1), 1, 1, 1), // lacks the first date
		}
		sig, err := r.Signals(hist)
		if err != nil {
			t.Fatal(err)
		}
		if len(sig) != 3 || !sig[0].Date.Equal(start.AddDate(0, 0, 1)) {
			t.Errorf("aligned %d bars from %v", len(sig), sig[0].Date)
		}
	})
	t.Run("missing symbol is an error", func(t *testing.T) {
		if _, err := r.Signals(map[string][]bars.Bar{"A": series(start, 1, 2)}); err == nil || !strings.Contains(err.Error(), "no bars for H") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("no shared dates is an error from Targets", func(t *testing.T) {
		hist := map[string][]bars.Bar{"A": series(start, 1, 2), "H": series(start.AddDate(0, 0, 10), 1, 1)}
		if _, err := r.Targets(hist, nil); err == nil || !strings.Contains(err.Error(), "share no dates") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("out of order haven is an error", func(t *testing.T) {
		h := series(start, 1, 1, 1)
		h[1], h[2] = h[2], h[1]
		hist := map[string][]bars.Bar{"A": series(start, 1, 2, 3), "H": h}
		if _, err := r.Signals(hist); err == nil || !strings.Contains(err.Error(), "strictly increasing") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("join is by calendar date not instant", func(t *testing.T) {
		h := series(start, 1, 1, 1)
		for i := range h {
			h[i].Date = h[i].Date.Add(13 * time.Hour) // same day, different instant
		}
		sig, err := r.Signals(map[string][]bars.Bar{"A": series(start, 1, 2, 3), "H": h})
		if err != nil {
			t.Fatal(err)
		}
		if len(sig) != 3 {
			t.Errorf("aligned %d bars, want 3", len(sig))
		}
	})
}

func TestRegistered(t *testing.T) {
	st, err := strategy.New(Name, []string{"SPY", "GLD"}, goodParams(), strategy.Sizing{GrossLeverage: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.(*Ratio); !ok {
		t.Fatalf("registry built a %T", st)
	}
}
