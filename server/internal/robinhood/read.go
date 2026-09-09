package robinhood

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Account is one brokerage account. AgenticAllowed is the only field that
// decides anything: Robinhood permits orders in exactly one account, and it
// is not necessarily the default one.
type Account struct {
	Number         string `json:"account_number"`
	Type           string `json:"type"`
	Nickname       string `json:"nickname"`
	AgenticAllowed bool   `json:"agentic_allowed"`
	OptionLevel    string `json:"option_level"`
	State          string `json:"state"`
	Deactivated    bool   `json:"deactivated"`
}

// WritesOptions reports whether this account may place the covered calls the
// rotation needs. Level 2 is enough; level 3 is a superset.
func (a Account) WritesOptions() bool {
	return a.OptionLevel == "option_level_2" || a.OptionLevel == "option_level_3"
}

// Accounts lists every account, tradable or not.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var out struct {
		Accounts []Account `json:"accounts"`
	}
	if err := c.call(ctx, "get_accounts", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Accounts, nil
}

// CheckAccount is the plan's §9 assertion, made before anything is placed:
// the configured account must exist, be the one this agent may trade, be
// live, and — when the strategy writes options — be approved for them.
// Anything else is a halt, not a warning.
func (c *Client) CheckAccount(ctx context.Context, needOptions bool) (Account, error) {
	accounts, err := c.Accounts(ctx)
	if err != nil {
		return Account{}, err
	}
	if c.Account == "" {
		return Account{}, fmt.Errorf("%w: no account configured to trade", ErrShape)
	}
	for _, a := range accounts {
		if a.Number != c.Account {
			continue
		}
		switch {
		case !a.AgenticAllowed:
			return a, fmt.Errorf("%w: account %s is not the one this agent may trade", ErrShape, mask(a.Number))
		case a.Deactivated || a.State != "active":
			return a, fmt.Errorf("%w: account %s is %s", ErrShape, mask(a.Number), a.State)
		case needOptions && !a.WritesOptions():
			return a, fmt.Errorf("%w: account %s has option level %q; covered calls need level 2",
				ErrShape, mask(a.Number), a.OptionLevel)
		}
		return a, nil
	}
	return Account{}, fmt.Errorf("%w: configured account %s is not in this login", ErrShape, mask(c.Account))
}

// mask keeps an account number out of a log line while leaving it
// identifiable.
func mask(n string) string {
	if len(n) <= 4 {
		return "••••"
	}
	return "••••" + n[len(n)-4:]
}

// Portfolio is the account's value and, the field that gates every buy, its
// buying power.
type Portfolio struct {
	TotalValue   Num `json:"total_value"`
	EquityValue  Num `json:"equity_value"`
	OptionsValue Num `json:"options_value"`
	CryptoValue  Num `json:"crypto_value"`
	Cash         Num `json:"cash"`
	BuyingPower  struct {
		BuyingPower Num `json:"buying_power"`
	} `json:"buying_power"`
}

// Spendable is the authoritative figure for what may be spent, which the
// server says is buying_power rather than cash.
func (p Portfolio) Spendable() float64 { return p.BuyingPower.BuyingPower.Float() }

// Portfolio reads the account's value breakdown and buying power.
func (c *Client) Portfolio(ctx context.Context) (Portfolio, error) {
	var p Portfolio
	err := c.call(ctx, "get_portfolio", map[string]any{"account_number": c.Account}, &p)
	return p, err
}

// EquityPosition is one open equity position. Quantity is what is held;
// AvailableForSells is what may actually be sold, which is smaller while
// shares are collateralising a short call.
type EquityPosition struct {
	Symbol            string `json:"symbol"`
	Quantity          Num    `json:"quantity"`
	IntradayQuantity  Num    `json:"intraday_quantity"`
	AverageBuyPrice   Num    `json:"average_buy_price"`
	AvailableForSells Num    `json:"shares_available_for_sells"`
}

// EquityPositions lists open equity positions.
func (c *Client) EquityPositions(ctx context.Context) ([]EquityPosition, error) {
	var out struct {
		Positions []EquityPosition `json:"positions"`
	}
	if err := c.call(ctx, "get_equity_positions", map[string]any{"account_number": c.Account}, &out); err != nil {
		return nil, err
	}
	return out.Positions, nil
}

// Shares is the quantity held of one symbol, and zero when it is not held.
func Shares(ps []EquityPosition, symbol string) float64 {
	for _, p := range ps {
		if strings.EqualFold(p.Symbol, symbol) {
			return p.Quantity.Float()
		}
	}
	return 0
}

// Quote is one equity's top of book. The two trade prices are separate on
// purpose: outside regular hours the regular-hours last trade goes stale
// while the extended-hours one keeps moving.
type Quote struct {
	Symbol        string `json:"symbol"`
	BidPrice      Num    `json:"bid_price"`
	AskPrice      Num    `json:"ask_price"`
	LastTrade     Num    `json:"last_trade_price"`
	LastNonReg    Num    `json:"last_non_reg_trade_price"`
	BidTime       Time   `json:"venue_bid_time"`
	AskTime       Time   `json:"venue_ask_time"`
	LastTradeTime Time   `json:"venue_last_trade_time"`
	PreviousClose Num    `json:"previous_close"`
	HasTraded     bool   `json:"has_traded"`
	State         string `json:"state"`
}

// Fresh is the older of the two sides of the book, which is the honest age
// of a quote: a stale bid with a live ask is a stale quote.
func (q Quote) Fresh() time.Time {
	if q.BidTime.Time.IsZero() || (!q.AskTime.Time.IsZero() && q.AskTime.Time.Before(q.BidTime.Time)) {
		return q.AskTime.Time
	}
	return q.BidTime.Time
}

// Tradable reports whether the instrument is in a state worth quoting.
func (q Quote) Tradable() bool { return q.State == "active" && q.HasTraded }

// EquityQuotes reads live quotes for the given symbols. Every symbol asked
// for must come back: a missing one is a shape error, because a strategy
// that silently prices a leg at zero is worse than one that halts.
func (c *Client) EquityQuotes(ctx context.Context, symbols ...string) (map[string]Quote, error) {
	var out struct {
		Results []struct {
			Quote Quote `json:"quote"`
		} `json:"results"`
	}
	if err := c.call(ctx, "get_equity_quotes", map[string]any{"symbols": symbols}, &out); err != nil {
		return nil, err
	}
	got := make(map[string]Quote, len(out.Results))
	for _, r := range out.Results {
		got[strings.ToUpper(r.Quote.Symbol)] = r.Quote
	}
	for _, s := range symbols {
		if _, ok := got[strings.ToUpper(s)]; !ok {
			return nil, fmt.Errorf("%w: asked for %s and it was not quoted", ErrShape, s)
		}
	}
	return got, nil
}
