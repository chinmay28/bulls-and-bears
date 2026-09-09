package robinhood

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Working order states: an order in any of these is still resolving, and the
// rotation places nothing while one is out.
var workingStates = map[string]bool{
	"new": true, "queued": true, "confirmed": true,
	"unconfirmed": true, "partially_filled": true, "pending_cancelled": true,
}

// Order is one equity or option order, in the fields the runtime reads back.
type Order struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	Side         string `json:"side"`
	Symbol       string `json:"symbol"`
	ChainSymbol  string `json:"chain_symbol"`
	Quantity     Num    `json:"quantity"`
	Filled       Num    `json:"cumulative_quantity"`
	AveragePrice Num    `json:"average_price"`
	Price        Num    `json:"price"`
	Type         string `json:"type"`
	CreatedAt    Time   `json:"created_at"`
	RefID        string `json:"ref_id"`
	PlacedAgent  string `json:"placed_agent"`
}

// Working reports whether this order is still out.
func (o Order) Working() bool { return workingStates[o.State] }

// Filled reports whether the order completed.
func (o Order) FilledFully() bool { return o.State == "filled" }

// Instrument on an option order is carried per leg.
type optionOrderWire struct {
	Order
	Legs []struct {
		OptionID       string `json:"option_id"`
		Side           string `json:"side"`
		PositionEffect string `json:"position_effect"`
		RatioQuantity  Num    `json:"ratio_quantity"`
	} `json:"legs"`
}

// EquityOrders lists this account's equity orders created at or after since
// (RFC3339 or a bare date; empty for no bound).
func (c *Client) EquityOrders(ctx context.Context, since string) ([]Order, error) {
	args := map[string]any{"account_number": c.Account}
	if since != "" {
		args["created_at_gte"] = since
	}
	var out struct {
		Orders []Order `json:"orders"`
	}
	if err := c.call(ctx, "get_equity_orders", args, &out); err != nil {
		return nil, err
	}
	return out.Orders, nil
}

// OptionOrders lists this account's option orders created at or after since.
func (c *Client) OptionOrders(ctx context.Context, since string) ([]Order, error) {
	args := map[string]any{"account_number": c.Account}
	if since != "" {
		args["created_at_gte"] = since
	}
	var out struct {
		Orders []optionOrderWire `json:"orders"`
	}
	if err := c.call(ctx, "get_option_orders", args, &out); err != nil {
		return nil, err
	}
	orders := make([]Order, 0, len(out.Orders))
	for _, w := range out.Orders {
		o := w.Order
		if o.Symbol == "" {
			o.Symbol = o.ChainSymbol
		}
		orders = append(orders, o)
	}
	return orders, nil
}

// WorkingOrders counts orders still out on either side of the book, over the
// symbols given. It is what stands between a cycle that repeats and a
// position that doubles, so an order it cannot classify counts as working.
func (c *Client) WorkingOrders(ctx context.Context, since string, symbols ...string) (int, error) {
	eq, err := c.EquityOrders(ctx, since)
	if err != nil {
		return 0, err
	}
	op, err := c.OptionOrders(ctx, since)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, s := range symbols {
		want[strings.ToUpper(s)] = true
	}
	n := 0
	for _, o := range append(eq, op...) {
		if !o.Working() {
			continue
		}
		sym := strings.ToUpper(o.Symbol)
		if len(want) == 0 || want[sym] || sym == "" {
			n++
		}
	}
	return n, nil
}

// Side of an order.
const (
	Buy  = "buy"
	Sell = "sell"
)

// EquityOrderRequest is one equity order. A zero Limit is a market order,
// which is what the rotation's equity legs ask for; RefID is the
// idempotency key and must be stable across a retry of the same logical
// order and different for a new one.
type EquityOrderRequest struct {
	Symbol string
	Side   string
	Qty    float64
	Limit  float64
	RefID  string
}

func (r EquityOrderRequest) args(account string) (map[string]any, error) {
	if r.Symbol == "" || (r.Side != Buy && r.Side != Sell) || r.Qty <= 0 {
		return nil, fmt.Errorf("%w: incomplete equity order %+v", ErrShape, r)
	}
	args := map[string]any{
		"account_number": account,
		"symbol":         r.Symbol,
		"side":           r.Side,
		"quantity":       num(r.Qty),
		"time_in_force":  "gfd",
		"market_hours":   "regular_hours",
	}
	if r.Limit > 0 {
		args["type"] = "limit"
		args["limit_price"] = num(r.Limit)
	} else {
		args["type"] = "market"
	}
	if r.RefID != "" {
		args["ref_id"] = r.RefID
	}
	return args, nil
}

// ReviewEquity simulates an equity order and returns the server's pre-trade
// alerts. It is the natural home for the plan's §6 confirmation threshold,
// and it is free: nothing is placed.
func (c *Client) ReviewEquity(ctx context.Context, r EquityOrderRequest) (string, error) {
	args, err := r.args(c.Account)
	if err != nil {
		return "", err
	}
	delete(args, "ref_id")
	res, err := c.Tools.CallTool(ctx, "review_equity_order", args)
	if err != nil {
		return "", err
	}
	return res.Text(), nil
}

// PlaceEquity places a real equity order with real money.
//
// It is never retried here and must not be retried by the caller without
// reusing RefID: the first attempt may have reached the exchange even when
// the answer did not come back.
func (c *Client) PlaceEquity(ctx context.Context, r EquityOrderRequest) (Order, error) {
	args, err := r.args(c.Account)
	if err != nil {
		return Order{}, err
	}
	var o Order
	if err := c.call(ctx, "place_equity_order", args, &o); err != nil {
		return Order{}, err
	}
	return o, nil
}

// OptionOrderRequest is one single-leg option order. The rotation only ever
// sends two shapes: sell-to-open one call, and buy-to-close one call. Limit
// is required — the rules forbid market orders on options — and must already
// sit on the chain's tick.
type OptionOrderRequest struct {
	OptionID    string
	ChainSymbol string
	Side        string
	// Open is true to open a position, false to close one.
	Open      bool
	Contracts int
	Limit     float64
	RefID     string
}

func (r OptionOrderRequest) args(account string) (map[string]any, error) {
	effect := "close"
	if r.Open {
		effect = "open"
	}
	switch {
	case r.OptionID == "":
		return nil, fmt.Errorf("%w: option order without a contract", ErrShape)
	case r.Side != Buy && r.Side != Sell:
		return nil, fmt.Errorf("%w: option order side %q", ErrShape, r.Side)
	case r.Contracts <= 0:
		return nil, fmt.Errorf("%w: option order for %d contracts", ErrShape, r.Contracts)
	case r.Limit <= 0:
		return nil, fmt.Errorf("%w: options are traded on limit orders; no limit given", ErrShape)
	}
	args := map[string]any{
		"account_number": account,
		"quantity":       strconv.Itoa(r.Contracts),
		"type":           "limit",
		"price":          num(r.Limit),
		"time_in_force":  "gfd",
		"market_hours":   "regular_hours",
		"legs": []map[string]any{{
			"option_id":       r.OptionID,
			"side":            r.Side,
			"position_effect": effect,
			"ratio_quantity":  1,
		}},
	}
	if r.ChainSymbol != "" {
		args["chain_symbol"] = r.ChainSymbol
		args["underlying_type"] = "equity"
	}
	if r.RefID != "" {
		args["ref_id"] = r.RefID
	}
	return args, nil
}

// ReviewOption simulates an option order and returns its alerts, fees and
// collateral without placing anything.
func (c *Client) ReviewOption(ctx context.Context, r OptionOrderRequest) (string, error) {
	args, err := r.args(c.Account)
	if err != nil {
		return "", err
	}
	delete(args, "ref_id")
	res, err := c.Tools.CallTool(ctx, "review_option_order", args)
	if err != nil {
		return "", err
	}
	return res.Text(), nil
}

// PlaceOption places a real options order with real money. As with
// PlaceEquity: never retried without the same RefID.
func (c *Client) PlaceOption(ctx context.Context, r OptionOrderRequest) (Order, error) {
	args, err := r.args(c.Account)
	if err != nil {
		return Order{}, err
	}
	// review_option_order carries chain_symbol/underlying_type to price fees;
	// the place tool does not take them.
	delete(args, "chain_symbol")
	delete(args, "underlying_type")
	var o Order
	if err := c.call(ctx, "place_option_order", args, &o); err != nil {
		return Order{}, err
	}
	return o, nil
}

// num renders a quantity or price the way this server wants it: a decimal
// string, trimmed of the trailing zeros a %f would leave.
func num(f float64) string {
	s := strconv.FormatFloat(f, 'f', 6, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
