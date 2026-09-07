// Package broker is the account: what is held, what can be bought, and how
// an order goes out and comes back as a fill.
//
// Dry-run is a broker implementation, not a code branch: paper/ fills against
// live quotes and keeps its book in SQLite; robinhood/ (Phase 3) places real
// orders through the MCP. Nothing upstream of this interface knows which.
package broker

import (
	"context"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// Side of an order.
type Side int

const (
	Buy Side = iota + 1
	Sell
)

func (s Side) String() string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	}
	return "side?"
}

// Order is an intent to trade. IntentID ties it to the risk decision that
// allowed it and to the journal lines on either side of it; every order
// submitted must be preceded by an allowed decision with the same IntentID.
type Order struct {
	IntentID string
	Symbol   string
	Side     Side
	// Qty is in shares; fractional is allowed.
	Qty float64
	// Limit is the limit price; zero means a market order.
	Limit float64
}

// Fill is an execution, whole or partial.
type Fill struct {
	OrderID, IntentID, Symbol string
	Qty, Price                float64
	At                        time.Time
}

// Broker is the account.
type Broker interface {
	Equity(ctx context.Context) (float64, error)
	BuyingPower(ctx context.Context) (float64, error)
	Positions(ctx context.Context) ([]strategy.Position, error)
	Place(ctx context.Context, o Order) (orderID string, err error)
	Fills(ctx context.Context, since time.Time) ([]Fill, error)
	Cancel(ctx context.Context, orderID string) error
}
