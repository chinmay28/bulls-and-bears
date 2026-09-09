package rotation

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/robinhood"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

var now = time.Date(2026, 9, 9, 19, 12, 0, 0, time.UTC)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// A directory that has never run is a flat book.
	b, err := Load(dir)
	if err != nil || b.State != (xlksata.State{}) || b.Pending != nil {
		t.Fatalf("got %+v, %v", b, err)
	}

	want := xlksata.State{
		Mode:       xlksata.Recovery,
		EntryPrice: 187.00,
		EntryDate:  now.Add(-48 * time.Hour),
		OptionPnL:  90,
		Dividends:  25,
		Costs:      1.30,
		ShortCall: &xlksata.ShortCall{
			OptionID:   "c1",
			Expiration: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
			Strike:     190,
			Credit:     0.90,
		},
	}
	if err := Save(dir, Book{State: want}, now); err != nil {
		t.Fatal(err)
	}
	b, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := b.State
	if got.Mode != want.Mode || got.EntryPrice != want.EntryPrice ||
		got.OptionPnL != want.OptionPnL || got.Dividends != want.Dividends || got.Costs != want.Costs {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got.ShortCall == nil || *got.ShortCall != *want.ShortCall {
		t.Fatalf("short call: %+v", got.ShortCall)
	}
	if !got.EntryDate.Equal(want.EntryDate) {
		t.Errorf("entry date %v, want %v", got.EntryDate, want.EntryDate)
	}

	// Flat again clears the call.
	if err := Save(dir, Book{}, now); err != nil {
		t.Fatal(err)
	}
	if b, _ = Load(dir); b.State.ShortCall != nil || b.State.Mode != xlksata.Flat {
		t.Fatalf("got %+v", b.State)
	}
}

func TestLedgerFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Book{State: xlksata.State{Mode: xlksata.Held, EntryPrice: 1}}, now); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(LedgerFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %v, want 0600", perm)
	}
}

// Forgetting an open lot would open a second one on top of it, so a line
// that parses and does not make sense is an error, never a flat book.
func TestUnreadableStateIsNeverAFlatBook(t *testing.T) {
	cases := map[string]string{
		"wrong version":  `{"version":99,"mode":"flat"}`,
		"unknown mode":   `{"version":1,"mode":"sideways"}`,
		"open, no entry": `{"version":1,"mode":"recovery","state":{"entry_price":0}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeLedger(t, dir, body)
			if got, err := Load(dir); err == nil {
				t.Fatalf("want an error, got %+v", got)
			}
		})
	}
}

// The crash this format is shaped to survive: the process died partway
// through appending. The torn line is skipped and the book is the one before
// it — one cycle stale, but whole.
func TestTornFinalLineFallsBackToTheOneBefore(t *testing.T) {
	dir := t.TempDir()
	want := xlksata.State{Mode: xlksata.Recovery, EntryPrice: 187, EntryDate: now, OptionPnL: 90}
	if err := Save(dir, Book{State: want}, now); err != nil {
		t.Fatal(err)
	}
	// A short write: the line the process never finished.
	f, err := os.OpenFile(LedgerFile(dir), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"ts":"2026-09-09T19:12:00Z","versi`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("a torn final line should not stop a start-up: %v", err)
	}
	if got.State.Mode != want.Mode || got.State.EntryPrice != want.EntryPrice || got.State.OptionPnL != want.OptionPnL {
		t.Fatalf("got %+v, want the previous book %+v", got.State, want)
	}
}

// A torn line earlier in the file is simply old: the last whole line wins,
// because that is the book.
func TestTheLastWholeLineWins(t *testing.T) {
	dir := t.TempDir()
	writeLedger(t, dir, "{not json at all",
		`{"version":1,"mode":"held","state":{"entry_price":180},"marks":{}}`,
		`{"version":1,"mode":"recovery","state":{"entry_price":187},"marks":{}}`)
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Mode != xlksata.Recovery || got.State.EntryPrice != 187 {
		t.Fatalf("got %+v", got.State)
	}
}

// Nothing is ever rewritten, so every book the directory has held is still
// there, in order, with the time it was written.
func TestLedgerKeepsTheHistory(t *testing.T) {
	dir := t.TempDir()
	steps := []xlksata.State{
		{},
		{Mode: xlksata.Held, EntryPrice: 187, EntryDate: now},
		{Mode: xlksata.Recovery, EntryPrice: 187, EntryDate: now},
		{Mode: xlksata.Recovery, EntryPrice: 187, EntryDate: now, OptionPnL: 85},
		{},
	}
	for i, st := range steps {
		if err := Save(dir, Book{State: st}, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	hist, err := History(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != len(steps) {
		t.Fatalf("got %d books, want %d", len(hist), len(steps))
	}
	for i, b := range hist {
		if b.State.Mode != steps[i].Mode || b.State.OptionPnL != steps[i].OptionPnL {
			t.Errorf("book %d: got %+v, want %+v", i, b.State, steps[i])
		}
		if b.At.IsZero() {
			t.Errorf("book %d has no timestamp", i)
		}
		if i > 0 && b.At.Before(hist[i-1].At) {
			t.Errorf("book %d is timestamped before book %d", i, i-1)
		}
	}
	// And the current book is the last of them.
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != (xlksata.State{}) {
		t.Fatalf("got %+v, want the last book", got.State)
	}
}

// Two rotations over one directory would each read a flat book, each decide
// to buy, and each buy. The lock is what stops that.
func TestLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); err == nil {
		t.Fatal("a second holder got the lock")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	// Releasing twice, and releasing a nil lock, are both fine.
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatal(err)
	}
}

func writeLedger(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(LedgerFile(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPhaseAt(t *testing.T) {
	ti, err := DefaultTimes()
	if err != nil {
		t.Fatal(err)
	}
	et := func(h, m int) time.Time {
		return time.Date(2026, 9, 9, h, m, 0, 0, ti.Loc)
	}
	cases := []struct {
		name  string
		at    time.Time
		want  xlksata.Phase
		armed bool
	}{
		{"before the open", et(8, 0), 0, false},
		{"after the open, before entry", et(9, 45), xlksata.PhaseManage, true},
		{"entry, 07:12 PT", et(10, 12), xlksata.PhaseEntry, true},
		{"a late entry still counts", et(10, 16), xlksata.PhaseEntry, true},
		{"past the entry window", et(10, 20), xlksata.PhaseManage, true},
		{"review, 12:07 PT", et(15, 7), xlksata.PhaseReview, true},
		{"a late review still counts", et(15, 11), xlksata.PhaseReview, true},
		{"past the review window", et(15, 30), xlksata.PhaseManage, true},
		{"after the close", et(16, 30), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, armed := ti.Phase(c.at)
			if armed != c.armed || (armed && got != c.want) {
				t.Fatalf("got %v/%v, want %v/%v", got, armed, c.want, c.armed)
			}
		})
	}

	// The rules are stated in Pacific; both US zones shift together, so the
	// same instant is the entry in either reading, in summer and in winter.
	pt, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no Pacific timezone on this machine")
	}
	for _, d := range []time.Time{
		time.Date(2026, 9, 9, 7, 12, 0, 0, pt),  // daylight time
		time.Date(2026, 12, 9, 7, 12, 0, 0, pt), // standard time
	} {
		if got, armed := ti.Phase(d); !armed || got != xlksata.PhaseEntry {
			t.Errorf("%s: got %v/%v, want entry", d.Format(time.RFC3339), got, armed)
		}
	}
}

// reader answers a snapshot's reads from fields set per test.
type reader struct {
	account    robinhood.Account
	accountErr error
	portfolio  robinhood.Portfolio
	equity     []robinhood.EquityPosition
	options    []robinhood.OptionPosition
	quotes     map[string]robinhood.Quote
	working    int
	chains     []robinhood.Chain
	calls      []robinhood.Instrument
	optQuotes  map[string]robinhood.OptionQuote
	instrument map[string]robinhood.Instrument

	quoteCalls int
}

func (r *reader) CheckAccount(context.Context, bool) (robinhood.Account, error) {
	return r.account, r.accountErr
}
func (r *reader) Portfolio(context.Context) (robinhood.Portfolio, error) { return r.portfolio, nil }
func (r *reader) EquityPositions(context.Context) ([]robinhood.EquityPosition, error) {
	return r.equity, nil
}
func (r *reader) OptionPositions(context.Context) ([]robinhood.OptionPosition, error) {
	return r.options, nil
}
func (r *reader) EquityQuotes(_ context.Context, _ ...string) (map[string]robinhood.Quote, error) {
	return r.quotes, nil
}
func (r *reader) WorkingOrders(context.Context, string, ...string) (int, error) {
	return r.working, nil
}
func (r *reader) Chains(context.Context, string) ([]robinhood.Chain, error) { return r.chains, nil }
func (r *reader) Calls(context.Context, string, []string) ([]robinhood.Instrument, error) {
	return r.calls, nil
}
func (r *reader) OptionQuotes(_ context.Context, ids ...string) (map[string]robinhood.OptionQuote, error) {
	r.quoteCalls++
	out := map[string]robinhood.OptionQuote{}
	for _, id := range ids {
		if q, ok := r.optQuotes[id]; ok {
			out[id] = q
		}
	}
	return out, nil
}
func (r *reader) StrikeOf(_ context.Context, id string) (robinhood.Instrument, error) {
	i, ok := r.instrument[id]
	if !ok {
		return robinhood.Instrument{}, errors.New("no such contract")
	}
	return i, nil
}

func rhQuote(bid, ask float64) robinhood.Quote {
	at := robinhood.Time{Time: now.Add(-time.Minute)}
	return robinhood.Quote{
		BidPrice: robinhood.Num(bid), AskPrice: robinhood.Num(ask),
		LastTrade: robinhood.Num((bid + ask) / 2),
		BidTime:   at, AskTime: at, State: "active", HasTraded: true,
	}
}

func baseReader() *reader {
	return &reader{
		account:    robinhood.Account{Number: "682795521", AgenticAllowed: true, OptionLevel: "option_level_3", State: "active"},
		quotes:     map[string]robinhood.Quote{"XLK": rhQuote(188.00, 188.10), "SATA": rhQuote(99.93, 100.00)},
		instrument: map[string]robinhood.Instrument{},
	}
}

func TestSnapshotReadsTheWholeBook(t *testing.T) {
	r := baseReader()
	r.portfolio.BuyingPower.BuyingPower = 20_000
	r.equity = []robinhood.EquityPosition{
		{Symbol: "XLK", Quantity: 100, AverageBuyPrice: 187},
		{Symbol: "SATA", Quantity: 42},
	}
	r.working = 2

	st := xlksata.State{Mode: xlksata.Held, EntryPrice: 187, EntryDate: now}
	s, err := Snapshot(context.Background(), r, xlksata.Defaults(), st, xlksata.PhaseReview, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Cash != 20_000 || s.XLKShares != 100 || s.SATAShares != 42 || s.WorkingOrders != 2 {
		t.Fatalf("got %+v", s)
	}
	if s.XLK.Bid != 188.00 || s.SATA.Ask != 100.00 {
		t.Fatalf("quotes: %+v %+v", s.XLK, s.SATA)
	}
	if s.XLK.At.IsZero() {
		t.Error("quote should carry its age")
	}
	// A held lot needs no chain walk.
	if r.quoteCalls != 0 {
		t.Errorf("asked for %d option quotes outside recovery", r.quoteCalls)
	}
}

// The account assertion runs before anything else: the wrong account is not
// a book worth reading.
func TestSnapshotChecksTheAccountFirst(t *testing.T) {
	r := baseReader()
	r.accountErr = errors.New("not the agentic account")
	if _, err := Snapshot(context.Background(), r, xlksata.Defaults(), xlksata.State{}, xlksata.PhaseEntry, now); err == nil {
		t.Fatal("want an error")
	}
}

func TestSnapshotReadsAnOpenShortCall(t *testing.T) {
	r := baseReader()
	r.options = []robinhood.OptionPosition{
		{OptionID: "c1", ChainSymbol: "XLK", Type: "short", Quantity: 1, AveragePrice: 90, Multiplier: 100},
		{OptionID: "long", ChainSymbol: "XLK", Type: "long", Quantity: 1},
	}
	r.instrument["c1"] = robinhood.Instrument{
		ID: "c1", Strike: 190,
		Expiration: robinhood.Time{Time: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)},
	}
	r.optQuotes = map[string]robinhood.OptionQuote{
		"c1": {InstrumentID: "c1", BidPrice: 0.60, AskPrice: 0.70, MarkPrice: 0.65,
			UpdatedAt: robinhood.Time{Time: now.Add(-2 * time.Minute)}},
	}
	st := xlksata.State{
		Mode: xlksata.Recovery, EntryPrice: 187, EntryDate: now,
		OptionPnL: 90,
		ShortCall: &xlksata.ShortCall{OptionID: "c1", Strike: 190, Credit: 0.90},
	}
	s, err := Snapshot(context.Background(), r, xlksata.Defaults(), st, xlksata.PhaseManage, now)
	if err != nil {
		t.Fatal(err)
	}
	// Only the short leg is an obligation.
	if len(s.ShortCalls) != 1 || s.ShortCalls[0].OptionID != "c1" {
		t.Fatalf("short calls: %+v", s.ShortCalls)
	}
	if s.ShortCalls[0].Strike != 190 || s.ShortCalls[0].Credit != 0.90 {
		t.Fatalf("the credit should be per share: %+v", s.ShortCalls[0])
	}
	if s.ShortCallQuote.Ask != 0.70 {
		t.Fatalf("buy-back quote: %+v", s.ShortCallQuote)
	}
	// With a call already open there is nothing to choose between.
	if len(s.Calls) != 0 {
		t.Errorf("should not walk the chain with a call open: %d candidates", len(s.Calls))
	}
}

func TestSnapshotBuildsCandidatesInRecovery(t *testing.T) {
	r := baseReader()
	r.chains = []robinhood.Chain{{
		ID: "chain", Symbol: "XLK", CanOpenPosition: true,
		Expirations: []string{"2026-09-11", "2026-09-18"},
	}}
	exp := robinhood.Time{Time: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)}
	r.calls = []robinhood.Instrument{
		{ID: "in", Strike: 190, Expiration: exp, State: "active", Tradability: "tradable"},
		{ID: "below", Strike: 185, Expiration: exp, State: "active", Tradability: "tradable"},
		{ID: "far", Strike: 260, Expiration: exp, State: "active", Tradability: "tradable"},
		{ID: "halted", Strike: 191, Expiration: exp, State: "inactive", Tradability: "untradable"},
		{ID: "unquoted", Strike: 192, Expiration: exp, State: "active", Tradability: "tradable"},
	}
	r.optQuotes = map[string]robinhood.OptionQuote{
		"in": {InstrumentID: "in", BidPrice: 0.80, AskPrice: 0.90, MarkPrice: 0.85, Delta: 0.26,
			UpdatedAt: robinhood.Time{Time: now}},
	}
	st := xlksata.State{Mode: xlksata.Recovery, EntryPrice: 187, EntryDate: now}

	s, err := Snapshot(context.Background(), r, xlksata.Defaults(), st, xlksata.PhaseManage, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Calls) != 1 || s.Calls[0].OptionID != "in" {
		t.Fatalf("candidates: %+v", s.Calls)
	}
	c := s.Calls[0]
	if c.Delta != 0.26 || c.Strike != 190 || c.Quote.Bid != 0.80 || !c.Tradable {
		t.Fatalf("candidate: %+v", c)
	}
}

// writer records what the executor sent.
type writer struct {
	reviewEq, reviewOpt int
	placedEq            []robinhood.EquityOrderRequest
	placedOpt           []robinhood.OptionOrderRequest
	reviewErr, placeErr error
}

func (w *writer) ReviewEquity(context.Context, robinhood.EquityOrderRequest) (string, error) {
	w.reviewEq++
	return "no alerts", w.reviewErr
}
func (w *writer) ReviewOption(context.Context, robinhood.OptionOrderRequest) (string, error) {
	w.reviewOpt++
	return "collateral: 100 XLK", w.reviewErr
}
func (w *writer) PlaceEquity(_ context.Context, r robinhood.EquityOrderRequest) (robinhood.Order, error) {
	if w.placeErr != nil {
		return robinhood.Order{}, w.placeErr
	}
	w.placedEq = append(w.placedEq, r)
	return robinhood.Order{ID: "eq-1", State: "queued"}, nil
}
func (w *writer) PlaceOption(_ context.Context, r robinhood.OptionOrderRequest) (robinhood.Order, error) {
	if w.placeErr != nil {
		return robinhood.Order{}, w.placeErr
	}
	w.placedOpt = append(w.placedOpt, r)
	return robinhood.Order{ID: "opt-1", State: "queued"}, nil
}

func TestExecute(t *testing.T) {
	ctx := context.Background()

	t.Run("nothing to do is not an error", func(t *testing.T) {
		w := &writer{}
		got, err := (&Executor{Broker: w}).Execute(ctx, xlksata.Plan{})
		if err != nil || got != nil {
			t.Fatalf("got %+v, %v", got, err)
		}
		if w.reviewEq+w.reviewOpt != 0 {
			t.Error("an empty plan should not reach the broker")
		}
	})

	t.Run("an equity intent is reviewed then placed", func(t *testing.T) {
		w := &writer{}
		e := &Executor{Broker: w, RefID: func(xlksata.Intent) string { return "ref-1" }}
		got, err := e.Execute(ctx, xlksata.Plan{Intents: []xlksata.Intent{
			{Kind: xlksata.BuyEquity, Symbol: "XLK", Qty: 100},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if w.reviewEq != 1 || len(w.placedEq) != 1 {
			t.Fatalf("reviewed %d, placed %d", w.reviewEq, len(w.placedEq))
		}
		r := w.placedEq[0]
		if r.Symbol != "XLK" || r.Side != robinhood.Buy || r.Qty != 100 || r.Limit != 0 || r.RefID != "ref-1" {
			t.Fatalf("sent %+v", r)
		}
		if !got[0].Placed || got[0].OrderID != "eq-1" || got[0].Review == "" {
			t.Fatalf("placement: %+v", got[0])
		}
	})

	t.Run("a written call goes out as a limit sell to open", func(t *testing.T) {
		w := &writer{}
		e := &Executor{Broker: w}
		_, err := e.Execute(ctx, xlksata.Plan{Intents: []xlksata.Intent{
			{Kind: xlksata.SellCallToOpen, Symbol: "XLK", Qty: 1, Limit: 0.85, OptionID: "c1"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if w.reviewOpt != 1 || len(w.placedOpt) != 1 {
			t.Fatalf("reviewed %d, placed %d", w.reviewOpt, len(w.placedOpt))
		}
		r := w.placedOpt[0]
		if r.Side != robinhood.Sell || !r.Open || r.Limit != 0.85 || r.Contracts != 1 || r.OptionID != "c1" {
			t.Fatalf("sent %+v", r)
		}
	})

	t.Run("a buy-back goes out as a limit buy to close", func(t *testing.T) {
		w := &writer{}
		_, err := (&Executor{Broker: w}).Execute(ctx, xlksata.Plan{Intents: []xlksata.Intent{
			{Kind: xlksata.BuyCallToClose, Symbol: "XLK", Qty: 1, Limit: 1.40, OptionID: "c1"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		r := w.placedOpt[0]
		if r.Side != robinhood.Buy || r.Open || r.Limit != 1.40 {
			t.Fatalf("sent %+v", r)
		}
	})

	t.Run("only the first intent is sent", func(t *testing.T) {
		w := &writer{}
		_, err := (&Executor{Broker: w}).Execute(ctx, xlksata.Plan{Intents: []xlksata.Intent{
			{Kind: xlksata.BuyCallToClose, Symbol: "XLK", Qty: 1, Limit: 1.40, OptionID: "c1"},
			{Kind: xlksata.SellEquity, Symbol: "XLK", Qty: 100},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if len(w.placedEq) != 0 {
			t.Fatal("the shares must not go before the call is bought back")
		}
	})

	t.Run("dry run reviews and places nothing", func(t *testing.T) {
		w := &writer{}
		got, err := (&Executor{Broker: w, DryRun: true}).Execute(ctx, xlksata.Plan{Intents: []xlksata.Intent{
			{Kind: xlksata.BuyEquity, Symbol: "XLK", Qty: 100},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if w.reviewEq != 1 || len(w.placedEq) != 0 {
			t.Fatalf("reviewed %d, placed %d", w.reviewEq, len(w.placedEq))
		}
		if got[0].Placed || got[0].Review == "" {
			t.Fatalf("placement: %+v", got[0])
		}
	})

	t.Run("a failed review does not place blind", func(t *testing.T) {
		w := &writer{reviewErr: errors.New("buying power")}
		got, err := (&Executor{Broker: w}).Execute(ctx, xlksata.Plan{Intents: []xlksata.Intent{
			{Kind: xlksata.BuyEquity, Symbol: "XLK", Qty: 100},
		}})
		if err == nil {
			t.Fatal("want an error")
		}
		if len(w.placedEq) != 0 {
			t.Fatal("nothing should have been placed")
		}
		if got[0].Err == nil {
			t.Error("the placement should carry the failure")
		}
	})

	t.Run("an intent it cannot send is refused, not guessed", func(t *testing.T) {
		w := &writer{}
		if _, err := (&Executor{Broker: w}).Execute(ctx, xlksata.Plan{
			Intents: []xlksata.Intent{{Kind: xlksata.Kind(99), Symbol: "XLK"}},
		}); err == nil {
			t.Fatal("want an error")
		}
	})
}
