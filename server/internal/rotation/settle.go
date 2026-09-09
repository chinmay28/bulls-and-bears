package rotation

import (
	"context"
	"fmt"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/robinhood"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// Orders is the part of the client that reads an order back.
type Orders interface {
	EquityOrders(ctx context.Context, since string) ([]robinhood.Order, error)
	OptionOrders(ctx context.Context, since string) ([]robinhood.Order, error)
}

// contractMultiplier is shares per contract. Robinhood reports an option
// order's average price per contract; the strategy reasons per share.
const contractMultiplier = 100

// Settle folds a placed order's outcome into the book, and is the only
// place the lot's entry price and premium are ever set.
//
// It is called at the top of every cycle, before the snapshot is read,
// because the state the strategy is given has to already account for
// whatever filled since it last ran. An order still working is left pending
// and the book is unchanged; one that filled is applied and cleared; one
// that was cancelled or rejected is cleared with nothing applied.
func Settle(ctx context.Context, o Orders, e *xlksata.Engine, b Book, since string) (Book, string, error) {
	if b.Pending == nil {
		return b, "", nil
	}
	p := b.Pending

	order, err := findOrder(ctx, o, p, since)
	if err != nil {
		return b, "", err
	}
	if order == nil {
		// Placed, and the broker has never heard of it. Treated as not
		// filled rather than as filled: the next cycle re-reads the book,
		// and a position that did appear will halt reconciliation loudly
		// rather than being silently absorbed at a guessed price.
		b.Pending = nil
		return b, fmt.Sprintf("order %s was placed and cannot be found; nothing applied", p.OrderID), nil
	}
	if order.Working() {
		return b, fmt.Sprintf("order %s is %s", p.OrderID, order.State), nil
	}

	b.Pending = nil
	if !order.FilledFully() || order.Filled.Float() <= 0 {
		return b, fmt.Sprintf("order %s ended %s; nothing applied", p.OrderID, order.State), nil
	}

	price := order.AveragePrice.Float()
	switch p.Kind {
	case xlksata.BuyEquity:
		if p.Symbol != xlksata.Risk {
			// A sweep into the parking leg is not part of the lot.
			return b, fmt.Sprintf("swept %g %s at %.2f", order.Filled.Float(), p.Symbol, price), nil
		}
		b.State = e.ApplyEntry(price, order.CreatedAt.Time)
		return b, fmt.Sprintf("opened the lot: %g %s at %.4f", order.Filled.Float(), p.Symbol, price), nil

	case xlksata.SellEquity:
		if p.Symbol != xlksata.Risk {
			return b, fmt.Sprintf("freed %g %s at %.2f", order.Filled.Float(), p.Symbol, price), nil
		}
		pct := e.CombinedPct(b.State, price, 0) * 100
		b.State = e.ApplyExit(b.State)
		return b, fmt.Sprintf("closed the lot at %.4f, %+.3f%% combined", price, pct), nil

	case xlksata.SellCallToOpen:
		credit := price / contractMultiplier
		b.State = e.ApplyCallOpened(b.State, xlksata.ShortCall{
			OptionID: p.OptionID, Expiration: p.Expiration, Strike: p.Strike, Credit: credit,
		})
		return b, fmt.Sprintf("wrote the %.2f call for %.2f a share", p.Strike, credit), nil

	case xlksata.BuyCallToClose:
		paid := price / contractMultiplier
		b.State = e.ApplyCallClosed(b.State, paid)
		return b, fmt.Sprintf("bought the %.2f call back at %.2f a share", p.Strike, paid), nil
	}
	return b, "", fmt.Errorf("rotation: pending order %s has kind %d, which cannot be settled", p.OrderID, p.Kind)
}

// findOrder looks the pending order up on whichever side of the book it was
// placed on.
func findOrder(ctx context.Context, o Orders, p *Pending, since string) (*robinhood.Order, error) {
	var orders []robinhood.Order
	var err error
	if p.OptionID != "" {
		orders, err = o.OptionOrders(ctx, since)
	} else {
		orders, err = o.EquityOrders(ctx, since)
	}
	if err != nil {
		return nil, err
	}
	for i := range orders {
		if orders[i].ID == p.OrderID {
			return &orders[i], nil
		}
	}
	return nil, nil
}

// PendingFor describes the order an intent became, so the next cycle can
// settle it. Everything it needs is on the intent, which is why an option
// intent carries its contract's terms.
func PendingFor(in xlksata.Intent, orderID string, at time.Time) *Pending {
	return &Pending{
		OrderID:    orderID,
		Kind:       in.Kind,
		Symbol:     in.Symbol,
		OptionID:   in.OptionID,
		Expiration: in.Expiration,
		Strike:     in.Strike,
		PlacedAt:   at.UTC(),
	}
}
