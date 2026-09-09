package rotation

import (
	"context"
	"fmt"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// Broker is everything one cycle needs of the account: the reads that build
// a snapshot, the order history that settles the last cycle's fill, and the
// writes that place this one's order.
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
	Settled string
	Before  xlksata.State
	After   xlksata.State
	Plan    xlksata.Plan
	Placed  []Placement
}

// Cycle is one turn of the strategy: settle what was outstanding, read the
// book, decide, record, place.
//
// The order matters and is the whole design.
//
//   - Settling first means the strategy is handed a state that already
//     accounts for anything that filled since it last ran. It is the only
//     step that can set an entry price, because the entry price is a fill.
//   - The state is written before the order goes out, never after. A crash
//     between the two leaves a book that says an order may be outstanding,
//     which the next cycle checks; the reverse would lose an order that had
//     already reached the exchange.
//   - Exactly one order goes out per cycle, which is the Executor's rule and
//     the reason the sequences in the strategy are safe to interrupt.
func Cycle(ctx context.Context, b Broker, e *xlksata.Engine, ex *Executor, dataDir string, phase xlksata.Phase, now time.Time) (Result, error) {
	res := Result{Phase: phase}

	book, err := Load(dataDir)
	if err != nil {
		return res, err
	}
	res.Before = book.State

	since := now.UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02")
	book, res.Settled, err = Settle(ctx, b, e, book, since)
	if err != nil {
		return res, fmt.Errorf("settling the last order: %w", err)
	}
	if res.Settled != "" {
		if err := Save(dataDir, book, now); err != nil {
			return res, err
		}
	}

	snap, err := Snapshot(ctx, b, e.Config(), book.State, phase, now)
	if err != nil {
		return res, err
	}
	// A pending order the broker still shows as working is a working order,
	// whichever way the count came back.
	if book.Pending != nil && snap.WorkingOrders == 0 {
		snap.WorkingOrders = 1
	}

	plan, err := e.Decide(snap)
	if err != nil {
		return res, err
	}
	res.Plan = plan

	// The plan's Next carries what the book itself decided — an assignment,
	// an expiry, the move into recovery — and is recorded whether or not an
	// order follows.
	book.State = plan.Next
	res.After = plan.Next
	if err := Save(dataDir, book, now); err != nil {
		return res, err
	}
	if len(plan.Intents) == 0 {
		return res, nil
	}

	placed, err := ex.Execute(ctx, plan)
	res.Placed = placed
	if err != nil {
		return res, err
	}
	for _, p := range placed {
		if !p.Placed {
			continue
		}
		book.Pending = PendingFor(p.Intent, p.OrderID, now)
		if err := Save(dataDir, book, now); err != nil {
			// The order is out and the book does not know it. Say so
			// loudly: the next cycle would otherwise place it again.
			return res, fmt.Errorf("order %s is placed but was not recorded: %w", p.OrderID, err)
		}
	}
	return res, nil
}
