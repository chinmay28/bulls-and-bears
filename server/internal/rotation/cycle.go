package rotation

import (
	"context"
	"fmt"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/journal"
	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// Broker is everything one cycle needs of the account: the reads that build
// a snapshot, the order history that settles the last cycle's fill, the
// portfolio the gate marks equity from, and the writes that place an order.
type Broker interface {
	Reader
	Orders
	Writer
}

// Result is what one cycle did, in the order it did it. Every field is
// filled even when nothing was placed, because a cycle that decided to do
// nothing still has to be able to say what it was looking at.
type Result struct {
	Phase xlksata.Phase
	// Settled is what the previous cycle's order turned out to be, empty
	// when there was none.
	Settled   string
	Before    xlksata.State
	After     xlksata.State
	Plan      xlksata.Plan
	Decisions []risk.Decision
	Placed    []Placement
	Marks     Marks
}

// Cycler runs one turn of the strategy. Everything it needs is a field, so
// the wiring is visible in one place and a test can leave out the parts it
// is not exercising.
type Cycler struct {
	Broker Broker
	Engine *xlksata.Engine
	Exec   *Executor
	// Gate applies the §6 rules. Nil skips them, which is only for tests:
	// bnb rotate always builds one.
	Gate *Gate
	// DataDir holds the book; JournalDir holds the run journals. An empty
	// JournalDir writes no journal.
	DataDir, JournalDir string
	// Confirmed is the run's -yes.
	Confirmed bool
	// Live says orders are really placed, which is what turns the
	// confirmation threshold on.
	Live bool
	// IntentID mints the id that ties a risk decision, a journal line and
	// the broker's idempotency key together. Nil uses a time-based id.
	IntentID func() string
}

func (c *Cycler) mode() string {
	if c.Live {
		return "live"
	}
	return "dry-run"
}

// Run is one turn: settle what was outstanding, read the book, decide, gate,
// record, place.
//
// The order matters and is the whole design.
//
//   - Settling first means the strategy is handed a state that already
//     accounts for anything that filled since it last ran. It is the only
//     step that can set an entry price, because the entry price is a fill.
//   - The risk decision is journalled, and synced, before the order goes
//     out. That is docs/PLAN.md §4.5 read literally: every order_submitted
//     must follow an allowed risk_decision for the same intent.
//   - The state is written before the order goes out, never after. A crash
//     between the two leaves a book that says an order may be outstanding,
//     which the next cycle checks; the reverse would lose an order that had
//     already reached the exchange.
//   - Exactly one order goes out per cycle, which is the Executor's rule and
//     the reason the sequences in the strategy are safe to interrupt.
func (c *Cycler) Run(ctx context.Context, phase xlksata.Phase, now time.Time) (Result, error) {
	res := Result{Phase: phase}

	runID := now.UTC().Format("2006-01-02")
	if c.Gate != nil {
		runID = tradingDay(now)
	}
	jw, err := c.openJournal(runID)
	if err != nil {
		return res, err
	}
	if jw != nil {
		defer jw.Close()
	}

	book, err := Load(c.DataDir)
	if err != nil {
		return res, c.fail(jw, "", err)
	}
	res.Before = book.State

	since := now.UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02")
	book, res.Settled, err = Settle(ctx, c.Broker, c.Engine, book, since)
	if err != nil {
		return res, c.fail(jw, "", fmt.Errorf("settling the last order: %w", err))
	}
	if res.Settled != "" {
		write(jw, journal.New(journal.KindFill, "", map[string]any{"settled": res.Settled}))
		if err := Save(c.DataDir, book, now); err != nil {
			return res, c.fail(jw, "", err)
		}
	}

	// Equity marks feed the gate and nothing else; the strategy never sees
	// them, because none of its rules are about the account's size.
	port, err := c.Broker.Portfolio(ctx)
	if err != nil {
		return res, c.fail(jw, "", err)
	}
	book.Marks = book.Marks.Roll(tradingDay(now), port.TotalValue.Float())
	res.Marks = book.Marks

	snap, err := Snapshot(ctx, c.Broker, c.Engine.Config(), book.State, phase, now)
	if err != nil {
		return res, c.fail(jw, "", err)
	}
	// A pending order the broker still shows as working is a working order,
	// whichever way the count came back.
	if book.Pending != nil && snap.WorkingOrders == 0 {
		snap.WorkingOrders = 1
	}
	write(jw, journal.New(journal.KindQuote, "", map[string]any{
		xlksata.Risk: snap.XLK, xlksata.Park: snap.SATA,
		"cash": snap.Cash, "working_orders": snap.WorkingOrders,
	}))

	plan, err := c.Engine.Decide(snap)
	if err != nil {
		return res, c.fail(jw, "", err)
	}
	res.Plan = plan
	write(jw, journal.New(journal.KindTargets, "", map[string]any{
		"phase": phase.String(), "mode": plan.Next.Mode.String(),
		"reason": plan.Reason, "intents": len(plan.Intents),
	}))

	// The plan's Next carries what the book itself decided — an assignment,
	// an expiry, the move into recovery — and is recorded whether or not an
	// order follows.
	book.State = plan.Next
	res.After = plan.Next
	if err := Save(c.DataDir, book, now); err != nil {
		return res, c.fail(jw, "", err)
	}
	if len(plan.Intents) == 0 {
		return res, nil
	}

	in := plan.Intents[0]
	intentID := c.mintID(now)
	notional := Notional(in, price(snap, in))
	decision := risk.Decision{IntentID: intentID, Allowed: true, Qty: in.Qty}
	if c.Gate != nil {
		decision = c.Gate.Check(intentID, in, notional, GateState{
			Equity:           port.TotalValue.Float(),
			StartOfDayEquity: book.Marks.StartOfDayEquity,
			HighWaterEquity:  book.Marks.HighWaterEquity,
			OrdersToday:      book.Marks.OrdersToday,
			DailyLimitHits:   book.Marks.DailyLimitHits,
			Live:             c.Live,
			Confirmed:        c.Confirmed,
		})
	}
	res.Decisions = []risk.Decision{decision}
	// This one write is checked. §4.5 says every order follows an allowed
	// decision on disk; a decision that failed to reach disk has not been
	// made, so the order does not go out.
	if err := writeChecked(jw, journal.Decision(intentID, decision.Allowed, map[string]any{
		"kind": in.Kind.String(), "symbol": in.Symbol, "qty": in.Qty,
		"limit": in.Limit, "notional": notional, "opening": Opening(in),
		"reason": decision.Reason, "needs_confirm": decision.NeedsConfirm,
	})); err != nil {
		return res, fmt.Errorf("recording the risk decision: %w", err)
	}
	if !decision.Allowed {
		return res, nil
	}

	// The id the gate allowed is the id the broker deduplicates on, so a
	// journal line, a decision and an order all name the same thing.
	placed, err := c.Exec.ExecuteIntent(ctx, in, intentID)
	res.Placed = placed
	for _, p := range placed {
		if !p.Placed {
			continue
		}
		write(jw, journal.New(journal.KindOrderSubmitted, intentID, map[string]any{
			"order_id": p.OrderID, "kind": p.Intent.Kind.String(),
			"symbol": p.Intent.Symbol, "qty": p.Intent.Qty, "limit": p.Intent.Limit,
		}))
		book.Marks.OrdersToday++
		book.Pending = PendingFor(p.Intent, p.OrderID, now)
		if serr := Save(c.DataDir, book, now); serr != nil {
			// The order is out and the book does not know it. Say so
			// loudly: the next cycle would otherwise place it again.
			return res, c.fail(jw, intentID, fmt.Errorf("order %s is placed but was not recorded: %w", p.OrderID, serr))
		}
	}
	res.Marks = book.Marks
	if err != nil {
		return res, c.fail(jw, intentID, err)
	}
	return res, nil
}

// price is the quote an intent's notional is measured against.
func price(s xlksata.Snapshot, in xlksata.Intent) float64 {
	if in.Symbol == xlksata.Park {
		return s.SATA.Ask
	}
	return s.XLK.Ask
}

// tradingDay is the run id: the exchange-local date, which is what makes a
// journal file and the gate's day-scoped counters agree about "today".
func tradingDay(now time.Time) string {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return now.UTC().Format("2006-01-02")
	}
	return now.In(loc).Format("2006-01-02")
}

func (c *Cycler) mintID(now time.Time) string {
	if c.IntentID != nil {
		return c.IntentID()
	}
	return now.UTC().Format("20060102T150405.000000000")
}

func (c *Cycler) openJournal(runID string) (*journal.Writer, error) {
	if c.JournalDir == "" {
		return nil, nil
	}
	return journal.Open(c.JournalDir, runID, c.mode())
}

// fail journals an error and returns it, so a halt is on disk as well as in
// the caller's log.
func (c *Cycler) fail(jw *journal.Writer, intentID string, err error) error {
	write(jw, journal.New(journal.KindError, intentID, map[string]any{"err": err.Error()}))
	return err
}

// write records an event where there is a journal. Its failures are not
// worth failing a cycle over: a missing quote line is a gap in the record,
// not a reason to stop trading.
func write(jw *journal.Writer, ev journal.Event) {
	if jw == nil {
		return
	}
	_ = jw.Write(ev)
}

// writeChecked is write for the one event whose failure must stop the cycle.
func writeChecked(jw *journal.Writer, ev journal.Event) error {
	if jw == nil {
		return nil
	}
	return jw.Write(ev)
}
