package risk

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/broker"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

var now = time.Date(2026, 9, 4, 19, 50, 0, 0, time.UTC)

func quote(sym string, bid, ask float64, age time.Duration) marketdata.Quote {
	return marketdata.Quote{Symbol: sym, Bid: bid, Ask: ask, Last: (bid + ask) / 2, At: now.Add(-age)}
}

// healthy is an account with nothing wrong: 10,000 equity, flat, fresh quotes.
func healthy() State {
	return State{
		Now: now, Equity: 10_000, StartOfDayEquity: 10_000, HighWaterEquity: 10_000,
		Quotes: []marketdata.Quote{quote("GLD", 230, 231, time.Minute), quote("GDX", 41, 42, time.Minute)},
	}
}

func buy(id, sym string, qty float64) broker.Order {
	return broker.Order{IntentID: id, Symbol: sym, Side: broker.Buy, Qty: qty}
}

func sell(id, sym string, qty float64) broker.Order {
	return broker.Order{IntentID: id, Symbol: sym, Side: broker.Sell, Qty: qty}
}

func gate() *RuleGate {
	return NewGate(DefaultConfig(), Limits{MaxNotionalPerLegUSD: 2000, GrossLeverage: 0.9})
}

func TestRulesOneAtATime(t *testing.T) {
	cases := []struct {
		name    string
		state   func(State) State
		intents []broker.Order
		allowed []bool
		reason  string // substring expected on the first rejected decision
	}{
		{"within limits", nil, []broker.Order{buy("a", "GLD", 5), sell("b", "GDX", 20)}, []bool{true, true}, ""},
		{"drawdown kill switch rejects everything", func(s State) State {
			s.HighWaterEquity = 11_200 // 10,000 is 10.7% below
			return s
		}, []broker.Order{buy("a", "GLD", 1), sell("b", "GDX", 1)}, []bool{false, false}, "kill switch"},
		{"three daily-limit hits halt", func(s State) State { s.DailyLimitHits = 3; return s }, []broker.Order{buy("a", "GLD", 1)}, []bool{false}, "days in a row"},
		{"no quote", nil, []broker.Order{buy("a", "SPY", 1)}, []bool{false}, "no quote"},
		{"stale quote", func(s State) State {
			s.Quotes = []marketdata.Quote{quote("GLD", 230, 231, 16*time.Minute)}
			return s
		}, []broker.Order{buy("a", "GLD", 1)}, []bool{false}, "old"},
		{"quote on the boundary is fresh", func(s State) State {
			s.Quotes = []marketdata.Quote{quote("GLD", 230, 231, 15*time.Minute)}
			return s
		}, []broker.Order{buy("a", "GLD", 1)}, []bool{true}, ""},
		{"per-leg notional", nil, []broker.Order{buy("a", "GLD", 9)}, []bool{false}, "per-leg cap"}, // 9 × 231 = 2079
		{"daily loss: opener rejected", func(s State) State { s.Equity = 9_700; return s }, []broker.Order{buy("a", "GLD", 1)}, []bool{false}, "no new positions"},
		{"daily loss: close allowed", func(s State) State {
			s.Equity = 9_700
			s.Positions = []strategy.Position{{Symbol: "GLD", Qty: 5, AvgCost: 230}}
			return s
		}, []broker.Order{sell("a", "GLD", 5)}, []bool{true}, ""},
		{"daily loss: partial close allowed", func(s State) State {
			s.Equity = 9_700
			s.Positions = []strategy.Position{{Symbol: "GLD", Qty: 5, AvgCost: 230}}
			return s
		}, []broker.Order{sell("a", "GLD", 2)}, []bool{true}, ""},
		{"daily loss: a flip is rejected", func(s State) State {
			s.Equity = 9_700
			s.Positions = []strategy.Position{{Symbol: "GLD", Qty: 5, AvgCost: 230}}
			return s
		}, []broker.Order{sell("a", "GLD", 6)}, []bool{false}, "open the other way"},
		{"daily loss: buy against a short is a close", func(s State) State {
			s.Equity = 9_700
			s.Positions = []strategy.Position{{Symbol: "GDX", Qty: -20, AvgCost: 41}}
			return s
		}, []broker.Order{buy("a", "GDX", 20)}, []bool{true}, ""},
		{"orders per day", func(s State) State { s.OrdersToday = 9; return s }, []broker.Order{buy("a", "GLD", 1), buy("b", "GDX", 1)}, []bool{true, false}, "orders per day"},
		{"gross leverage: already at the cap", func(s State) State {
			s.Positions = []strategy.Position{{Symbol: "GLD", Qty: 40, AvgCost: 230}} // 40 × 230.5 = 9,220 > 9,000
			return s
		}, []broker.Order{buy("a", "GDX", 1)}, []bool{false}, "already at the cap"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := healthy()
			if c.state != nil {
				s = c.state(s)
			}
			got := gate().Check(context.Background(), c.intents, s)
			if len(got) != len(c.intents) {
				t.Fatalf("%d decisions for %d intents", len(got), len(c.intents))
			}
			for i, d := range got {
				if d.IntentID != c.intents[i].IntentID {
					t.Errorf("decision %d is for %s, want %s (order must be kept)", i, d.IntentID, c.intents[i].IntentID)
				}
				if d.Allowed != c.allowed[i] {
					t.Errorf("%s: allowed = %v (%s), want %v", d.IntentID, d.Allowed, d.Reason, c.allowed[i])
				}
				if d.Reason == "" {
					t.Errorf("%s: every decision needs a reason", d.IntentID)
				}
				if !d.Allowed && c.reason != "" && i == firstFalse(c.allowed) && !strings.Contains(d.Reason, c.reason) {
					t.Errorf("%s: reason %q, want it to mention %q", d.IntentID, d.Reason, c.reason)
				}
			}
		})
	}
}

func firstFalse(bs []bool) int {
	for i, b := range bs {
		if !b {
			return i
		}
	}
	return -1
}

func TestGrossLeverageScalesOpenersProportionally(t *testing.T) {
	s := healthy()
	// After the trades the book holds 8 GLD and −40 GDX, marked at Last:
	// 8 × 230.5 + 40 × 41.5 = 3,504 gross, fine under a cap of 9,000. Under
	// a cap of 2,000 both legs must be scaled by the same f ≈ 0.571.
	g := NewGate(DefaultConfig(), Limits{MaxNotionalPerLegUSD: 5000, GrossLeverage: 0.2})
	got := g.Check(context.Background(), []broker.Order{buy("a", "GLD", 8), sell("b", "GDX", 40)}, s)
	for _, d := range got {
		if !d.Allowed {
			t.Fatalf("%s rejected: %s", d.IntentID, d.Reason)
		}
		if !strings.Contains(d.Reason, "scaled") {
			t.Errorf("%s: reason %q, want a scaling note", d.IntentID, d.Reason)
		}
	}
	gross := got[0].Qty*230.5 + got[1].Qty*41.5
	if math.Abs(gross-2000) > 0.5 {
		t.Errorf("scaled gross = %.2f, want ≈ 2000", gross)
	}
	if math.Abs(got[0].Qty/8-got[1].Qty/40) > 1e-6 {
		t.Errorf("legs were not scaled by the same factor: %v %v", got[0].Qty/8, got[1].Qty/40)
	}
}

func TestClosesAreNotScaled(t *testing.T) {
	s := healthy()
	s.Positions = []strategy.Position{{Symbol: "GLD", Qty: 10, AvgCost: 230}}
	g := NewGate(DefaultConfig(), Limits{GrossLeverage: 0.2}) // cap 2,000; held 2,305
	got := g.Check(context.Background(), []broker.Order{sell("a", "GLD", 10)}, s)
	if !got[0].Allowed || got[0].Qty != 10 {
		t.Fatalf("closing under the cap should be allowed in full: %+v", got[0])
	}
}

func TestConfirmationOnlyWhenLive(t *testing.T) {
	s := healthy()
	got := gate().Check(context.Background(), []broker.Order{buy("a", "GLD", 5)}, s) // 1,155 > 500
	if got[0].NeedsConfirm {
		t.Error("dry-run must never ask for confirmation")
	}
	s.Live = true
	got = gate().Check(context.Background(), []broker.Order{buy("a", "GLD", 5), buy("b", "GDX", 1)}, s)
	if !got[0].NeedsConfirm || got[1].NeedsConfirm {
		t.Errorf("live: confirm = %v, %v; want the big order only", got[0].NeedsConfirm, got[1].NeedsConfirm)
	}
}

func TestKill(t *testing.T) {
	g := gate()
	s := healthy()
	if r, halt := g.Kill(s); halt {
		t.Fatalf("healthy account halted: %s", r)
	}
	s.HighWaterEquity = 11_112 // 10,000 is 10.006% below
	if r, halt := g.Kill(s); !halt || !strings.Contains(r, "kill switch") {
		t.Errorf("Kill = %q, %v", r, halt)
	}
	s = healthy()
	s.DailyLimitHits = 2
	if _, halt := g.Kill(s); halt {
		t.Error("two hits should not halt")
	}
	s.DailyLimitHits = 3
	if _, halt := g.Kill(s); !halt {
		t.Error("three hits should halt")
	}
}

func TestDailyLimitHit(t *testing.T) {
	g := gate()
	s := healthy()
	s.Equity = 9_701
	if g.DailyLimitHit(s) {
		t.Error("2.99% is not a hit")
	}
	s.Equity = 9_700
	if !g.DailyLimitHit(s) {
		t.Error("3.00% is a hit")
	}
}

func TestDivergence(t *testing.T) {
	cfg := DefaultConfig()
	for _, c := range []struct {
		paper, live float64
		want        string
	}{
		{10_000, 10_100, "ok"},
		{10_000, 9_800, "warn"},
		{10_000, 10_500, "halt"},
		{0, 10_000, "ok"},
	} {
		if got, _ := Divergence(c.paper, c.live, cfg); got != c.want {
			t.Errorf("Divergence(%v, %v) = %s, want %s", c.paper, c.live, got, c.want)
		}
	}
}

func TestSymbols(t *testing.T) {
	s := State{Positions: []strategy.Position{{Symbol: "GDX"}}}
	got := Symbols([]broker.Order{buy("a", "GLD", 1), buy("b", "GLD", 1)}, s)
	if strings.Join(got, ",") != "GDX,GLD" {
		t.Errorf("symbols = %v", got)
	}
}

func TestExampleConfigLoadsToDefaults(t *testing.T) {
	cfg, err := LoadConfig("../../../risk.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != DefaultConfig() {
		t.Errorf("risk.example.yaml = %+v, want the defaults %+v", cfg, DefaultConfig())
	}
}

func TestConfigParsing(t *testing.T) {
	cases := []struct {
		name, yaml, wantErr string
	}{
		{"empty is defaults", "", ""},
		{"unknown key", "max_orders_per_dayy: 3\n", "field max_orders_per_dayy not found"},
		{"percentage typed as a fraction", "daily_loss_limit: 3\n", "not a fraction"},
		{"warn above halt", "divergence_warn: 0.06\n", "above divergence_halt"},
		{"negative duration", "stale_quote: -1m\n", "negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := parseConfig(strings.NewReader(c.yaml))
			if c.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if cfg != DefaultConfig() {
					t.Errorf("cfg = %+v", cfg)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
		})
	}
}
