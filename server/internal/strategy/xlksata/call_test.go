package xlksata

import (
	"math"
	"strings"
	"testing"
	"time"
)

// call builds a candidate against the 187.00 fixture lot. The default is a
// contract that passes every test, so each case below can break exactly one
// rule and see it caught.
func call(id string, edit ...func(*CallQuote)) CallQuote {
	c := CallQuote{
		OptionID:   id,
		Expiration: date(2026, 9, 14), // 5 DTE from the fixture clock
		Strike:     190,
		Delta:      0.25,
		Quote:      q(0.80, 0.90),
		Tradable:   true,
	}
	for _, e := range edit {
		e(&c)
	}
	return c
}

func lot(cs ...CallQuote) (Snapshot, State) {
	s := base()
	s.Phase = PhaseManage
	s.XLK = q(188.00, 188.10)
	s.XLKShares = 100
	s.Calls = cs
	st := State{Mode: Recovery, EntryPrice: entry, EntryDate: now.Add(-24 * time.Hour)}
	s.State = st
	return s, st
}

func TestSelectCallRules(t *testing.T) {
	e := engine(t)

	// Each case supplies exactly one candidate, broken in exactly one way.
	rejects := map[string]func(*CallQuote){
		"untradable":         func(c *CallQuote) { c.Tradable = false },
		"no bid":             func(c *CallQuote) { c.Quote.Bid = 0 },
		"no ask":             func(c *CallQuote) { c.Quote.Ask = 0 },
		"expires too soon":   func(c *CallQuote) { c.Expiration = date(2026, 9, 10) }, // 1 DTE
		"expires too late":   func(c *CallQuote) { c.Expiration = date(2026, 9, 17) }, // 8 DTE
		"delta too low":      func(c *CallQuote) { c.Delta = 0.19 },
		"delta too high":     func(c *CallQuote) { c.Delta = 0.31 },
		"strike below entry": func(c *CallQuote) { c.Strike = 186.50 },
	}
	for name, breakIt := range rejects {
		t.Run(name, func(t *testing.T) {
			s, st := lot(call("c1", breakIt))
			if pick, why := e.SelectCall(s, st); pick != nil {
				t.Fatalf("want no pick, got %+v (%s)", pick, why)
			}
		})
	}

	// The window edges themselves are inside it.
	accepts := map[string]func(*CallQuote){
		"minimum dte":     func(c *CallQuote) { c.Expiration = date(2026, 9, 11) }, // 2 DTE
		"maximum dte":     func(c *CallQuote) { c.Expiration = date(2026, 9, 16) }, // 7 DTE
		"minimum delta":   func(c *CallQuote) { c.Delta = 0.20 },
		"maximum delta":   func(c *CallQuote) { c.Delta = 0.30 },
		"strike at entry": func(c *CallQuote) { c.Strike = entry; c.Quote = q(2.00, 2.10) },
		"deep out strike": func(c *CallQuote) { c.Strike = 195 },
	}
	for name, edit := range accepts {
		t.Run(name, func(t *testing.T) {
			s, st := lot(call("c1", edit))
			if pick, why := e.SelectCall(s, st); pick == nil {
				t.Fatalf("want a pick: %s", why)
			}
		})
	}

	t.Run("no candidates at all", func(t *testing.T) {
		s, st := lot()
		if pick, why := e.SelectCall(s, st); pick != nil || !strings.Contains(why, "no candidate") {
			t.Fatalf("got %+v (%s)", pick, why)
		}
	})

	t.Run("the reason counts every rejection", func(t *testing.T) {
		s, st := lot(
			call("a", func(c *CallQuote) { c.Tradable = false }),
			call("b", func(c *CallQuote) { c.Delta = 0.05 }),
			call("c", func(c *CallQuote) { c.Strike = 180 }),
		)
		pick, why := e.SelectCall(s, st)
		if pick != nil {
			t.Fatalf("want no pick, got %+v", pick)
		}
		for _, want := range []string{"1 untradable", "1 outside 0.20-0.30 delta", "1 struck below"} {
			if !strings.Contains(why, want) {
				t.Fatalf("reason %q is missing %q", why, want)
			}
		}
	})
}

// The assignment test is the rule that stops the recovery capping itself
// below the number it exists to reach.
func TestSelectCallAssignmentTest(t *testing.T) {
	e := engine(t)

	// At a 187.00 strike there is no share gain, so the premium alone has
	// to clear 187.00 of profit: 1.87 per share.
	t.Run("premium alone cannot reach the target", func(t *testing.T) {
		s, st := lot(call("c1", func(c *CallQuote) {
			c.Strike = entry
			c.Quote = q(1.00, 1.10) // 1.05 mid, 105 dollars against 187
		}))
		pick, why := e.SelectCall(s, st)
		if pick != nil {
			t.Fatalf("want no pick, got %+v", pick)
		}
		if !strings.Contains(why, "would not reach") {
			t.Fatalf("reason should name the assignment test: %q", why)
		}
	})

	t.Run("premium already banked lets the same strike through", func(t *testing.T) {
		s, st := lot(call("c1", func(c *CallQuote) {
			c.Strike = entry
			c.Quote = q(1.00, 1.10)
		}))
		st.OptionPnL = 90 // an earlier call's credit: 105 + 90 = 195 > 187
		if pick, why := e.SelectCall(s, st); pick == nil {
			t.Fatalf("want a pick: %s", why)
		}
	})

	t.Run("dividends count and costs are charged", func(t *testing.T) {
		s, st := lot(call("c1", func(c *CallQuote) {
			c.Strike = entry
			c.Quote = q(1.00, 1.10) // 105
		}))
		st.Dividends = 90 // 195 > 187
		if pick, _ := e.SelectCall(s, st); pick == nil {
			t.Fatal("dividends should count toward the assignment test")
		}
		st.Costs = 12 // 183 < 187
		if pick, _ := e.SelectCall(s, st); pick != nil {
			t.Fatal("costs should be charged against the assignment test")
		}
	})

	t.Run("the reported figure is the one that passed", func(t *testing.T) {
		s, st := lot(call("c1"))
		pick, _ := e.SelectCall(s, st)
		if pick == nil {
			t.Fatal("want a pick")
		}
		// 100 x (190 - 187) + 0.85 x 100 = 385 on 18,700
		if want := 385.0 / 18_700; math.Abs(pick.Assigned-want) > 1e-9 {
			t.Fatalf("assigned %.6f, want %.6f", pick.Assigned, want)
		}
		if pick.Assigned < e.Config().RecoveryTarget {
			t.Fatal("a pick must clear the target by construction")
		}
	})
}

func TestSelectCallRanking(t *testing.T) {
	e := engine(t)

	t.Run("closest to the target delta wins", func(t *testing.T) {
		s, st := lot(
			call("far", func(c *CallQuote) { c.Delta = 0.29 }),
			call("near", func(c *CallQuote) { c.Delta = 0.26 }),
			call("mid", func(c *CallQuote) { c.Delta = 0.22 }),
		)
		pick, _ := e.SelectCall(s, st)
		if pick == nil || pick.Call.OptionID != "near" {
			t.Fatalf("want the 0.26 delta, got %+v", pick)
		}
	})

	t.Run("equal distance breaks to the larger credit", func(t *testing.T) {
		s, st := lot(
			call("thin", func(c *CallQuote) { c.Delta = 0.23; c.Quote = q(0.50, 0.60) }),
			call("rich", func(c *CallQuote) { c.Delta = 0.27; c.Quote = q(1.20, 1.30) }),
		)
		pick, _ := e.SelectCall(s, st)
		if pick == nil || pick.Call.OptionID != "rich" {
			t.Fatalf("want the richer of two equally distant deltas, got %+v", pick)
		}
	})

	t.Run("the order candidates arrive in does not matter", func(t *testing.T) {
		a := call("a", func(c *CallQuote) { c.Delta = 0.24 })
		b := call("b", func(c *CallQuote) { c.Delta = 0.26 })
		s1, st := lot(a, b)
		s2, _ := lot(b, a)
		p1, _ := e.SelectCall(s1, st)
		p2, _ := e.SelectCall(s2, st)
		if p1 == nil || p2 == nil || p1.Call.OptionID != p2.Call.OptionID {
			t.Fatalf("selection is not total: %+v vs %+v", p1, p2)
		}
	})
}

func TestSellLimitRespectsTheTick(t *testing.T) {
	e := engine(t)
	cases := []struct{ bid, ask, want float64 }{
		{0.80, 0.90, 0.85},   // penny tick below 3.00
		{0.826, 0.834, 0.83}, // rounded down to the penny
		{2.97, 3.01, 2.99},   // midpoint below the cutoff keeps the penny tick
		{3.10, 3.30, 3.20},   // nickel tick at and above 3.00
		{3.12, 3.19, 3.15},   // rounded down to the nickel
	}
	for _, c := range cases {
		if got := e.sellLimit(Quote{Bid: c.bid, Ask: c.ask}); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("sellLimit(%.3f/%.3f) = %.4f, want %.4f", c.bid, c.ask, got, c.want)
		}
	}
	// A sell limit is never below the standing bid.
	if got := e.sellLimit(Quote{Bid: 1.00, Ask: 1.00}); got < 1.00 {
		t.Errorf("sellLimit floored below the bid: %.4f", got)
	}
}

func TestRoundTickUp(t *testing.T) {
	e := engine(t)
	cases := []struct{ in, want float64 }{
		{0.87, 0.87},
		{0.861, 0.87},
		{3.13, 3.15},
		{3.15, 3.15},
	}
	for _, c := range cases {
		if got := e.roundTickUp(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("roundTickUp(%.4f) = %.4f, want %.4f", c.in, got, c.want)
		}
	}
}

func TestDTEDays(t *testing.T) {
	cases := []struct {
		exp  time.Time
		want int
	}{
		{date(2026, 9, 9), 0},
		{date(2026, 9, 11), 2},
		{date(2026, 9, 16), 7},
		{date(2026, 10, 2), 23},
	}
	for _, c := range cases {
		if got := dteDays(now, c.exp); got != c.want {
			t.Errorf("dteDays(%s) = %d, want %d", c.exp.Format("2006-01-02"), got, c.want)
		}
	}
	// The snapshot clock is an instant, the expiration a date: late in the
	// US session is still the same trading day in UTC terms used here.
	if got := dteDays(time.Date(2026, 9, 9, 23, 59, 0, 0, time.UTC), date(2026, 9, 11)); got != 2 {
		t.Errorf("late-in-day snapshot gave %d DTE, want 2", got)
	}
}
