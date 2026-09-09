package rotation

import (
	"context"
	"fmt"

	"github.com/chinmay28/bulls-and-bears/server/internal/robinhood"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// Writer is the order-placing half of the Robinhood client.
type Writer interface {
	ReviewEquity(ctx context.Context, r robinhood.EquityOrderRequest) (string, error)
	ReviewOption(ctx context.Context, r robinhood.OptionOrderRequest) (string, error)
	PlaceEquity(ctx context.Context, r robinhood.EquityOrderRequest) (robinhood.Order, error)
	PlaceOption(ctx context.Context, r robinhood.OptionOrderRequest) (robinhood.Order, error)
}

// Placement is what happened to one intent.
type Placement struct {
	Intent xlksata.Intent
	// Review is the broker's pre-trade answer, always fetched.
	Review string
	// OrderID is empty when the order was reviewed and not placed.
	OrderID string
	Placed  bool
	Err     error
}

// Executor carries a plan's intents to the broker.
//
// It places at most one intent per cycle, on purpose. Every intent the
// strategy emits is either the whole of what it wants now or the first step
// of a sequence whose second step depends on the first having filled — sell
// SATA then buy XLK, buy the call back then sell the shares. Placing two at
// once would send the second against a book that has not moved yet.
type Executor struct {
	Broker Writer
	// RefID makes each order idempotent. It is called once per intent and
	// must return a value stable for one logical order.
	RefID func(xlksata.Intent) string
	// DryRun reviews every intent and places none. This is not the paper
	// broker — it is a way to watch the live account decide.
	DryRun bool
}

// Execute reviews and, unless DryRun, places the plan's first intent.
// A plan with no intents is not an error: most cycles have nothing to do.
func (e *Executor) Execute(ctx context.Context, p xlksata.Plan) ([]Placement, error) {
	if len(p.Intents) == 0 {
		return nil, nil
	}
	return e.ExecuteIntent(ctx, p.Intents[0], "")
}

// ExecuteIntent reviews and places one intent under a caller-supplied
// idempotency key. The key is the intent id the risk gate allowed and the
// journal recorded, so one identifier ties the decision, the log line and
// the broker's own deduplication together.
func (e *Executor) ExecuteIntent(ctx context.Context, in xlksata.Intent, refID string) ([]Placement, error) {
	req, opt, err := e.request(in, refID)
	if err != nil {
		return nil, err
	}

	var out Placement
	out.Intent = in
	if opt != nil {
		out.Review, err = e.Broker.ReviewOption(ctx, *opt)
	} else {
		out.Review, err = e.Broker.ReviewEquity(ctx, *req)
	}
	if err != nil {
		// A review that fails is a reason to stop, not to place blind.
		out.Err = fmt.Errorf("review %s %s: %w", in.Kind, in.Symbol, err)
		return []Placement{out}, out.Err
	}
	if e.DryRun {
		return []Placement{out}, nil
	}

	var o robinhood.Order
	if opt != nil {
		o, err = e.Broker.PlaceOption(ctx, *opt)
	} else {
		o, err = e.Broker.PlaceEquity(ctx, *req)
	}
	if err != nil {
		out.Err = fmt.Errorf("place %s %s: %w", in.Kind, in.Symbol, err)
		return []Placement{out}, out.Err
	}
	out.OrderID, out.Placed = o.ID, true
	return []Placement{out}, nil
}

// request turns one intent into the order it means. Exactly one of the two
// is non-nil.
func (e *Executor) request(in xlksata.Intent, ref string) (*robinhood.EquityOrderRequest, *robinhood.OptionOrderRequest, error) {
	if ref == "" && e.RefID != nil {
		ref = e.RefID(in)
	}
	switch in.Kind {
	case xlksata.BuyEquity, xlksata.SellEquity:
		side := robinhood.Buy
		if in.Kind == xlksata.SellEquity {
			side = robinhood.Sell
		}
		return &robinhood.EquityOrderRequest{
			Symbol: in.Symbol, Side: side, Qty: in.Qty, Limit: in.Limit, RefID: ref,
		}, nil, nil

	case xlksata.SellCallToOpen:
		return nil, &robinhood.OptionOrderRequest{
			OptionID: in.OptionID, ChainSymbol: in.Symbol, Side: robinhood.Sell,
			Open: true, Contracts: int(in.Qty), Limit: in.Limit, RefID: ref,
		}, nil

	case xlksata.BuyCallToClose:
		return nil, &robinhood.OptionOrderRequest{
			OptionID: in.OptionID, ChainSymbol: in.Symbol, Side: robinhood.Buy,
			Open: false, Contracts: int(in.Qty), Limit: in.Limit, RefID: ref,
		}, nil
	}
	return nil, nil, fmt.Errorf("rotation: intent kind %d is not one this executor sends", in.Kind)
}
