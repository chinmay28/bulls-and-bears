package rotation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinmay28/bulls-and-bears/server/internal/journal"
	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

func buyLot() xlksata.Intent {
	return xlksata.Intent{Kind: xlksata.BuyEquity, Symbol: xlksata.Risk, Qty: 100}
}

func healthy() GateState {
	return GateState{Equity: 25_000, StartOfDayEquity: 25_000, HighWaterEquity: 25_000}
}

func TestGateKillSwitch(t *testing.T) {
	g := NewGate(risk.DefaultConfig(), 0)

	st := healthy()
	st.HighWaterEquity = 30_000
	st.Equity = 26_000 // -13.3%, past the 10% switch
	d := g.Check("i1", buyLot(), 18_800, st)
	if d.Allowed {
		t.Fatal("want a rejection past the drawdown kill switch")
	}
	if !strings.Contains(d.Reason, "kill switch") {
		t.Errorf("reason: %q", d.Reason)
	}

	// The switch stops a close too: a halt is a halt.
	sell := xlksata.Intent{Kind: xlksata.SellEquity, Symbol: xlksata.Risk, Qty: 100}
	if d := g.Check("i2", sell, 18_800, st); d.Allowed {
		t.Fatal("the kill switch halts everything, not just opens")
	}
}

func TestGateConsecutiveLimitHitsHalt(t *testing.T) {
	g := NewGate(risk.DefaultConfig(), 0)
	st := healthy()
	st.DailyLimitHits = 3
	if d := g.Check("i1", buyLot(), 18_800, st); d.Allowed {
		t.Fatal("three daily-limit days in a row should halt")
	}
}

func TestGateMaxOrdersPerDay(t *testing.T) {
	g := NewGate(risk.DefaultConfig(), 0)
	st := healthy()
	st.OrdersToday = 10
	d := g.Check("i1", buyLot(), 18_800, st)
	if d.Allowed || !strings.Contains(d.Reason, "limit 10") {
		t.Fatalf("got %+v", d)
	}
	st.OrdersToday = 9
	if d := g.Check("i1", buyLot(), 18_800, st); !d.Allowed {
		t.Fatalf("the tenth order is still inside the limit: %+v", d)
	}
}

// The daily loss limit stops opening orders and lets the book get out.
func TestGateDailyLossLimit(t *testing.T) {
	g := NewGate(risk.DefaultConfig(), 0)
	st := healthy()
	st.StartOfDayEquity = 25_000
	st.Equity = 24_200 // -3.2%, past the 3% limit

	if d := g.Check("i1", buyLot(), 18_800, st); d.Allowed {
		t.Fatal("an opening order should be refused after the daily limit")
	}

	// Everything that reduces the lot's exposure is still allowed.
	for _, in := range []xlksata.Intent{
		{Kind: xlksata.SellEquity, Symbol: xlksata.Risk, Qty: 100},
		{Kind: xlksata.SellCallToOpen, Symbol: xlksata.Risk, Qty: 1, Limit: 0.85, OptionID: "c1"},
		{Kind: xlksata.BuyCallToClose, Symbol: xlksata.Risk, Qty: 1, Limit: 1.40, OptionID: "c1"},
	} {
		if d := g.Check("i2", in, 18_800, st); !d.Allowed {
			t.Errorf("%s should still be allowed after the daily limit: %s", in.Kind, d.Reason)
		}
	}

	// A sweep into SATA is a buy, and is refused with the rest of them.
	sweep := xlksata.Intent{Kind: xlksata.BuyEquity, Symbol: xlksata.Park, Qty: 10}
	if d := g.Check("i3", sweep, 1_000, st); d.Allowed {
		t.Error("a sweep is an opening order and waits for tomorrow")
	}
}

// A written call carries the position effect "open" but reduces the lot's
// exposure, and is the only way a recovery makes progress.
func TestOpeningCountsBuysOnly(t *testing.T) {
	cases := map[xlksata.Kind]bool{
		xlksata.BuyEquity:      true,
		xlksata.SellEquity:     false,
		xlksata.SellCallToOpen: false,
		xlksata.BuyCallToClose: false,
	}
	for kind, want := range cases {
		if got := Opening(xlksata.Intent{Kind: kind}); got != want {
			t.Errorf("Opening(%s) = %v, want %v", kind, got, want)
		}
	}
}

func TestNotional(t *testing.T) {
	// An equity leg is shares times price.
	if got := Notional(buyLot(), 188.00); got != 18_800 {
		t.Errorf("equity notional %v, want 18800", got)
	}
	// An option leg is premium times the contract multiplier, not the
	// underlying's price.
	call := xlksata.Intent{Kind: xlksata.SellCallToOpen, Qty: 1, Limit: 0.85}
	if got := Notional(call, 188.00); got != 85 {
		t.Errorf("option notional %v, want 85", got)
	}
	back := xlksata.Intent{Kind: xlksata.BuyCallToClose, Qty: 1, Limit: 1.40}
	if got := Notional(back, 188.00); got != 140 {
		t.Errorf("buy-back notional %v, want 140", got)
	}
}

// The confirmation threshold is the one rule that changes with -live. A
// 100-share XLK lot is far over the default $500, so unattended live running
// needs -yes and says so.
func TestGateConfirmationThreshold(t *testing.T) {
	g := NewGate(risk.DefaultConfig(), 0)

	st := healthy()
	st.Live = true
	d := g.Check("i1", buyLot(), 18_800, st)
	if d.Allowed {
		t.Fatal("an unconfirmed live order over the threshold should not be placed")
	}
	if !strings.Contains(d.Reason, "-yes") {
		t.Errorf("the reason should say how to proceed: %q", d.Reason)
	}

	st.Confirmed = true
	d = g.Check("i1", buyLot(), 18_800, st)
	if !d.Allowed || !d.NeedsConfirm {
		t.Fatalf("with -yes it is allowed and still marked: %+v", d)
	}

	// A review-only run never asks.
	st = healthy()
	if d := g.Check("i1", buyLot(), 18_800, st); !d.Allowed || d.NeedsConfirm {
		t.Fatalf("a dry run should not need confirmation: %+v", d)
	}

	// A small order is under the threshold either way.
	st.Live = true
	small := xlksata.Intent{Kind: xlksata.SellCallToOpen, Qty: 1, Limit: 0.85}
	if d := g.Check("i2", small, 85, st); !d.Allowed || d.NeedsConfirm {
		t.Fatalf("85 is under the 500 threshold: %+v", d)
	}
}

func TestGateNotionalCap(t *testing.T) {
	g := NewGate(risk.DefaultConfig(), 15_000)
	if d := g.Check("i1", buyLot(), 18_800, healthy()); d.Allowed {
		t.Fatal("an order over the cap should be refused")
	}
	g = NewGate(risk.DefaultConfig(), 0) // uncapped
	if d := g.Check("i1", buyLot(), 18_800, healthy()); !d.Allowed {
		t.Fatal("zero means uncapped")
	}
}

func TestMarksRoll(t *testing.T) {
	// A first run sets the day's opening equity and the high-water mark.
	m := Marks{}.Roll("2026-09-09", 25_000)
	if m.StartOfDayEquity != 25_000 || m.HighWaterEquity != 25_000 || m.DailyLimitHits != 0 {
		t.Fatalf("got %+v", m)
	}

	// Within a day only the high-water mark moves.
	m.OrdersToday = 3
	m = m.Roll("2026-09-09", 26_000)
	if m.HighWaterEquity != 26_000 || m.StartOfDayEquity != 25_000 || m.OrdersToday != 3 {
		t.Fatalf("got %+v", m)
	}
	m = m.Roll("2026-09-09", 24_000)
	if m.HighWaterEquity != 26_000 {
		t.Fatalf("the high-water mark does not fall: %+v", m)
	}

	// A new day resets the day-scoped fields and keeps the mark.
	next := m.Roll("2026-09-10", 24_000)
	if next.StartOfDayEquity != 24_000 || next.OrdersToday != 0 || next.HighWaterEquity != 26_000 {
		t.Fatalf("got %+v", next)
	}

	// A day that hit the limit increments the consecutive count; one that
	// did not clears it.
	hit := Marks{Day: "2026-09-09", LimitHitToday: true, DailyLimitHits: 1}
	if got := hit.Roll("2026-09-10", 20_000); got.DailyLimitHits != 2 {
		t.Errorf("consecutive hits %d, want 2", got.DailyLimitHits)
	}
	clean := Marks{Day: "2026-09-09", DailyLimitHits: 2}
	if got := clean.Roll("2026-09-10", 20_000); got.DailyLimitHits != 0 {
		t.Errorf("a clean day should clear the streak, got %d", got.DailyLimitHits)
	}
}

func TestMarksSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	want := Marks{
		Day: "2026-09-09", StartOfDayEquity: 25_000, HighWaterEquity: 26_000,
		OrdersToday: 4, LimitHitToday: true, DailyLimitHits: 1,
	}
	if err := Save(dir, Book{Marks: want}, now); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Marks != want {
		t.Fatalf("got %+v, want %+v", got.Marks, want)
	}
}

// docs/PLAN.md §4.5: every order_submitted follows an allowed risk_decision
// for the same intent. journal.Check is the invariant; this is the proof
// that a real cycle satisfies it.
func TestCycleJournalSatisfiesTheOrderInvariant(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	b.reader.portfolio.BuyingPower.BuyingPower = 20_000
	b.reader.portfolio.TotalValue = 25_000

	c := cycler(t, b, dir)
	c.Gate = NewGate(risk.DefaultConfig(), 0)
	res, err := c.Run(context.Background(), xlksata.PhaseEntry, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Placed) != 1 || !res.Placed[0].Placed {
		t.Fatalf("nothing was placed (%s)", res.Plan.Reason)
	}

	events := readJournal(t, dir)
	if err := journal.Check(events); err != nil {
		t.Fatalf("journal violates the order invariant: %v", err)
	}
	var decisions, orders int
	for _, ev := range events {
		switch ev.Kind {
		case journal.KindRiskDecision:
			decisions++
			if ev.IntentID != "intent-1" {
				t.Errorf("decision intent id %q", ev.IntentID)
			}
		case journal.KindOrderSubmitted:
			orders++
			if ev.IntentID != "intent-1" {
				t.Errorf("order intent id %q", ev.IntentID)
			}
		}
	}
	if decisions != 1 || orders != 1 {
		t.Fatalf("%d decisions, %d orders", decisions, orders)
	}
}

// A refused intent is journalled and no order follows it.
func TestCycleRefusedIntentPlacesNothing(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	b.reader.portfolio.BuyingPower.BuyingPower = 20_000
	b.reader.portfolio.TotalValue = 25_000

	c := cycler(t, b, dir)
	c.Gate = NewGate(risk.DefaultConfig(), 1_000) // the lot is far over
	res, err := c.Run(context.Background(), xlksata.PhaseEntry, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Placed) != 0 || len(b.writer.placedEq) != 0 {
		t.Fatalf("placed despite a refusal: %+v", res.Placed)
	}
	if len(res.Decisions) != 1 || res.Decisions[0].Allowed {
		t.Fatalf("decisions: %+v", res.Decisions)
	}
	events := readJournal(t, dir)
	if err := journal.Check(events); err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Kind == journal.KindOrderSubmitted {
			t.Fatal("an order followed a refused decision")
		}
	}
	// The refused order does not count against the day's ten.
	book, _ := Load(dir)
	if book.Marks.OrdersToday != 0 {
		t.Errorf("orders today %d, want 0", book.Marks.OrdersToday)
	}
}

func TestCycleCountsOrdersAgainstTheDay(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	b.reader.portfolio.BuyingPower.BuyingPower = 20_000
	b.reader.portfolio.TotalValue = 25_000

	c := cycler(t, b, dir)
	c.Gate = NewGate(risk.DefaultConfig(), 0)
	if _, err := c.Run(context.Background(), xlksata.PhaseEntry, now); err != nil {
		t.Fatal(err)
	}
	book, _ := Load(dir)
	if book.Marks.OrdersToday != 1 {
		t.Fatalf("orders today %d, want 1", book.Marks.OrdersToday)
	}
	if book.Marks.Day == "" || book.Marks.StartOfDayEquity != 25_000 {
		t.Fatalf("marks: %+v", book.Marks)
	}
}

func readJournal(t *testing.T, dir string) []journal.Event {
	t.Helper()
	jdir := filepath.Join(dir, "journal")
	runs, err := journal.ListRuns(jdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want one run journal, got %v", runs)
	}
	events, err := journal.Read(journal.File(jdir, runs[0]))
	if err != nil {
		t.Fatal(err)
	}
	return events
}
