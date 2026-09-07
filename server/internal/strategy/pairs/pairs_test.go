package pairs

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

func goodParams() strategy.Params {
	return strategy.Params{"hedge_ratio": 1.5, "lookback": 20, "entry_z": 2, "exit_z": 0.5, "max_hold_days": 30}
}

func TestParseValidation(t *testing.T) {
	cases := []struct {
		name     string
		universe []string
		edit     func(strategy.Params)
		gross    float64
		wantErr  string // substring; "" means it must build
	}{
		{name: "good", universe: []string{"GLD", "GDX"}, gross: 0.9},
		{name: "one symbol", universe: []string{"GLD"}, gross: 0.9, wantErr: "exactly two"},
		{name: "three symbols", universe: []string{"GLD", "GDX", "SLV"}, gross: 0.9, wantErr: "exactly two"},
		{name: "same symbol twice", universe: []string{"GLD", "GLD"}, gross: 0.9, wantErr: "same symbol"},
		{name: "empty symbol", universe: []string{"GLD", ""}, gross: 0.9, wantErr: "empty symbol"},
		{name: "typo in param", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["lookbak"] = 20 }, wantErr: "unknown params lookbak"},
		{name: "missing param", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { delete(p, "exit_z") }, wantErr: "missing params exit_z"},
		{name: "lookback 1", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["lookback"] = 1 }, wantErr: "lookback must be >= 2"},
		{name: "lookback fractional", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["lookback"] = 20.5 }, wantErr: "lookback must be a whole number"},
		{name: "max_hold_days 0", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["max_hold_days"] = 0 }, wantErr: "max_hold_days must be >= 1"},
		{name: "max_hold_days fractional", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["max_hold_days"] = 2.5 }, wantErr: "max_hold_days must be a whole number"},
		{name: "exit equals entry", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["exit_z"] = 2 }, wantErr: "entry_z > exit_z"},
		{name: "exit above entry", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["exit_z"] = 3 }, wantErr: "entry_z > exit_z"},
		{name: "negative exit", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["exit_z"] = -0.1 }, wantErr: "entry_z > exit_z"},
		{name: "exit zero is allowed", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["exit_z"] = 0 }},
		{name: "hedge ratio NaN", universe: []string{"GLD", "GDX"}, gross: 0.9,
			edit: func(p strategy.Params) { p["hedge_ratio"] = math.NaN() }, wantErr: "hedge_ratio"},
		{name: "gross zero", universe: []string{"GLD", "GDX"}, gross: 0, wantErr: "gross_leverage"},
		{name: "gross negative", universe: []string{"GLD", "GDX"}, gross: -1, wantErr: "gross_leverage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := goodParams()
			if tc.edit != nil {
				tc.edit(params)
			}
			s, err := New(tc.universe, params, strategy.Sizing{GrossLeverage: tc.gross})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := s.Universe(); len(got) != 2 || got[0] != tc.universe[0] || got[1] != tc.universe[1] {
					t.Fatalf("Universe() = %v, want %v", got, tc.universe)
				}
				return
			}
			if err == nil {
				t.Fatalf("built a strategy, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRegistered(t *testing.T) {
	s, err := strategy.New(Name, []string{"GLD", "GDX"}, goodParams(), strategy.Sizing{GrossLeverage: 0.9})
	if err != nil {
		t.Fatalf("strategy.New(%q): %v", Name, err)
	}
	if _, ok := s.(*Pairs); !ok {
		t.Fatalf("strategy.New(%q) returned %T, want *Pairs", Name, s)
	}
}

// day0 is an arbitrary Monday; series in these tests run one bar per day
// from it.
var day0 = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// series builds bars for a symbol with the given adjcloses on consecutive
// days starting at day0 (offset skips days, to build misaligned series).
func series(prices []float64, offset int) []bars.Bar {
	out := make([]bars.Bar, len(prices))
	for i, p := range prices {
		out[i] = bars.Bar{Date: day0.AddDate(0, 0, offset+i), AdjClose: p, Close: p, Open: p, High: p, Low: p}
	}
	return out
}

// unitPair makes a pair with h=1 whose B leg is 1 forever, so the spread
// is A-1 and z depends on A alone; with lookback 2 the z-score is +-1/sqrt2
// on any bar where A moved and undefined where it did not, which makes a
// price path a script for the state machine.
func unitPair(t *testing.T, a []float64, params strategy.Params, gross float64) (*Pairs, map[string][]bars.Bar) {
	t.Helper()
	p, err := Parse([]string{"A", "B"}, params, strategy.Sizing{GrossLeverage: gross})
	if err != nil {
		t.Fatal(err)
	}
	ones := make([]float64, len(a))
	for i := range ones {
		ones[i] = 1
	}
	return p, map[string][]bars.Bar{"A": series(a, 0), "B": series(ones, 0)}
}

func TestZHandComputed(t *testing.T) {
	// spread = A - 1 = [1, 2, 3, 6, 6, 6] with lookback 3.
	p, hist := unitPair(t, []float64{2, 3, 4, 7, 7, 7},
		strategy.Params{"hedge_ratio": 1, "lookback": 3, "entry_z": 10, "exit_z": 0, "max_hold_days": 5}, 1)
	sig, err := p.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		z       float64
		defined bool
	}{
		{0, false}, // one bar
		{0, false}, // two bars: still short of the window
		{1, true},  // [1,2,3]: mean 2, sd 1
		{(7.0 / 3) / math.Sqrt(13.0/3), true}, // [2,3,6]: mean 11/3, var (25+4+49)/9/2
		{1 / math.Sqrt(3), true},              // [3,6,6]: mean 5, var 6/2
		{0, false},                            // [6,6,6]: sd 0
	}
	if len(sig) != len(want) {
		t.Fatalf("got %d signals, want %d", len(sig), len(want))
	}
	for i, w := range want {
		if sig[i].ZDefined != w.defined {
			t.Errorf("bar %d: ZDefined = %v, want %v", i, sig[i].ZDefined, w.defined)
		}
		if w.defined && math.Abs(sig[i].Z-w.z) > 1e-12 {
			t.Errorf("bar %d: Z = %v, want %v", i, sig[i].Z, w.z)
		}
	}
	z, ok, err := p.Z(hist)
	if err != nil || ok || z != 0 {
		t.Fatalf("Z() = %v, %v, %v; want undefined on the last bar", z, ok, err)
	}
}

func TestStep(t *testing.T) {
	pr := Params{EntryZ: 2, ExitZ: 0.5, MaxHoldDays: 3}
	cases := []struct {
		name          string
		s, held       int
		z             float64
		defined       bool
		wantS, wantHd int
	}{
		{"flat stays flat inside the band", 0, 0, 1.9, true, 0, 0},
		{"flat enters long at -entry", 0, 0, -2, true, 1, 0},
		{"flat enters long below -entry", 0, 0, -3, true, 1, 0},
		{"flat enters short at +entry", 0, 0, 2, true, -1, 0},
		{"long holds and counts", 1, 0, -1, true, 1, 1},
		{"long exits at -exit", 1, 1, -0.5, true, 0, 0},
		{"long exits above -exit even past +entry, no short", 1, 1, 3, true, 0, 0},
		{"long time stop", 1, 3, -3, true, 0, 0},
		{"long just under time stop", 1, 2, -3, true, 1, 3},
		{"short holds and counts", -1, 0, 1, true, -1, 1},
		{"short exits at +exit", -1, 1, 0.5, true, 0, 0},
		{"short exits below exit even past -entry, no long", -1, 1, -3, true, 0, 0},
		{"short time stop", -1, 3, 3, true, 0, 0},
		{"undefined forces flat from long", 1, 1, 0, false, 0, 0},
		{"undefined forces flat from short", -1, 1, 0, false, 0, 0},
		{"undefined keeps flat", 0, 0, 0, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, held := step(tc.s, tc.held, tc.z, tc.defined, pr)
			if s != tc.wantS || held != tc.wantHd {
				t.Fatalf("step(%d, %d, %v, %v) = (%d, %d), want (%d, %d)", tc.s, tc.held, tc.z, tc.defined, s, held, tc.wantS, tc.wantHd)
			}
		})
	}
}

// TestStateMachineReplay drives the whole strategy with a price path: with
// lookback 2 a down move is z = -0.707, an up move +0.707, a flat move
// undefined, and entry 0.5 / exit 0.1 turn those into enter / exit / flat.
func TestStateMachineReplay(t *testing.T) {
	params := strategy.Params{"hedge_ratio": 1, "lookback": 2, "entry_z": 0.5, "exit_z": 0.1, "max_hold_days": 3}
	p, hist := unitPair(t, []float64{10, 9, 8, 7, 8, 8, 9, 10, 10}, params, 1)
	sig, err := p.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{
		0,  // t0: one bar, undefined
		+1, // t1: down, z = -0.707 <= -0.5: enter long
		+1, // t2: down: hold (held 1)
		+1, // t3: down: hold (held 2)
		0,  // t4: up, z = +0.707 >= -0.1: exit; +0.707 >= 0.5 must NOT open a short on the exit bar
		0,  // t5: flat move, undefined: flat
		-1, // t6: up, z >= 0.5: enter short
		-1, // t7: up: hold
		0,  // t8: flat move, undefined forces flat
	}
	assertStates(t, sig, want)

	// The time stop: max_hold_days 2 on a run of down moves exits on the
	// third bar after entry and re-enters on the next.
	params["max_hold_days"] = 2
	p, hist = unitPair(t, []float64{10, 9, 8, 7, 6, 5, 4}, params, 1)
	sig, err = p.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	assertStates(t, sig, []int{
		0,  // t0
		+1, // t1: enter, held 0
		+1, // t2: held 1
		+1, // t3: held 2
		0,  // t4: held >= 2: time stop
		+1, // t5: z <= -entry again: re-enter
		+1, // t6
	})
}

func assertStates(t *testing.T, sig []Signal, want []int) {
	t.Helper()
	if len(sig) != len(want) {
		t.Fatalf("got %d signals, want %d", len(sig), len(want))
	}
	for i, w := range want {
		if sig[i].State != w {
			t.Errorf("bar %d (%s): state %+d, want %+d (z=%v defined=%v)", i, sig[i].Date.Format("2006-01-02"), sig[i].State, w, sig[i].Z, sig[i].ZDefined)
		}
	}
}

func TestWeights(t *testing.T) {
	const G = 0.9
	const h = 1.631
	// Prices that move enough to trigger with lookback 2: every bar after
	// the first is in a position (entry 0.5 < 0.707), alternating exits.
	params := strategy.Params{"hedge_ratio": h, "lookback": 2, "entry_z": 0.5, "exit_z": 0.1, "max_hold_days": 10}
	p, err := Parse([]string{"GLD", "GDX"}, params, strategy.Sizing{GrossLeverage: G})
	if err != nil {
		t.Fatal(err)
	}
	gld := []float64{180, 175, 170, 172, 178, 178}
	gdx := []float64{30, 30, 30, 30, 30, 30}
	hist := map[string][]bars.Bar{"GLD": series(gld, 0), "GDX": series(gdx, 0)}
	sig, err := p.Signals(hist)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range sig {
		wA, wB := s.Weights["GLD"], s.Weights["GDX"]
		if len(s.Weights) != 2 {
			t.Fatalf("bar %d: weights %v, want exactly the two symbols", i, s.Weights)
		}
		if s.State == 0 {
			if wA != 0 || wB != 0 {
				t.Errorf("bar %d: flat but weights %v", i, s.Weights)
			}
			continue
		}
		if got := math.Abs(wA) + math.Abs(wB); math.Abs(got-G) > 1e-12 {
			t.Errorf("bar %d: |wA|+|wB| = %v, want %v", i, got, G)
		}
		// Long the spread is long A, short B; short is the mirror.
		if float64(s.State)*wA <= 0 || float64(s.State)*wB >= 0 {
			t.Errorf("bar %d: state %+d but weights A=%v B=%v", i, s.State, wA, wB)
		}
		// Dollar-neutral by notional: w_A / w_B = -price_A / (h * price_B).
		nA, nB := gld[i], h*gdx[i]
		if math.Abs(wA*nB+wB*nA) > 1e-12 {
			t.Errorf("bar %d: legs not notional-balanced: A=%v B=%v", i, wA, wB)
		}
	}
	// Exact check of one bar: t1 is a long entry.
	if sig[1].State != +1 {
		t.Fatalf("bar 1: state %+d, want +1", sig[1].State)
	}
	gross := 175 + h*30
	if wantA := G * 175 / gross; math.Abs(sig[1].Weights["GLD"]-wantA) > 1e-12 {
		t.Errorf("bar 1: w_GLD = %v, want %v", sig[1].Weights["GLD"], wantA)
	}
	if wantB := -G * h * 30 / gross; math.Abs(sig[1].Weights["GDX"]-wantB) > 1e-12 {
		t.Errorf("bar 1: w_GDX = %v, want %v", sig[1].Weights["GDX"], wantB)
	}
	// Both flat bars and positioned bars appear, so the test covered both.
	states := map[int]bool{}
	for _, s := range sig {
		states[s.State] = true
	}
	if !states[0] || !states[1] || !states[-1] {
		t.Fatalf("path did not visit every state: %v", states)
	}

	// Targets is the last signal's weights, and the broker's positions do
	// not change the answer.
	got, err := p.Targets(hist, []strategy.Position{{Symbol: "GLD", Qty: -1000}, {Symbol: "GDX", Qty: 5}})
	if err != nil {
		t.Fatal(err)
	}
	last := sig[len(sig)-1].Weights
	if got["GLD"] != last["GLD"] || got["GDX"] != last["GDX"] {
		t.Fatalf("Targets = %v, want last signal %v", got, last)
	}
}

func TestAlignment(t *testing.T) {
	params := strategy.Params{"hedge_ratio": 1, "lookback": 2, "entry_z": 0.5, "exit_z": 0.1, "max_hold_days": 3}
	p, err := Parse([]string{"A", "B"}, params, strategy.Sizing{GrossLeverage: 1})
	if err != nil {
		t.Fatal(err)
	}
	// A has days 0..5; B has days 2..7 with day 4 missing. Shared: 2, 3, 5.
	a := series([]float64{10, 9, 8, 7, 6, 5}, 0)
	b := series([]float64{1, 1, 1, 1, 1, 1}, 2)
	b = append(b[:2], b[3:]...)
	// B stamped at a different clock time still joins on the calendar day.
	b[0].Date = b[0].Date.Add(13 * time.Hour)
	sig, err := p.Signals(map[string][]bars.Bar{"A": a, "B": b})
	if err != nil {
		t.Fatal(err)
	}
	var days []int
	for _, s := range sig {
		days = append(days, int(s.Date.Sub(day0).Hours()/24))
	}
	if want := []int{2, 3, 5}; len(days) != 3 || days[0] != 2 || days[1] != 3 || days[2] != 5 {
		t.Fatalf("aligned days %v, want %v", days, want)
	}
	// The window runs over aligned bars, not calendar days: day 5's window
	// is [day 3, day 5] = spread [6, 4], a down move, so z is defined.
	if !sig[2].ZDefined || sig[2].Z >= 0 {
		t.Fatalf("day 5: z = %v defined %v, want a defined negative z", sig[2].Z, sig[2].ZDefined)
	}

	t.Run("no shared dates", func(t *testing.T) {
		hist := map[string][]bars.Bar{"A": series([]float64{1, 2}, 0), "B": series([]float64{1, 2}, 10)}
		sig, err := p.Signals(hist)
		if err != nil || len(sig) != 0 {
			t.Fatalf("Signals = %v, %v; want empty and no error", sig, err)
		}
		if _, err := p.Targets(hist, nil); err == nil {
			t.Fatal("Targets on disjoint series built targets, want error")
		}
		if _, _, err := p.Z(hist); err == nil {
			t.Fatal("Z on disjoint series succeeded, want error")
		}
	})

	t.Run("missing symbol", func(t *testing.T) {
		for _, hist := range []map[string][]bars.Bar{
			{"A": series([]float64{1, 2}, 0)},
			{"B": series([]float64{1, 2}, 0)},
			{"A": series([]float64{1, 2}, 0), "C": series([]float64{1, 2}, 0)},
			{},
			nil,
		} {
			if _, err := p.Signals(hist); err == nil {
				t.Errorf("Signals(%v) succeeded, want error for missing symbol", hist)
			}
			if _, err := p.Targets(hist, nil); err == nil {
				t.Errorf("Targets(%v) succeeded, want error for missing symbol", hist)
			}
		}
	})

	t.Run("out of order", func(t *testing.T) {
		a := series([]float64{1, 2, 3}, 0)
		a[1], a[2] = a[2], a[1]
		if _, err := p.Signals(map[string][]bars.Bar{"A": a, "B": series([]float64{1, 1, 1}, 0)}); err == nil {
			t.Fatal("Signals on an unsorted series succeeded, want error")
		}
	})
}
