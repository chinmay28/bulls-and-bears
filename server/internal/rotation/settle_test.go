package rotation

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/robinhood"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

type orderBook struct {
	equity, option []robinhood.Order
	err            error
}

func (o *orderBook) EquityOrders(context.Context, string) ([]robinhood.Order, error) {
	return o.equity, o.err
}
func (o *orderBook) OptionOrders(context.Context, string) ([]robinhood.Order, error) {
	return o.option, o.err
}

func filled(id string, qty, price float64) robinhood.Order {
	return robinhood.Order{
		ID: id, State: "filled",
		Quantity: robinhood.Num(qty), Filled: robinhood.Num(qty),
		AveragePrice: robinhood.Num(price),
		CreatedAt:    robinhood.Time{Time: now},
	}
}

func eng(t *testing.T) *xlksata.Engine {
	t.Helper()
	e, err := xlksata.New(xlksata.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSettleNothingPending(t *testing.T) {
	b, note, err := Settle(context.Background(), &orderBook{}, eng(t), Book{}, "")
	if err != nil || note != "" || b.Pending != nil {
		t.Fatalf("got %+v %q %v", b, note, err)
	}
}

// The entry fill is the only place the lot's basis is ever set, so it is the
// one that has to be right.
func TestSettleEntryFillSetsTheBasis(t *testing.T) {
	o := &orderBook{equity: []robinhood.Order{filled("eq-1", 100, 187.42)}}
	in := Book{Pending: &Pending{OrderID: "eq-1", Kind: xlksata.BuyEquity, Symbol: "XLK"}}

	b, note, err := Settle(context.Background(), o, eng(t), in, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.State.Mode != xlksata.Held || b.State.EntryPrice != 187.42 {
		t.Fatalf("got %+v (%s)", b.State, note)
	}
	if b.Pending != nil {
		t.Error("a settled order should stop being pending")
	}
	if b.State.OptionPnL != 0 || b.State.Dividends != 0 || b.State.Costs != 0 {
		t.Errorf("a new lot inherits nothing: %+v", b.State)
	}
}

func TestSettleExitClearsTheLot(t *testing.T) {
	o := &orderBook{equity: []robinhood.Order{filled("eq-2", 100, 189.10)}}
	in := Book{
		State:   xlksata.State{Mode: xlksata.Recovery, EntryPrice: 187, OptionPnL: 90},
		Pending: &Pending{OrderID: "eq-2", Kind: xlksata.SellEquity, Symbol: "XLK"},
	}
	b, note, err := Settle(context.Background(), o, eng(t), in, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.State != (xlksata.State{}) {
		t.Fatalf("an exit leaves nothing behind: %+v", b.State)
	}
	// 100 x (189.10 - 187) + 90 = 300 on an 18,700 basis.
	if want := "+1.604%"; !strings.Contains(note, want) {
		t.Errorf("note %q should carry the combined return %s", note, want)
	}
}

// A sweep in or out of the parking leg is not part of the lot's accounting.
func TestSettleParkingLegLeavesTheLotAlone(t *testing.T) {
	for _, kind := range []xlksata.Kind{xlksata.BuyEquity, xlksata.SellEquity} {
		o := &orderBook{equity: []robinhood.Order{filled("s1", 189, 99.94)}}
		want := xlksata.State{Mode: xlksata.Held, EntryPrice: 187}
		in := Book{State: want, Pending: &Pending{OrderID: "s1", Kind: kind, Symbol: "SATA"}}
		b, _, err := Settle(context.Background(), o, eng(t), in, "")
		if err != nil {
			t.Fatal(err)
		}
		if b.State != want {
			t.Fatalf("%s SATA changed the lot: %+v", kind, b.State)
		}
	}
}

// Robinhood reports an option order's average price per contract; the
// strategy reasons in premium per share.
func TestSettleOptionFillsConvertPerContractToPerShare(t *testing.T) {
	e := eng(t)
	exp := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

	t.Run("written call", func(t *testing.T) {
		o := &orderBook{option: []robinhood.Order{filled("opt-1", 1, 90)}}
		in := Book{
			State: xlksata.State{Mode: xlksata.Recovery, EntryPrice: 187},
			Pending: &Pending{
				OrderID: "opt-1", Kind: xlksata.SellCallToOpen,
				Symbol: "XLK", OptionID: "c1", Strike: 190, Expiration: exp,
			},
		}
		b, note, err := Settle(context.Background(), o, e, in, "")
		if err != nil {
			t.Fatal(err)
		}
		if b.State.OptionPnL != 90 {
			t.Errorf("realised option cash %v, want 90 dollars", b.State.OptionPnL)
		}
		if b.State.ShortCall == nil {
			t.Fatal("the call should be recorded")
		}
		if c := b.State.ShortCall; c.Credit != 0.90 || c.Strike != 190 || !c.Expiration.Equal(exp) {
			t.Errorf("call: %+v (%s)", c, note)
		}
	})

	t.Run("bought back", func(t *testing.T) {
		o := &orderBook{option: []robinhood.Order{filled("opt-2", 1, 40)}}
		in := Book{
			State: xlksata.State{
				Mode: xlksata.Recovery, EntryPrice: 187, OptionPnL: 90,
				ShortCall: &xlksata.ShortCall{OptionID: "c1", Strike: 190, Credit: 0.90},
			},
			Pending: &Pending{OrderID: "opt-2", Kind: xlksata.BuyCallToClose, Symbol: "XLK", OptionID: "c1", Strike: 190},
		}
		b, _, err := Settle(context.Background(), o, e, in, "")
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(b.State.OptionPnL-50) > 1e-9 {
			t.Errorf("90 received less 40 paid is 50, got %v", b.State.OptionPnL)
		}
		if b.State.ShortCall != nil {
			t.Error("the call should be cleared")
		}
		if b.State.Mode != xlksata.Recovery {
			t.Error("buying the call back does not end the recovery")
		}
	})
}

func TestSettleLeavesAWorkingOrderPending(t *testing.T) {
	o := &orderBook{equity: []robinhood.Order{{ID: "eq-1", State: "confirmed"}}}
	in := Book{Pending: &Pending{OrderID: "eq-1", Kind: xlksata.BuyEquity, Symbol: "XLK"}}
	b, note, err := Settle(context.Background(), o, eng(t), in, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Pending == nil {
		t.Fatal("a working order stays pending")
	}
	if b.State != (xlksata.State{}) || !strings.Contains(note, "confirmed") {
		t.Fatalf("got %+v (%s)", b.State, note)
	}
}

func TestSettleAppliesNothingWhenTheOrderDidNotFill(t *testing.T) {
	for _, state := range []string{"cancelled", "rejected", "failed", "voided"} {
		t.Run(state, func(t *testing.T) {
			o := &orderBook{equity: []robinhood.Order{{ID: "eq-1", State: state}}}
			in := Book{Pending: &Pending{OrderID: "eq-1", Kind: xlksata.BuyEquity, Symbol: "XLK"}}
			b, note, err := Settle(context.Background(), o, eng(t), in, "")
			if err != nil {
				t.Fatal(err)
			}
			if b.Pending != nil {
				t.Error("a resolved order should stop being pending")
			}
			if b.State != (xlksata.State{}) {
				t.Fatalf("nothing should have been applied: %+v", b.State)
			}
			if !strings.Contains(note, state) {
				t.Errorf("note %q should say what happened", note)
			}
		})
	}
}

// An order the broker has never heard of is not treated as a fill at a
// guessed price: the pending marker is dropped, and if a position really did
// appear, the next cycle halts on it loudly.
func TestSettleUnknownOrderAppliesNothing(t *testing.T) {
	o := &orderBook{}
	in := Book{Pending: &Pending{OrderID: "ghost", Kind: xlksata.BuyEquity, Symbol: "XLK"}}
	b, note, err := Settle(context.Background(), o, eng(t), in, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Pending != nil || b.State != (xlksata.State{}) {
		t.Fatalf("got %+v", b)
	}
	if !strings.Contains(note, "cannot be found") {
		t.Errorf("note %q should say so", note)
	}
}

func TestSettlePropagatesAReadFailure(t *testing.T) {
	o := &orderBook{err: errors.New("broker down")}
	in := Book{Pending: &Pending{OrderID: "eq-1", Kind: xlksata.BuyEquity, Symbol: "XLK"}}
	if _, _, err := Settle(context.Background(), o, eng(t), in, ""); err == nil {
		t.Fatal("want an error rather than a cleared pending order")
	}
}

// An option order is looked for on the options side of the book, not the
// equities side.
func TestSettleLooksOnTheRightSideOfTheBook(t *testing.T) {
	o := &orderBook{
		equity: []robinhood.Order{filled("opt-1", 1, 999)}, // a decoy with the same id
		option: []robinhood.Order{filled("opt-1", 1, 90)},
	}
	in := Book{
		State:   xlksata.State{Mode: xlksata.Recovery, EntryPrice: 187},
		Pending: &Pending{OrderID: "opt-1", Kind: xlksata.SellCallToOpen, Symbol: "XLK", OptionID: "c1", Strike: 190},
	}
	b, _, err := Settle(context.Background(), o, eng(t), in, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.State.OptionPnL != 90 {
		t.Errorf("read the equity order by mistake: %v", b.State.OptionPnL)
	}
}

func TestPendingForCarriesTheContract(t *testing.T) {
	exp := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	in := xlksata.Intent{
		Kind: xlksata.SellCallToOpen, Symbol: "XLK", Qty: 1, Limit: 0.85,
		OptionID: "c1", Strike: 190, Expiration: exp,
	}
	p := PendingFor(in, "opt-1", now)
	if p.OrderID != "opt-1" || p.Kind != xlksata.SellCallToOpen || p.OptionID != "c1" ||
		p.Strike != 190 || !p.Expiration.Equal(exp) {
		t.Fatalf("got %+v", p)
	}
	if p.PlacedAt.IsZero() {
		t.Error("a pending order should record when it went out")
	}
}

// The pending order survives a restart: that is the whole point of writing
// it down rather than holding it in memory.
func TestPendingSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	want := &Pending{
		OrderID: "opt-1", Kind: xlksata.SellCallToOpen, Symbol: "XLK",
		OptionID: "c1", Strike: 190,
		Expiration: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		PlacedAt:   now,
	}
	in := Book{State: xlksata.State{Mode: xlksata.Recovery, EntryPrice: 187}, Pending: want}
	if err := Save(dir, in, now); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pending == nil || *got.Pending != *want {
		t.Fatalf("got %+v, want %+v", got.Pending, want)
	}
}
