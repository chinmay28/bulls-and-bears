package robinhood

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Chain is one underlying's option chain. MinTicks is the increment a limit
// price must land on; an order off the tick is rejected. SelloutSeconds is
// how long before expiry the broker force-closes a short contract, which is
// an exit the runtime does not control and must expect.
type Chain struct {
	ID              string   `json:"id"`
	Symbol          string   `json:"symbol"`
	CanOpenPosition bool     `json:"can_open_position"`
	Expirations     []string `json:"expiration_dates"`
	Multiplier      Num      `json:"trade_value_multiplier"`
	MinTicks        struct {
		Above  Num `json:"above_tick"`
		Below  Num `json:"below_tick"`
		Cutoff Num `json:"cutoff_price"`
	} `json:"min_ticks"`
	SettleOnOpen   bool `json:"settle_on_open"`
	SelloutSeconds int  `json:"sellout_time_to_expiration"`
}

// ExpirationsWithin lists the chain's expirations falling inside the given
// day window from now, inclusive at both ends — the 2-7 DTE band the
// rotation writes in.
func (c Chain) ExpirationsWithin(now time.Time, minDTE, maxDTE int) []string {
	day := func(t time.Time) time.Time {
		t = t.UTC()
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	var out []string
	for _, e := range c.Expirations {
		d, err := time.Parse("2006-01-02", e)
		if err != nil {
			continue
		}
		dte := int(day(d).Sub(day(now)).Hours() / 24)
		if dte >= minDTE && dte <= maxDTE {
			out = append(out, e)
		}
	}
	return out
}

// Chains lists the chains for an underlying. A symbol may have more than
// one, so the caller is given all of them.
func (c *Client) Chains(ctx context.Context, symbol string) ([]Chain, error) {
	var out struct {
		Chains []Chain `json:"chains"`
	}
	if err := c.call(ctx, "get_option_chains", map[string]any{"underlying_symbol": symbol}, &out); err != nil {
		return nil, err
	}
	return out.Chains, nil
}

// Instrument is one contract.
type Instrument struct {
	ID          string `json:"id"`
	ChainID     string `json:"chain_id"`
	ChainSymbol string `json:"chain_symbol"`
	Expiration  Time   `json:"expiration_date"`
	Strike      Num    `json:"strike_price"`
	Type        string `json:"type"`
	State       string `json:"state"`
	Tradability string `json:"tradability"`
	Multiplier  Num    `json:"trade_value_multiplier"`
	Sellout     Time   `json:"sellout_datetime"`
}

// Tradable reports whether an order may be sent for this contract.
func (i Instrument) Tradable() bool { return i.State == "active" && i.Tradability == "tradable" }

// Calls lists the call contracts on a chain for the given expirations.
func (c *Client) Calls(ctx context.Context, chainID string, expirations []string) ([]Instrument, error) {
	if len(expirations) == 0 {
		return nil, nil
	}
	var out struct {
		Instruments []Instrument `json:"instruments"`
	}
	args := map[string]any{
		"chain_id":         chainID,
		"expiration_dates": strings.Join(expirations, ","),
		"type":             "call",
		"state":            "active",
	}
	if err := c.call(ctx, "get_option_instruments", args, &out); err != nil {
		return nil, err
	}
	return out.Instruments, nil
}

// OptionQuote is one contract's live market and greeks. Delta is why this
// package exists in the shape it does: the server computes it, so a
// delta-targeted rule needs no pricing model of ours (docs/DISCOVERY.md).
type OptionQuote struct {
	InstrumentID string `json:"instrument_id"`
	BidPrice     Num    `json:"bid_price"`
	AskPrice     Num    `json:"ask_price"`
	MarkPrice    Num    `json:"mark_price"`
	Delta        Num    `json:"delta"`
	Gamma        Num    `json:"gamma"`
	Theta        Num    `json:"theta"`
	Vega         Num    `json:"vega"`
	ImpliedVol   Num    `json:"implied_volatility"`
	OpenInterest int    `json:"open_interest"`
	Volume       int    `json:"volume"`
	UpdatedAt    Time   `json:"updated_at"`
}

// OptionQuotes reads quotes for contracts by instrument id. The server drops
// closes above twenty ids per call, so callers batch; this returns what it
// was given, keyed by id.
func (c *Client) OptionQuotes(ctx context.Context, ids ...string) (map[string]OptionQuote, error) {
	if len(ids) == 0 {
		return map[string]OptionQuote{}, nil
	}
	var out struct {
		Results []struct {
			Quote OptionQuote `json:"quote"`
		} `json:"results"`
	}
	if err := c.call(ctx, "get_option_quotes", map[string]any{"instrument_ids": ids}, &out); err != nil {
		return nil, err
	}
	got := make(map[string]OptionQuote, len(out.Results))
	for _, r := range out.Results {
		got[r.Quote.InstrumentID] = r.Quote
	}
	return got, nil
}

// OptionPosition is an open contract position. Type is "short" or "long";
// the rotation may only ever hold one short call and never a long one.
type OptionPosition struct {
	OptionID     string `json:"option_id"`
	ChainSymbol  string `json:"chain_symbol"`
	Type         string `json:"type"`
	Quantity     Num    `json:"quantity"`
	AveragePrice Num    `json:"average_price"`
	Expiration   Time   `json:"expiration_date"`
	Multiplier   Num    `json:"trade_value_multiplier"`
}

// Short reports whether this is a written contract.
func (p OptionPosition) Short() bool { return p.Type == "short" && p.Quantity.Float() != 0 }

// OptionPositions lists currently open option positions.
func (c *Client) OptionPositions(ctx context.Context) ([]OptionPosition, error) {
	var out struct {
		Positions []OptionPosition `json:"positions"`
	}
	args := map[string]any{"account_number": c.Account, "nonzero": true}
	if err := c.call(ctx, "get_option_positions", args, &out); err != nil {
		return nil, err
	}
	return out.Positions, nil
}

// AveragePrice on a Robinhood option position is per contract, not per
// share: a 0.90 credit on a 100-multiplier contract reports as 90. PerShare
// converts it back to the premium the strategy reasons in.
func (p OptionPosition) PerShare() float64 {
	m := p.Multiplier.Float()
	if m <= 0 {
		m = 100
	}
	return p.AveragePrice.Float() / m
}

// StrikeOf looks a contract's strike up, which an option position does not
// carry: the server returns an id and expects a separate instrument call.
func (c *Client) StrikeOf(ctx context.Context, optionID string) (Instrument, error) {
	var out struct {
		Instruments []Instrument `json:"instruments"`
	}
	if err := c.call(ctx, "get_option_instruments", map[string]any{"ids": optionID}, &out); err != nil {
		return Instrument{}, err
	}
	if len(out.Instruments) == 0 {
		return Instrument{}, fmt.Errorf("%w: contract %s is not in the chain", ErrShape, optionID)
	}
	return out.Instruments[0], nil
}
