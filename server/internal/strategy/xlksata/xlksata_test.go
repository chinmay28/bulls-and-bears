package xlksata

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// The fixture is one lot of 100 XLK bought at 187.00, so the basis is 18,700
// and the recovery target is 187.00 of combined profit. Every number below
// is chosen against those two.
const entry = 187.00

var now = time.Date(2026, 9, 9, 19, 12, 0, 0, time.UTC) // 12:12 PT

func q(bid, ask float64) Quote {
	return Quote{Bid: bid, Ask: ask, Last: (bid + ask) / 2, At: now.Add(-time.Minute)}
}

func engine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(Defaults())
	if err != nil {
		t.Fatalf("New(Defaults()): %v", err)
	}
	return e
}

// base is a flat book with cash and SATA, which every case edits.
func base() Snapshot {
	return Snapshot{
		Now:        now,
		Phase:      PhaseEntry,
		XLK:        q(187.80, 188.00),
		SATA:       q(100.00, 100.10),
		SATAShares: 300,
		Cash:       0,
	}
}

// held puts the fixture lot on the book in the given mode.
func held(s Snapshot, mode Mode) Snapshot {
	s.State = State{Mode: mode, EntryPrice: entry, EntryDate: now.Add(-24 * time.Hour)}
	s.XLKShares = 100
	return s
}

func onlyIntent(t *testing.T, p Plan) Intent {
	t.Helper()
	if len(p.Intents) != 1 {
		t.Fatalf("want exactly 1 intent, got %d (%s)", len(p.Intents), p.Reason)
	}
	return p.Intents[0]
}

func TestConfigCheck(t *testing.T) {
	bad := map[string]func(*Config){
		"no shares":       func(c *Config) { c.LotShares = 0 },
		"targets crossed": func(c *Config) { c.RecoveryTarget = 0.0001 },
		"empty dte":       func(c *Config) { c.MinDTE, c.MaxDTE = 7, 2 },
		"empty delta":     func(c *Config) { c.MinDelta, c.MaxDelta = 0.30, 0.20 },
		"delta over one":  func(c *Config) { c.MaxDelta = 1.5 },
		"target outside":  func(c *Config) { c.TargetDelta = 0.9 },
		"negative buffer": func(c *Config) { c.FundingBuffer = -0.1 },
		"no quote age":    func(c *Config) { c.MaxQuoteAge = 0 },
		"no tick":         func(c *Config) { c.TickBelow = 0 },
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			c := Defaults()
			mutate(&c)
			if _, err := New(c); !errors.Is(err, ErrHalt) {
				t.Fatalf("want ErrHalt, got %v", err)
			}
		})
	}
	if _, err := New(Defaults()); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
}

// A snapshot the engine cannot trust is a halt, never a guess.
func TestDecideFailsClosed(t *testing.T) {
	e := engine(t)
	cases := map[string]func(*Snapshot){
		"no snapshot clock": func(s *Snapshot) { s.Now = time.Time{} },
		"stale risk quote":  func(s *Snapshot) { s.XLK.At = now.Add(-16 * time.Minute) },
		"stale park quote":  func(s *Snapshot) { s.SATA.At = now.Add(-time.Hour) },
		"quote from the future": func(s *Snapshot) {
			s.XLK.At = now.Add(5 * time.Minute)
		},
		"no bid":          func(s *Snapshot) { s.XLK.Bid = 0 },
		"crossed market":  func(s *Snapshot) { s.XLK.Bid, s.XLK.Ask = 188.10, 188.00 },
		"untimed quote":   func(s *Snapshot) { s.XLK.At = time.Time{} },
		"negative cash":   func(s *Snapshot) { s.Cash = -1 },
		"unknown phase":   func(s *Snapshot) { s.Phase = Phase(99) },
		"shares unknown":  func(s *Snapshot) { s.XLKShares = 100 },
		"open, no entry":  func(s *Snapshot) { s.State = State{Mode: Held}; s.XLKShares = 100 },
		"lot wrong size":  func(s *Snapshot) { *s = held(*s, Recovery); s.XLKShares = 50 },
		"two short calls": func(s *Snapshot) { *s = held(*s, Recovery); s.ShortCalls = []ShortCall{{}, {}} },
		"unwritten short call": func(s *Snapshot) {
			*s = held(*s, Recovery)
			s.ShortCalls = []ShortCall{{OptionID: "x", Strike: 190}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := base()
			mutate(&s)
			if _, err := e.Decide(s); !errors.Is(err, ErrHalt) {
				t.Fatalf("want ErrHalt, got %v", err)
			}
		})
	}
}

// A quote the engine does not use is not a reason to halt: SATA is only
// traded from a flat book, so a bad SATA quote must not stop a recovery.
func TestStaleParkQuoteDoesNotStopRecovery(t *testing.T) {
	e := engine(t)
	s := held(base(), Recovery)
	s.Phase = PhaseManage
	s.SATA = Quote{}
	if _, err := e.Decide(s); err != nil {
		t.Fatalf("recovery must not need a %s quote: %v", Park, err)
	}
}

func TestWorkingOrderPlacesNothing(t *testing.T) {
	e := engine(t)
	s := base()
	s.Cash = 20_000
	s.WorkingOrders = 1
	p, err := e.Decide(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Intents) != 0 {
		t.Fatalf("want no intents while an order works, got %v", p.Intents)
	}
	if !strings.Contains(p.Reason, "still working") {
		t.Fatalf("reason should say why: %q", p.Reason)
	}
}

func TestEntry(t *testing.T) {
	e := engine(t)

	t.Run("cash on hand buys the lot", func(t *testing.T) {
		s := base()
		s.Cash = 20_000
		got := onlyIntent(t, decide(t, e, s))
		want := Intent{Kind: BuyEquity, Symbol: Risk, Qty: 100}
		if got.Kind != want.Kind || got.Symbol != want.Symbol || got.Qty != want.Qty {
			t.Fatalf("got %+v, want a market buy of 100 %s", got, Risk)
		}
		if got.Limit != 0 {
			t.Fatalf("the entry is a market order, got limit %.2f", got.Limit)
		}
	})

	t.Run("sells only enough SATA to fund the lot", func(t *testing.T) {
		s := base()
		got := onlyIntent(t, decide(t, e, s))
		// 100 x 188.00 = 18,800, plus the 0.5% buffer is 18,894, over a
		// 100.00 bid: 189 shares.
		if got.Kind != SellEquity || got.Symbol != Park || got.Qty != 189 {
			t.Fatalf("got %+v, want a sell of 189 %s", got, Park)
		}
		if got.Qty*s.SATA.Bid < 100*s.XLK.Ask {
			t.Fatalf("proceeds %.2f do not fund the %.2f lot", got.Qty*s.SATA.Bid, 100*s.XLK.Ask)
		}
	})

	t.Run("counts the cash it already has", func(t *testing.T) {
		s := base()
		s.Cash = 10_000
		got := onlyIntent(t, decide(t, e, s))
		if got.Qty != 89 { // (18,894 - 10,000) / 100.00, rounded up
			t.Fatalf("want 89 %s sold against 10,000 cash, got %g", Park, got.Qty)
		}
	})

	t.Run("will not part-fund a lot", func(t *testing.T) {
		s := base()
		s.SATAShares = 100 // 10,000, against an 18,800 lot
		p := decide(t, e, s)
		if len(p.Intents) != 0 {
			t.Fatalf("want no intents when the lot cannot be funded, got %v", p.Intents)
		}
		if !strings.Contains(p.Reason, "does not fund") {
			t.Fatalf("reason should say why: %q", p.Reason)
		}
	})

	t.Run("no second lot", func(t *testing.T) {
		for _, mode := range []Mode{Held, Recovery} {
			s := held(base(), mode)
			s.Cash = 50_000
			p := decide(t, e, s)
			if len(p.Intents) != 0 {
				t.Fatalf("%s: want no intents, got %v", mode, p.Intents)
			}
		}
	})

	t.Run("entry never sweeps", func(t *testing.T) {
		s := base()
		s.SATAShares = 0
		s.Cash = 5_000 // too little for a lot, but plenty to sweep
		p := decide(t, e, s)
		if len(p.Intents) != 0 {
			t.Fatalf("entry must not buy %s it is about to sell, got %v", Park, p.Intents)
		}
	})
}

func TestReview(t *testing.T) {
	e := engine(t)

	t.Run("clears the quick target and sells", func(t *testing.T) {
		s := held(base(), Held)
		s.Phase = PhaseReview
		s.XLK = q(187.30, 187.40) // +0.160%
		got := onlyIntent(t, decide(t, e, s))
		if got.Kind != SellEquity || got.Symbol != Risk || got.Qty != 100 || got.Limit != 0 {
			t.Fatalf("got %+v, want a market sell of the lot", got)
		}
	})

	// The target is inclusive. A 1,000.00 entry against a 1,001.50 bid is
	// used because both prices and the resulting 0.0015 are exact in binary
	// floating point, so the case tests the comparison rather than the
	// rounding of a price that only looks like the boundary.
	t.Run("exactly at the target counts as clearing it", func(t *testing.T) {
		s := base()
		s.Phase = PhaseReview
		s.State = State{Mode: Held, EntryPrice: 1000.00, EntryDate: now}
		s.XLKShares = 100
		s.XLK = q(1001.50, 1001.60)
		p := decide(t, e, s)
		if len(p.Intents) != 1 || p.Intents[0].Kind != SellEquity {
			t.Fatalf("+0.15%% is not short of +0.15%%: %+v (%s)", p.Intents, p.Reason)
		}
	})

	t.Run("misses and enters recovery", func(t *testing.T) {
		s := held(base(), Held)
		s.Phase = PhaseReview
		s.XLK = q(187.20, 187.30) // +0.107%
		p := decide(t, e, s)
		if p.Next.Mode != Recovery {
			t.Fatalf("want recovery, got %s", p.Next.Mode)
		}
		if p.Next.EntryPrice != entry {
			t.Fatalf("recovery must keep the original entry, got %.2f", p.Next.EntryPrice)
		}
		if !strings.Contains(p.Reason, "recovery") {
			t.Fatalf("reason should say so: %q", p.Reason)
		}
	})

	t.Run("a loss enters recovery, it does not sell", func(t *testing.T) {
		s := held(base(), Held)
		s.Phase = PhaseReview
		s.XLK = q(180.00, 180.10)
		p := decide(t, e, s)
		if p.Next.Mode != Recovery {
			t.Fatalf("want recovery, got %s", p.Next.Mode)
		}
		for _, in := range p.Intents {
			if in.Kind == SellEquity && in.Symbol == Risk {
				t.Fatalf("the rules never sell the lot at a loss: %+v", in)
			}
		}
	})
}

func TestRecovery(t *testing.T) {
	e := engine(t)

	recovery := func() Snapshot {
		s := held(base(), Recovery)
		s.Phase = PhaseManage
		return s
	}

	t.Run("takes the combined target", func(t *testing.T) {
		s := recovery()
		s.XLK = q(189.00, 189.10) // +200 on an 18,700 basis = +1.07%
		got := onlyIntent(t, decide(t, e, s))
		if got.Kind != SellEquity || got.Symbol != Risk || got.Qty != 100 {
			t.Fatalf("got %+v, want the lot sold", got)
		}
	})

	t.Run("holds below the target when no call qualifies", func(t *testing.T) {
		s := recovery()
		s.XLK = q(188.00, 188.10) // +100 = +0.53%
		p := decide(t, e, s)
		if len(p.Intents) != 0 {
			t.Fatalf("want to hold, got %v", p.Intents)
		}
		if !strings.Contains(p.Reason, "no call qualifies") {
			t.Fatalf("reason should say why: %q", p.Reason)
		}
	})

	t.Run("premium and dividends count toward the target", func(t *testing.T) {
		s := recovery()
		s.XLK = q(188.00, 188.10) // +100 of share P&L, 87 short of 187
		s.State.OptionPnL = 60
		s.State.Dividends = 30
		s.State.Costs = 2 // +188 combined, just over
		got := onlyIntent(t, decide(t, e, s))
		if got.Kind != SellEquity {
			t.Fatalf("got %+v, want the lot sold on combined profit", got)
		}
	})

	t.Run("SATA income does not count toward the target", func(t *testing.T) {
		s := recovery()
		s.XLK = q(188.00, 188.10)
		s.SATAShares = 100_000 // a fortune parked in SATA
		p := decide(t, e, s)
		if len(p.Intents) != 0 {
			t.Fatalf("%s must not fund the target: %v", Park, p.Intents)
		}
	})

	t.Run("closes the call before the shares", func(t *testing.T) {
		s := recovery()
		s.XLK = q(191.00, 191.10) // +400 of share P&L
		sc := ShortCall{OptionID: "c1", Expiration: date(2026, 9, 11), Strike: 190, Credit: 0.90}
		s.State.ShortCall = &sc
		s.State.OptionPnL = 90
		s.ShortCalls = []ShortCall{sc}
		s.ShortCallQuote = q(1.30, 1.40) // 140 to buy back: 400+90-140 = +350
		got := onlyIntent(t, decide(t, e, s))
		if got.Kind != BuyCallToClose || got.OptionID != "c1" || got.Qty != 1 {
			t.Fatalf("got %+v, want the short call bought back first", got)
		}
		if got.Limit != 1.40 {
			t.Fatalf("buy-back limit should be the ask on the tick, got %.2f", got.Limit)
		}
	})

	t.Run("the buy-back cost is netted before deciding", func(t *testing.T) {
		s := recovery()
		s.XLK = q(189.00, 189.10) // +200 of share P&L
		sc := ShortCall{OptionID: "c1", Expiration: date(2026, 9, 11), Strike: 188, Credit: 0.50}
		s.State.ShortCall = &sc
		s.State.OptionPnL = 50
		s.ShortCalls = []ShortCall{sc}
		s.ShortCallQuote = q(1.50, 1.60) // 200+50-160 = +90, short of 187
		p := decide(t, e, s)
		if len(p.Intents) != 0 {
			t.Fatalf("want to hold: the target is not realisable, got %v", p.Intents)
		}
	})

	t.Run("never writes a second call", func(t *testing.T) {
		s := recovery()
		s.XLK = q(188.00, 188.10)
		sc := ShortCall{OptionID: "c1", Expiration: date(2026, 9, 11), Strike: 190, Credit: 0.90}
		s.State.ShortCall = &sc
		s.State.OptionPnL = 90
		s.ShortCalls = []ShortCall{sc}
		s.ShortCallQuote = q(0.60, 0.70)
		s.Calls = []CallQuote{{OptionID: "c2", Expiration: date(2026, 9, 11), Strike: 192, Delta: 0.25, Quote: q(1.00, 1.10), Tradable: true}}
		p := decide(t, e, s)
		if len(p.Intents) != 0 {
			t.Fatalf("one call at a time: got %v", p.Intents)
		}
	})

	t.Run("writes a qualifying call", func(t *testing.T) {
		s := recovery()
		s.XLK = q(188.00, 188.10)
		s.Calls = []CallQuote{{OptionID: "c1", Expiration: date(2026, 9, 11), Strike: 190, Delta: 0.26, Quote: q(0.80, 0.90), Tradable: true}}
		got := onlyIntent(t, decide(t, e, s))
		if got.Kind != SellCallToOpen || got.OptionID != "c1" || got.Qty != 1 {
			t.Fatalf("got %+v, want one call written", got)
		}
		if got.Limit != 0.85 {
			t.Fatalf("want the midpoint on the tick (0.85), got %.2f", got.Limit)
		}
	})
}

func TestManageAndSweep(t *testing.T) {
	e := engine(t)

	t.Run("sweeps idle cash into SATA", func(t *testing.T) {
		s := base()
		s.Phase = PhaseManage
		s.Cash = 1_000
		got := onlyIntent(t, decide(t, e, s))
		if got.Kind != BuyEquity || got.Symbol != Park {
			t.Fatalf("got %+v, want idle cash swept to %s", got, Park)
		}
		if cost := got.Qty * s.SATA.Ask; cost > s.Cash {
			t.Fatalf("sweep of %.4f shares costs %.2f against %.2f cash: that is margin", got.Qty, cost, s.Cash)
		}
	})

	t.Run("dust is left alone", func(t *testing.T) {
		s := base()
		s.Phase = PhaseManage
		s.Cash = 0.40
		p := decide(t, e, s)
		if len(p.Intents) != 0 {
			t.Fatalf("want no dust order, got %v", p.Intents)
		}
	})

	t.Run("a held lot waits for its review", func(t *testing.T) {
		s := held(base(), Held)
		s.Phase = PhaseManage
		s.XLK = q(190.00, 190.10) // well past the quick target
		p := decide(t, e, s)
		if len(p.Intents) != 0 {
			t.Fatalf("the quick target is a 12:07 test, not a standing one: %v", p.Intents)
		}
	})
}

func TestReconcile(t *testing.T) {
	e := engine(t)

	t.Run("assignment completes the trade", func(t *testing.T) {
		s := held(base(), Recovery)
		s.Phase = PhaseManage
		sc := ShortCall{OptionID: "c1", Expiration: date(2026, 9, 11), Strike: 190, Credit: 0.90}
		s.State.ShortCall = &sc
		s.State.OptionPnL = 90
		s.XLKShares = 0 // called away
		s.Cash = 19_000
		p := decide(t, e, s)
		if p.Next.Mode != Flat || p.Next.ShortCall != nil {
			t.Fatalf("assignment leaves the book flat, got %+v", p.Next)
		}
		if !strings.Contains(p.Reason, "assigned") {
			t.Fatalf("reason should record it: %q", p.Reason)
		}
		// 100 x (190 - 187) + 90 = 390 on 18,700 = +2.086%
		if !strings.Contains(p.Reason, "+2.09%") && !strings.Contains(p.Reason, "+2.086") {
			t.Fatalf("reason should carry the return: %q", p.Reason)
		}
		got := onlyIntent(t, p)
		if got.Kind != BuyEquity || got.Symbol != Park {
			t.Fatalf("proceeds sweep to %s, got %+v", Park, got)
		}
	})

	t.Run("an expired call leaves the lot and the premium", func(t *testing.T) {
		s := held(base(), Recovery)
		s.Phase = PhaseManage
		sc := ShortCall{OptionID: "c1", Expiration: date(2026, 9, 4), Strike: 190, Credit: 0.90}
		s.State.ShortCall = &sc
		s.State.OptionPnL = 90
		s.XLK = q(188.00, 188.10)
		p := decide(t, e, s)
		if p.Next.ShortCall != nil {
			t.Fatalf("the call is gone, state should say so: %+v", p.Next.ShortCall)
		}
		if p.Next.Mode != Recovery || p.Next.OptionPnL != 90 {
			t.Fatalf("the lot and its premium survive: %+v", p.Next)
		}
	})
}

// Decide is pure: the same snapshot answers the same way, and the snapshot
// it was given is not modified.
func TestDecideIsPure(t *testing.T) {
	e := engine(t)
	s := held(base(), Recovery)
	s.Phase = PhaseManage
	s.XLK = q(188.00, 188.10)
	s.Calls = []CallQuote{
		{OptionID: "b", Expiration: date(2026, 9, 11), Strike: 191, Delta: 0.24, Quote: q(0.70, 0.80), Tradable: true},
		{OptionID: "a", Expiration: date(2026, 9, 11), Strike: 190, Delta: 0.26, Quote: q(0.80, 0.90), Tradable: true},
	}
	before := s
	first := decide(t, e, s)
	second := decide(t, e, s)
	if first.Reason != second.Reason || len(first.Intents) != len(second.Intents) {
		t.Fatalf("two calls disagreed:\n %s\n %s", first.Reason, second.Reason)
	}
	if len(first.Intents) > 0 && first.Intents[0] != second.Intents[0] {
		t.Fatalf("two calls chose differently: %+v vs %+v", first.Intents[0], second.Intents[0])
	}
	if s.State != before.State || s.Cash != before.Cash {
		t.Fatal("Decide modified its snapshot")
	}
}

func TestApply(t *testing.T) {
	e := engine(t)

	st := e.ApplyEntry(187.00, now)
	if st.Mode != Held || st.EntryPrice != 187 {
		t.Fatalf("entry: %+v", st)
	}
	if e.Basis(st) != 18_700 {
		t.Fatalf("basis: %.2f", e.Basis(st))
	}

	st = e.ApplyCallOpened(st, ShortCall{OptionID: "c1", Strike: 190, Credit: 0.90})
	if st.OptionPnL != 90 || st.ShortCall == nil {
		t.Fatalf("written call: %+v", st)
	}
	st = e.ApplyDividend(st, 25)
	st = e.ApplyCost(st, 1.30)
	st = e.ApplyCallClosed(st, 0.40)
	if st.ShortCall != nil {
		t.Fatalf("closed call should be cleared: %+v", st.ShortCall)
	}
	if math.Abs(st.OptionPnL-50) > 1e-9 {
		t.Fatalf("90 received less 40 paid is 50, got %.2f", st.OptionPnL)
	}
	// 100 x (188 - 187) + 50 + 25 - 1.30 = 173.70 on 18,700
	if got := e.CombinedPct(st, 188.00, 0); math.Abs(got-173.70/18_700) > 1e-9 {
		t.Fatalf("combined: %.6f", got)
	}
	if flat := e.ApplyExit(st); flat != (State{}) {
		t.Fatalf("an exit leaves nothing behind: %+v", flat)
	}
}

// decide is Decide with the halt path turned into a test failure, which is
// what every case below that is not about halting wants.
func decide(t *testing.T, e *Engine, s Snapshot) Plan {
	t.Helper()
	p, err := e.Decide(s)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return p
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
