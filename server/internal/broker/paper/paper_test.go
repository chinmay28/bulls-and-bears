package paper

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/broker"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata"
)

// feed is a MarketData that answers from a map, and can be changed mid-test.
type feed struct {
	mu     sync.Mutex
	quotes map[string]marketdata.Quote
}

func (f *feed) set(q marketdata.Quote) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quotes[q.Symbol] = q
}

func (f *feed) Quotes(_ context.Context, symbols []string) ([]marketdata.Quote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []marketdata.Quote
	for _, s := range symbols {
		if q, ok := f.quotes[s]; ok {
			out = append(out, q)
		}
	}
	return out, nil
}

var t0 = time.Date(2026, 9, 4, 19, 50, 0, 0, time.UTC)

func newBook(t *testing.T) (*Book, *feed, string) {
	t.Helper()
	f := &feed{quotes: map[string]marketdata.Quote{
		"GLD": {Symbol: "GLD", Bid: 230, Ask: 231, Last: 230.5, At: t0},
		"GDX": {Symbol: "GDX", Bid: 41, Ask: 42, Last: 41.5, At: t0},
	}}
	path := filepath.Join(t.TempDir(), "data", "bnb.db")
	b, err := Open(path, f, Options{StartingCash: 10_000, Costs: Costs{CommissionUSD: 1, SlippageBps: 10}, Clock: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b, f, path
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestMarketBuyFillsAtSlippedAskLessCommission(t *testing.T) {
	b, _, path := newBook(t)
	ctx := context.Background()
	id, err := b.Place(ctx, broker.Order{IntentID: "i1", Symbol: "GLD", Side: broker.Buy, Qty: 10})
	if err != nil {
		t.Fatal(err)
	}
	if id != "paper-1" {
		t.Errorf("id = %s", id)
	}
	// 231 × 1.001 = 231.231; cash = 10000 − 2312.31 − 1 = 7686.69
	cash, _ := b.BuyingPower(ctx)
	if !near(cash, 10_000-10*231.231-1) {
		t.Errorf("cash = %.4f", cash)
	}
	fills, _ := b.Fills(ctx, time.Time{})
	if len(fills) != 1 || !near(fills[0].Price, 231.231) || fills[0].Qty != 10 || fills[0].IntentID != "i1" {
		t.Errorf("fills = %+v", fills)
	}
	pos, _ := b.Positions(ctx)
	if len(pos) != 1 || pos[0].Qty != 10 || !near(pos[0].AvgCost, 231.231) {
		t.Errorf("positions = %+v", pos)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, err %v; want 0600", fi.Mode(), err)
	}
}

func TestMarketSellGoesShortAtSlippedBid(t *testing.T) {
	b, _, _ := newBook(t)
	ctx := context.Background()
	if _, err := b.Place(ctx, broker.Order{IntentID: "i1", Symbol: "GDX", Side: broker.Sell, Qty: 20}); err != nil {
		t.Fatal(err)
	}
	// 41 × 0.999 = 40.959; cash = 10000 + 819.18 − 1
	cash, _ := b.BuyingPower(ctx)
	if !near(cash, 10_000+20*40.959-1) {
		t.Errorf("cash = %.4f", cash)
	}
	pos, _ := b.Positions(ctx)
	if len(pos) != 1 || pos[0].Qty != -20 || !near(pos[0].AvgCost, 40.959) {
		t.Errorf("positions = %+v", pos)
	}
	fills, _ := b.Fills(ctx, time.Time{})
	if fills[0].Qty != -20 {
		t.Errorf("a sell fill should be negative: %+v", fills[0])
	}
}

func TestLimitOrders(t *testing.T) {
	b, _, _ := newBook(t)
	ctx := context.Background()
	// Marketable buy limit above the ask fills at the better of limit and slipped market.
	if _, err := b.Place(ctx, broker.Order{IntentID: "i1", Symbol: "GLD", Side: broker.Buy, Qty: 1, Limit: 231.1}); err != nil {
		t.Fatal(err)
	}
	fills, _ := b.Fills(ctx, time.Time{})
	if !near(fills[0].Price, 231.1) {
		t.Errorf("marketable limit filled at %v, want the limit 231.1 (better than slipped 231.231)", fills[0].Price)
	}
	// Non-marketable buy limit rests.
	id, err := b.Place(ctx, broker.Order{IntentID: "i2", Symbol: "GLD", Side: broker.Buy, Qty: 1, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := b.Snapshot(ctx)
	if len(snap.OpenOrders) != 1 || snap.OpenOrders[0].ID != id || snap.OpenOrders[0].Limit != 200 {
		t.Errorf("open orders = %+v", snap.OpenOrders)
	}
	if fills, _ := b.Fills(ctx, time.Time{}); len(fills) != 1 {
		t.Errorf("a resting order must not fill: %d fills", len(fills))
	}
	if err := b.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := b.Cancel(ctx, id); err == nil {
		t.Error("cancelling twice must fail")
	}
	if err := b.Cancel(ctx, "paper-1"); err == nil {
		t.Error("cancelling a filled order must fail")
	}
	if err := b.Cancel(ctx, "nope"); err == nil {
		t.Error("cancelling an unknown order must fail")
	}
	snap, _ = b.Snapshot(ctx)
	if len(snap.OpenOrders) != 0 {
		t.Errorf("open orders after cancel = %+v", snap.OpenOrders)
	}
}

func TestRejections(t *testing.T) {
	b, f, _ := newBook(t)
	ctx := context.Background()
	cases := []struct {
		name  string
		order broker.Order
	}{
		{"insufficient buying power", broker.Order{IntentID: "x", Symbol: "GLD", Side: broker.Buy, Qty: 100}},
		{"no quote", broker.Order{IntentID: "x", Symbol: "SPY", Side: broker.Buy, Qty: 1}},
		{"zero qty", broker.Order{IntentID: "x", Symbol: "GLD", Side: broker.Buy, Qty: 0}},
		{"no side", broker.Order{IntentID: "x", Symbol: "GLD", Qty: 1}},
		{"quote without a book", broker.Order{IntentID: "x", Symbol: "TLT", Side: broker.Buy, Qty: 1}},
	}
	f.set(marketdata.Quote{Symbol: "TLT", Last: 90})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before, _ := b.BuyingPower(ctx)
			_, err := b.Place(ctx, c.order)
			if !errors.Is(err, ErrRejected) {
				t.Fatalf("err = %v, want ErrRejected", err)
			}
			after, _ := b.BuyingPower(ctx)
			if before != after {
				t.Errorf("cash moved on a rejection: %v -> %v", before, after)
			}
		})
	}
	if pos, _ := b.Positions(ctx); len(pos) != 0 {
		t.Errorf("positions after rejections = %+v", pos)
	}
}

func TestAverageCostAndRealizedPnL(t *testing.T) {
	b, f, _ := newBook(t)
	ctx := context.Background()
	b2 := func(qty float64, side broker.Side) {
		t.Helper()
		if _, err := b.Place(ctx, broker.Order{IntentID: "i", Symbol: "GLD", Side: side, Qty: qty}); err != nil {
			t.Fatal(err)
		}
	}
	b2(10, broker.Buy) // 231.231
	f.set(marketdata.Quote{Symbol: "GLD", Bid: 240, Ask: 241, Last: 240.5, At: t0})
	b2(10, broker.Buy) // 241.241; avg = 236.236
	pos, _ := b.Positions(ctx)
	if !near(pos[0].AvgCost, (231.231+241.241)/2) || pos[0].Qty != 20 {
		t.Fatalf("after two buys: %+v", pos[0])
	}
	cashBefore, _ := b.BuyingPower(ctx)
	b2(5, broker.Sell) // at 240 × 0.999 = 239.76; avg unchanged
	pos, _ = b.Positions(ctx)
	if !near(pos[0].AvgCost, (231.231+241.241)/2) || pos[0].Qty != 15 {
		t.Errorf("after a partial sell: %+v", pos[0])
	}
	cashAfter, _ := b.BuyingPower(ctx)
	if !near(cashAfter-cashBefore, 5*239.76-1) {
		t.Errorf("cash from the sale = %v", cashAfter-cashBefore)
	}
	b2(25, broker.Sell) // flips to −10 at 239.76: fresh average
	pos, _ = b.Positions(ctx)
	if pos[0].Qty != -10 || !near(pos[0].AvgCost, 239.76) {
		t.Errorf("after the flip: %+v", pos[0])
	}
	b2(10, broker.Buy) // flat: no row
	if pos, _ := b.Positions(ctx); len(pos) != 0 {
		t.Errorf("after closing: %+v", pos)
	}
}

func TestApplyTradeTable(t *testing.T) {
	cases := []struct {
		qty, avg, delta, price float64
		wantQty, wantAvg       float64
	}{
		{0, 0, 10, 100, 10, 100},
		{10, 100, 10, 120, 20, 110},
		{10, 100, -4, 130, 6, 100},
		{10, 100, -10, 130, 0, 0},
		{10, 100, -15, 130, -5, 130},
		{-5, 130, -5, 110, -10, 120},
		{-10, 120, 4, 100, -6, 120},
	}
	for _, c := range cases {
		q, a := applyTrade(c.qty, c.avg, c.delta, c.price)
		if !near(q, c.wantQty) || !near(a, c.wantAvg) {
			t.Errorf("applyTrade(%v,%v,%v,%v) = %v,%v; want %v,%v", c.qty, c.avg, c.delta, c.price, q, a, c.wantQty, c.wantAvg)
		}
	}
}

func TestEquityMarksAndFailsClosed(t *testing.T) {
	b, f, _ := newBook(t)
	ctx := context.Background()
	if _, err := b.Place(ctx, broker.Order{IntentID: "i", Symbol: "GLD", Side: broker.Buy, Qty: 10}); err != nil {
		t.Fatal(err)
	}
	cash, _ := b.BuyingPower(ctx)
	eq, err := b.Equity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !near(eq, cash+10*230.5) {
		t.Errorf("equity at last = %v", eq)
	}
	f.set(marketdata.Quote{Symbol: "GLD", Bid: 230, Ask: 232, At: t0}) // no last: mid
	eq, _ = b.Equity(ctx)
	if !near(eq, cash+10*231) {
		t.Errorf("equity at mid = %v", eq)
	}
	delete(f.quotes, "GLD")
	if _, err := b.Equity(ctx); err == nil {
		t.Error("equity with a held symbol unquoted must be an error")
	}
	snap, err := b.Snapshot(ctx)
	if err == nil {
		t.Errorf("snapshot should fail the same way, got %+v", snap)
	}
}

func TestRestartKeepsTheBook(t *testing.T) {
	f := &feed{quotes: map[string]marketdata.Quote{"GLD": {Symbol: "GLD", Bid: 230, Ask: 231, Last: 230.5, At: t0}}}
	path := filepath.Join(t.TempDir(), "bnb.db")
	ctx := context.Background()
	b, err := Open(path, f, Options{StartingCash: 10_000, Clock: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Place(ctx, broker.Order{IntentID: "i", Symbol: "GLD", Side: broker.Buy, Qty: 3}); err != nil {
		t.Fatal(err)
	}
	b.Equity(ctx)
	cash, _ := b.BuyingPower(ctx)
	b.Close()

	b, err = Open(path, f, Options{StartingCash: 99, Clock: func() time.Time { return t0.Add(24 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	cash2, _ := b.BuyingPower(ctx)
	pos, _ := b.Positions(ctx)
	fills, _ := b.Fills(ctx, time.Time{})
	if cash2 != cash || len(pos) != 1 || pos[0].Qty != 3 || len(fills) != 1 {
		t.Errorf("after reopen: cash %v (was %v), positions %+v, fills %d", cash2, cash, pos, len(fills))
	}
	snap, _ := b.Snapshot(ctx)
	if snap.StartingCash != 10_000 || !snap.OpenedAt.Equal(t0) {
		t.Errorf("snapshot = %+v, want the original starting cash and opening time", snap)
	}
	id, _ := b.Place(ctx, broker.Order{IntentID: "i", Symbol: "GLD", Side: broker.Sell, Qty: 1})
	if id != "paper-2" {
		t.Errorf("order ids must continue across restarts, got %s", id)
	}
}

func TestEquityHistoryOneRowPerDay(t *testing.T) {
	f := &feed{quotes: map[string]marketdata.Quote{"GLD": {Symbol: "GLD", Bid: 230, Ask: 231, Last: 230.5, At: t0}}}
	clock := t0
	b, err := Open(filepath.Join(t.TempDir(), "bnb.db"), f, Options{StartingCash: 10_000, Clock: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	b.Equity(ctx)
	b.Place(ctx, broker.Order{IntentID: "i", Symbol: "GLD", Side: broker.Buy, Qty: 10})
	f.set(marketdata.Quote{Symbol: "GLD", Bid: 240, Ask: 241, Last: 240.5, At: t0})
	b.Equity(ctx)
	hist, _ := b.EquityHistory(ctx)
	if len(hist) != 1 || hist[0].Open != 10_000 || hist[0].Equity <= 10_000 {
		t.Fatalf("history = %+v", hist)
	}
	sod, _ := b.StartOfDayEquity(ctx, t0)
	if sod != 10_000 {
		t.Errorf("start of day = %v", sod)
	}
	hw, _ := b.HighWaterEquity(ctx)
	if hw != hist[0].Equity {
		t.Errorf("high water = %v, want %v", hw, hist[0].Equity)
	}
	clock = t0.Add(24 * time.Hour)
	b.Equity(ctx)
	hist, _ = b.EquityHistory(ctx)
	if len(hist) != 2 {
		t.Errorf("two days, %d rows", len(hist))
	}
	sod, _ = b.StartOfDayEquity(ctx, clock.Add(24*time.Hour))
	if sod != hist[1].Equity {
		t.Errorf("start of a day with no mark = %v, want the last mark %v", sod, hist[1].Equity)
	}
}

func TestConcurrentPlacesAreSerialized(t *testing.T) {
	b, _, _ := newBook(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Place(ctx, broker.Order{IntentID: "i", Symbol: "GLD", Side: broker.Buy, Qty: 5}) // 1,157 each; 8 would need 9,258
		}()
	}
	wg.Wait()
	fills, _ := b.Fills(ctx, time.Time{})
	if len(fills) != 8 {
		t.Errorf("fills = %d, want all 8 (9,258 < 10,000)", len(fills))
	}
	cash, _ := b.BuyingPower(ctx)
	if cash < 0 {
		t.Errorf("cash went negative: %v", cash)
	}
}
