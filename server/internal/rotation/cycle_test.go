package rotation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/robinhood"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// broker is a reader, an order book and a writer in one, which is what a
// cycle takes.
type broker struct {
	*reader
	*orderBook
	*writer
}

func newBroker() *broker {
	return &broker{reader: baseReader(), orderBook: &orderBook{}, writer: &writer{}}
}

func cycler(t *testing.T, b *broker, dir string) *Cycler {
	t.Helper()
	return &Cycler{
		Broker: b, Engine: eng(t), Exec: &Executor{Broker: b.writer},
		DataDir: dir, JournalDir: filepath.Join(dir, "journal"),
		IntentID: func() string { return "intent-1" },
	}
}

func run(t *testing.T, b *broker, dir string, phase xlksata.Phase) Result {
	t.Helper()
	res, err := cycler(t, b, dir).Run(context.Background(), phase, now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// The whole life of one lot, one cycle at a time, with the book reread
// between each: the sequence the strategy is actually run in.
func TestCycleOpensALotOverTwoCycles(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	b.reader.equity = []robinhood.EquityPosition{{Symbol: "SATA", Quantity: 300}}

	// Cycle one: flat with no cash, so SATA is sold to fund the lot.
	res := run(t, b, dir, xlksata.PhaseEntry)
	if len(b.writer.placedEq) != 1 {
		t.Fatalf("nothing was placed (%s)", res.Plan.Reason)
	}
	if r := b.writer.placedEq[0]; r.Symbol != "SATA" || r.Side != robinhood.Sell {
		t.Fatalf("sent %+v, want a SATA sale", r)
	}
	book, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if book.Pending == nil || book.Pending.OrderID != "eq-1" {
		t.Fatalf("the placed order should be pending: %+v", book.Pending)
	}

	// Cycle two, before the sale has filled: nothing else goes out.
	b.orderBook.equity = []robinhood.Order{{ID: "eq-1", State: "confirmed"}}
	res = run(t, b, dir, xlksata.PhaseEntry)
	if len(b.writer.placedEq) != 1 {
		t.Fatalf("a second order went out while the first was working: %+v", b.writer.placedEq)
	}
	if !strings.Contains(res.Plan.Reason, "still working") {
		t.Errorf("reason: %q", res.Plan.Reason)
	}

	// Cycle three: the sale filled, and the cash buys the lot.
	b.orderBook.equity = []robinhood.Order{filled("eq-1", 189, 100.00)}
	b.reader.equity = []robinhood.EquityPosition{{Symbol: "SATA", Quantity: 111}}
	b.reader.portfolio.BuyingPower.BuyingPower = 18_900
	res = run(t, b, dir, xlksata.PhaseEntry)
	if len(b.writer.placedEq) != 2 {
		t.Fatalf("the lot was not bought (%s)", res.Plan.Reason)
	}
	if r := b.writer.placedEq[1]; r.Symbol != "XLK" || r.Side != robinhood.Buy || r.Qty != 100 {
		t.Fatalf("sent %+v, want 100 XLK", r)
	}

	// Cycle four: the buy filled, so the lot's basis is recorded from it.
	b.orderBook.equity = []robinhood.Order{filled("eq-1", 100, 188.05)}
	b.reader.equity = []robinhood.EquityPosition{
		{Symbol: "XLK", Quantity: 100}, {Symbol: "SATA", Quantity: 111},
	}
	b.reader.portfolio.BuyingPower.BuyingPower = 95
	res = run(t, b, dir, xlksata.PhaseEntry)
	if res.After.Mode != xlksata.Held || res.After.EntryPrice != 188.05 {
		t.Fatalf("lot: %+v (settled: %s)", res.After, res.Settled)
	}
	book, _ = Load(dir)
	if book.State.EntryPrice != 188.05 {
		t.Fatalf("the basis should be on disk: %+v", book.State)
	}
}

func TestCycleTakesTheQuickProfit(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	if err := Save(dir, Book{State: xlksata.State{
		Mode: xlksata.Held, EntryPrice: 187.00, EntryDate: now,
	}}, now); err != nil {
		t.Fatal(err)
	}
	b.reader.equity = []robinhood.EquityPosition{{Symbol: "XLK", Quantity: 100}}
	b.reader.quotes = map[string]robinhood.Quote{
		"XLK":  rhQuote(187.40, 187.50), // +0.214%
		"SATA": rhQuote(99.93, 100.00),
	}
	res := run(t, b, dir, xlksata.PhaseReview)
	if len(b.writer.placedEq) != 1 {
		t.Fatalf("the lot was not sold (%s)", res.Plan.Reason)
	}
	if r := b.writer.placedEq[0]; r.Symbol != "XLK" || r.Side != robinhood.Sell || r.Qty != 100 {
		t.Fatalf("sent %+v", r)
	}
}

func TestCycleEntersRecoveryAndRecordsIt(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	if err := Save(dir, Book{State: xlksata.State{
		Mode: xlksata.Held, EntryPrice: 187.00, EntryDate: now,
	}}, now); err != nil {
		t.Fatal(err)
	}
	b.reader.equity = []robinhood.EquityPosition{{Symbol: "XLK", Quantity: 100}}
	b.reader.quotes = map[string]robinhood.Quote{
		"XLK":  rhQuote(187.10, 187.20), // +0.053%, short of the target
		"SATA": rhQuote(99.93, 100.00),
	}
	res := run(t, b, dir, xlksata.PhaseReview)
	if res.After.Mode != xlksata.Recovery {
		t.Fatalf("want recovery, got %s", res.After.Mode)
	}
	// The mode change is a decision, not a fill, so it must be on disk even
	// though nothing was placed.
	book, _ := Load(dir)
	if book.State.Mode != xlksata.Recovery || book.State.EntryPrice != 187.00 {
		t.Fatalf("on disk: %+v", book.State)
	}
	if len(b.writer.placedEq) != 0 {
		t.Fatalf("the lot must not be sold at a loss: %+v", b.writer.placedEq)
	}
}

func TestCycleWritesACallAndSettlesIt(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	if err := Save(dir, Book{State: xlksata.State{
		Mode: xlksata.Recovery, EntryPrice: 187.00, EntryDate: now.Add(-24 * time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	b.reader.equity = []robinhood.EquityPosition{{Symbol: "XLK", Quantity: 100}}
	b.reader.chains = []robinhood.Chain{{
		ID: "chain", Symbol: "XLK", CanOpenPosition: true,
		Expirations: []string{"2026-09-11"},
	}}
	exp := robinhood.Time{Time: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)}
	b.reader.calls = []robinhood.Instrument{
		{ID: "c1", Strike: 190, Expiration: exp, State: "active", Tradability: "tradable"},
	}
	b.reader.optQuotes = map[string]robinhood.OptionQuote{
		"c1": {InstrumentID: "c1", BidPrice: 0.80, AskPrice: 0.90, MarkPrice: 0.85, Delta: 0.26,
			UpdatedAt: robinhood.Time{Time: now}},
	}

	res := run(t, b, dir, xlksata.PhaseManage)
	if len(b.writer.placedOpt) != 1 {
		t.Fatalf("no call was written (%s)", res.Plan.Reason)
	}
	r := b.writer.placedOpt[0]
	if r.OptionID != "c1" || r.Side != robinhood.Sell || !r.Open || r.Limit != 0.85 {
		t.Fatalf("sent %+v", r)
	}
	book, _ := Load(dir)
	if book.Pending == nil || book.Pending.Strike != 190 || book.Pending.OptionID != "c1" {
		t.Fatalf("the pending call should carry its terms: %+v", book.Pending)
	}

	// The next cycle settles the fill and records the premium.
	b.orderBook.option = []robinhood.Order{filled("opt-1", 1, 85)}
	b.reader.options = []robinhood.OptionPosition{
		{OptionID: "c1", ChainSymbol: "XLK", Type: "short", Quantity: 1, AveragePrice: 85, Multiplier: 100},
	}
	b.reader.instrument["c1"] = robinhood.Instrument{ID: "c1", Strike: 190, Expiration: exp}
	b.reader.optQuotes["c1"] = robinhood.OptionQuote{
		InstrumentID: "c1", BidPrice: 0.80, AskPrice: 0.90, MarkPrice: 0.85,
		UpdatedAt: robinhood.Time{Time: now},
	}
	res = run(t, b, dir, xlksata.PhaseManage)
	if res.After.OptionPnL != 85 {
		t.Fatalf("premium: %v (settled: %s)", res.After.OptionPnL, res.Settled)
	}
	if res.After.ShortCall == nil || res.After.ShortCall.Credit != 0.85 {
		t.Fatalf("call: %+v", res.After.ShortCall)
	}
	if len(b.writer.placedOpt) != 1 {
		t.Fatal("a second call must not be written")
	}
}

// A book that says an order may be outstanding stops the next cycle even if
// the working-order count comes back zero.
func TestCyclePendingOrderBlocksEvenIfTheCountSaysZero(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	b.reader.portfolio.BuyingPower.BuyingPower = 20_000
	if err := Save(dir, Book{Pending: &Pending{
		OrderID: "ghost-but-working", Kind: xlksata.BuyEquity, Symbol: "XLK",
	}}, now); err != nil {
		t.Fatal(err)
	}
	// The broker still shows it working, so Settle leaves it pending.
	b.orderBook.equity = []robinhood.Order{{ID: "ghost-but-working", State: "queued"}}
	b.reader.working = 0

	res := run(t, b, dir, xlksata.PhaseEntry)
	if len(b.writer.placedEq) != 0 {
		t.Fatalf("placed while an order was outstanding: %+v", b.writer.placedEq)
	}
	if !strings.Contains(res.Plan.Reason, "still working") {
		t.Errorf("reason: %q", res.Plan.Reason)
	}
}

// A book that disagrees with the account halts rather than trading.
func TestCycleHaltsOnABookItDoesNotRecognise(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	b.reader.equity = []robinhood.EquityPosition{{Symbol: "XLK", Quantity: 100}}
	// State says flat; the account holds a lot.
	_, err := cycler(t, b, dir).Run(context.Background(), xlksata.PhaseEntry, now)
	if err == nil {
		t.Fatal("want a halt")
	}
	if len(b.writer.placedEq) != 0 {
		t.Fatal("nothing should have been placed")
	}
}

func TestCycleDryRunPlacesNothing(t *testing.T) {
	dir := t.TempDir()
	b := newBroker()
	b.reader.portfolio.BuyingPower.BuyingPower = 20_000
	c := cycler(t, b, dir)
	c.Exec = &Executor{Broker: b.writer, DryRun: true}
	res, err := c.Run(context.Background(), xlksata.PhaseEntry, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.Intents) != 1 {
		t.Fatalf("the plan should still say what it wanted: %+v", res.Plan)
	}
	if len(b.writer.placedEq) != 0 || b.writer.reviewEq != 1 {
		t.Fatalf("reviewed %d, placed %d", b.writer.reviewEq, len(b.writer.placedEq))
	}
	book, _ := Load(dir)
	if book.Pending != nil {
		t.Error("a dry run has nothing to wait on")
	}
}
