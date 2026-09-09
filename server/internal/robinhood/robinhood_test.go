package robinhood

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/mcp"
)

// fake is a Tools that answers from testdata, and records what it was asked.
// Every fixture under testdata/ is a real answer from the live server on
// 2026-09-09, copied verbatim (docs/DISCOVERY.md).
type fake struct {
	files map[string]string // tool name -> fixture file
	body  map[string]string // tool name -> literal body, for the odd cases
	err   error

	calls []call
}

type call struct {
	tool string
	args map[string]any
}

func (f *fake) CallTool(_ context.Context, name string, args any) (*mcp.ToolResult, error) {
	m := map[string]any{}
	if b, err := json.Marshal(args); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	f.calls = append(f.calls, call{tool: name, args: m})
	if f.err != nil {
		return nil, f.err
	}
	if body, ok := f.body[name]; ok {
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: body}}}, nil
	}
	file, ok := f.files[name]
	if !ok {
		return nil, errors.New("fake: no fixture for " + name)
	}
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		return nil, err
	}
	return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: string(b)}}}, nil
}

func (f *fake) lastArgs(t *testing.T, tool string) map[string]any {
	t.Helper()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].tool == tool {
			return f.calls[i].args
		}
	}
	t.Fatalf("%s was never called (called %v)", tool, f.calls)
	return nil
}

func client(files map[string]string) (*Client, *fake) {
	f := &fake{files: files, body: map[string]string{}}
	return &Client{Tools: f, Account: "682795521"}, f
}

const agentic = "682795521"

func TestNumDecodesBothForms(t *testing.T) {
	var v struct {
		A, B, C, D, E Num
	}
	err := json.Unmarshal([]byte(`{"A":"187.870000","B":1.5,"C":"","D":null,"E":"0"}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		got  Num
		want float64
	}{{v.A, 187.87}, {v.B, 1.5}, {v.C, 0}, {v.D, 0}, {v.E, 0}} {
		if c.got.Float() != c.want {
			t.Errorf("got %v, want %v", c.got.Float(), c.want)
		}
	}
	// A price this package cannot read is not a price of zero.
	var bad struct{ A Num }
	if err := json.Unmarshal([]byte(`{"A":"n/a"}`), &bad); err == nil {
		t.Error("want an error for an unreadable number, got none")
	}
}

func TestTimeDecodesEveryFormTheServerSends(t *testing.T) {
	var v struct{ A, B, C, D Time }
	err := json.Unmarshal([]byte(`{
		"A":"2026-09-09T05:33:09.906108834Z",
		"B":"2026-09-11T19:45:00+00:00",
		"C":"2026-09-11",
		"D":null}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	if v.A.Time.IsZero() || v.B.Time.IsZero() {
		t.Fatal("timestamps did not parse")
	}
	if got := v.C.Time.Format("2006-01-02"); got != "2026-09-11" {
		t.Errorf("bare date parsed as %s", got)
	}
	if !v.D.Time.IsZero() {
		t.Error("null should be the zero time")
	}
}

func TestCallRejectsAnswersItCannotTrust(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"not json":    "gateway timeout",
		"no envelope": `{"guide":"prose only"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c, f := client(nil)
			f.body["get_portfolio"] = body
			if _, err := c.Portfolio(context.Background()); !errors.Is(err, ErrShape) {
				t.Fatalf("want ErrShape, got %v", err)
			}
		})
	}
	t.Run("no transport", func(t *testing.T) {
		c := &Client{Account: agentic}
		if _, err := c.Portfolio(context.Background()); !errors.Is(err, ErrShape) {
			t.Fatalf("want ErrShape, got %v", err)
		}
	})
}

func TestCheckAccount(t *testing.T) {
	files := map[string]string{"get_accounts": "accounts.json"}

	t.Run("the agentic account passes", func(t *testing.T) {
		c, _ := client(files)
		a, err := c.CheckAccount(context.Background(), true)
		if err != nil {
			t.Fatal(err)
		}
		if a.Number != agentic || !a.AgenticAllowed || !a.WritesOptions() {
			t.Fatalf("got %+v", a)
		}
	})

	t.Run("an account this agent may not trade is a halt", func(t *testing.T) {
		c, _ := client(files)
		c.Account = "5SE52241" // option level 3, but not agentic
		if _, err := c.CheckAccount(context.Background(), true); !errors.Is(err, ErrShape) {
			t.Fatalf("want ErrShape, got %v", err)
		} else if !strings.Contains(err.Error(), "not the one this agent may trade") {
			t.Fatalf("error should say why: %v", err)
		}
	})

	t.Run("an account with no options is a halt when options are needed", func(t *testing.T) {
		c, _ := client(map[string]string{"get_accounts": "accounts.json"})
		c.Account = "712077270"
		if _, err := c.CheckAccount(context.Background(), true); !errors.Is(err, ErrShape) {
			t.Fatalf("want ErrShape, got %v", err)
		}
	})

	t.Run("an unknown account is a halt", func(t *testing.T) {
		c, _ := client(files)
		c.Account = "999"
		if _, err := c.CheckAccount(context.Background(), false); !errors.Is(err, ErrShape) {
			t.Fatalf("want ErrShape, got %v", err)
		}
	})

	t.Run("the number stays out of the message", func(t *testing.T) {
		c, _ := client(files)
		c.Account = "999888777"
		_, err := c.CheckAccount(context.Background(), false)
		if strings.Contains(err.Error(), "999888777") {
			t.Fatalf("account number should be masked: %v", err)
		}
	})
}

func TestPortfolio(t *testing.T) {
	c, f := client(map[string]string{"get_portfolio": "portfolio.json"})
	p, err := c.Portfolio(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The account as it actually stood: all crypto, nothing spendable.
	if p.Spendable() != 0 {
		t.Errorf("buying power %v, want 0", p.Spendable())
	}
	if p.CryptoValue.Float() < 394 || p.EquityValue.Float() != 0 {
		t.Errorf("got %+v", p)
	}
	if got := f.lastArgs(t, "get_portfolio")["account_number"]; got != agentic {
		t.Errorf("scoped to %v, want the configured account", got)
	}
}

func TestEquityQuotes(t *testing.T) {
	c, _ := client(map[string]string{"get_equity_quotes": "equity_quotes.json"})

	got, err := c.EquityQuotes(context.Background(), "XLK", "SATA")
	if err != nil {
		t.Fatal(err)
	}
	xlk := got["XLK"]
	if xlk.BidPrice.Float() != 188.08 || xlk.AskPrice.Float() != 188.54 {
		t.Errorf("XLK book: %+v", xlk)
	}
	if !xlk.Tradable() {
		t.Error("XLK should read as tradable")
	}
	if xlk.Fresh().IsZero() {
		t.Error("quote should carry an age")
	}

	// A symbol that was asked for and did not come back is a halt, not a
	// zero: a leg silently priced at zero is worse than a stopped run.
	if _, err := c.EquityQuotes(context.Background(), "XLK", "NOPE"); !errors.Is(err, ErrShape) {
		t.Fatalf("want ErrShape for a missing symbol, got %v", err)
	}
}

func TestQuoteFreshnessTakesTheOlderSide(t *testing.T) {
	older := time.Date(2026, 9, 9, 5, 30, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 9, 5, 33, 0, 0, time.UTC)
	q := Quote{BidTime: Time{older}, AskTime: Time{newer}}
	if !q.Fresh().Equal(older) {
		t.Errorf("got %v, want the older side %v", q.Fresh(), older)
	}
	q = Quote{BidTime: Time{newer}, AskTime: Time{older}}
	if !q.Fresh().Equal(older) {
		t.Errorf("got %v, want the older side %v", q.Fresh(), older)
	}
}

func TestPositions(t *testing.T) {
	c, _ := client(map[string]string{
		"get_equity_positions": "empty_positions.json",
		"get_option_positions": "empty_positions.json",
	})
	eq, err := c.EquityPositions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(eq) != 0 || Shares(eq, "XLK") != 0 {
		t.Fatalf("got %+v", eq)
	}
	op, err := c.OptionPositions(context.Background())
	if err != nil || len(op) != 0 {
		t.Fatalf("got %+v, %v", op, err)
	}
}

func TestSharesIsCaseInsensitive(t *testing.T) {
	ps := []EquityPosition{{Symbol: "xlk", Quantity: 100}}
	if got := Shares(ps, "XLK"); got != 100 {
		t.Errorf("got %v, want 100", got)
	}
}

func TestOptionPositionPerShare(t *testing.T) {
	// The server reports the premium per contract; the strategy reasons in
	// premium per share.
	p := OptionPosition{AveragePrice: 90, Multiplier: 100}
	if got := p.PerShare(); got != 0.90 {
		t.Errorf("got %v, want 0.90", got)
	}
	// A contract with no multiplier reported falls back to the standard 100.
	p = OptionPosition{AveragePrice: 90}
	if got := p.PerShare(); got != 0.90 {
		t.Errorf("got %v, want 0.90", got)
	}
}

func TestChains(t *testing.T) {
	c, _ := client(map[string]string{"get_option_chains": "option_chains.json"})
	chains, err := c.Chains(context.Background(), "XLK")
	if err != nil || len(chains) != 1 {
		t.Fatalf("got %+v, %v", chains, err)
	}
	ch := chains[0]
	if ch.MinTicks.Below.Float() != 0.01 || ch.MinTicks.Above.Float() != 0.05 || ch.MinTicks.Cutoff.Float() != 3 {
		t.Errorf("min ticks: %+v", ch.MinTicks)
	}
	if ch.SelloutSeconds != 1800 {
		t.Errorf("sellout %d, want 1800", ch.SelloutSeconds)
	}

	// From a Wednesday, the 2-7 day band catches the Friday and nothing else:
	// the next weekly is 9 days out. The rotation will often have exactly one
	// expiration to choose strikes from.
	now := time.Date(2026, 9, 9, 14, 12, 0, 0, time.UTC)
	got := ch.ExpirationsWithin(now, 2, 7)
	if len(got) != 1 || got[0] != "2026-09-11" {
		t.Errorf("got %v, want just 2026-09-11", got)
	}
	if got := ch.ExpirationsWithin(now, 0, 30); len(got) != 4 {
		t.Errorf("a wide band should take all four: %v", got)
	}
}

func TestCallsAndQuotes(t *testing.T) {
	c, f := client(map[string]string{
		"get_option_instruments": "option_instruments.json",
		"get_option_quotes":      "option_quotes.json",
	})
	ins, err := c.Calls(context.Background(), "chain", []string{"2026-09-11"})
	if err != nil || len(ins) != 1 {
		t.Fatalf("got %+v, %v", ins, err)
	}
	if !ins[0].Tradable() || ins[0].Strike.Float() != 190 || ins[0].Type != "call" {
		t.Errorf("got %+v", ins[0])
	}
	if got := f.lastArgs(t, "get_option_instruments")["type"]; got != "call" {
		t.Errorf("should ask for calls only, asked %v", got)
	}

	// No expirations means no call at all, rather than an unfiltered chain.
	before := len(f.calls)
	if got, err := c.Calls(context.Background(), "chain", nil); err != nil || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(f.calls) != before {
		t.Error("an empty expiration list should not reach the server")
	}

	qs, err := c.OptionQuotes(context.Background(), ins[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	q := qs[ins[0].ID]
	// Delta is the field the whole covered-call rule rests on.
	if q.Delta.Float() < 0.31 || q.Delta.Float() > 0.32 {
		t.Errorf("delta %v, want about 0.3121", q.Delta.Float())
	}
	if q.BidPrice.Float() != 0.49 || q.AskPrice.Float() != 1.15 || q.MarkPrice.Float() != 0.82 {
		t.Errorf("book: %+v", q)
	}
	if q.OpenInterest != 1528 || q.Volume != 164 {
		t.Errorf("liquidity: oi %d vol %d", q.OpenInterest, q.Volume)
	}
}

func TestWorkingOrders(t *testing.T) {
	body := func(state, symbol string) string {
		return `{"data":{"orders":[{"id":"1","state":"` + state + `","symbol":"` + symbol + `","side":"buy","quantity":"100"}]}}`
	}
	none := `{"data":{"orders":[]}}`

	t.Run("counts an order still out", func(t *testing.T) {
		c, f := client(nil)
		f.body["get_equity_orders"] = body("confirmed", "XLK")
		f.body["get_option_orders"] = none
		n, err := c.WorkingOrders(context.Background(), "2026-09-09", "XLK", "SATA")
		if err != nil || n != 1 {
			t.Fatalf("got %d, %v", n, err)
		}
	})

	t.Run("ignores one that has resolved", func(t *testing.T) {
		c, f := client(nil)
		f.body["get_equity_orders"] = body("filled", "XLK")
		f.body["get_option_orders"] = none
		n, err := c.WorkingOrders(context.Background(), "", "XLK")
		if err != nil || n != 0 {
			t.Fatalf("got %d, %v", n, err)
		}
	})

	t.Run("ignores another symbol", func(t *testing.T) {
		c, f := client(nil)
		f.body["get_equity_orders"] = body("queued", "AAPL")
		f.body["get_option_orders"] = none
		n, err := c.WorkingOrders(context.Background(), "", "XLK", "SATA")
		if err != nil || n != 0 {
			t.Fatalf("got %d, %v", n, err)
		}
	})

	t.Run("an option order takes its chain symbol", func(t *testing.T) {
		c, f := client(nil)
		f.body["get_equity_orders"] = none
		f.body["get_option_orders"] = `{"data":{"orders":[{"id":"2","state":"queued","chain_symbol":"XLK","legs":[{"option_id":"o1","side":"sell","position_effect":"open"}]}]}}`
		n, err := c.WorkingOrders(context.Background(), "", "XLK")
		if err != nil || n != 1 {
			t.Fatalf("got %d, %v", n, err)
		}
	})
}

func TestEquityOrderArgs(t *testing.T) {
	c, f := client(nil)
	f.body["place_equity_order"] = `{"data":{"id":"o1","state":"queued","symbol":"XLK"}}`

	o, err := c.PlaceEquity(context.Background(), EquityOrderRequest{
		Symbol: "XLK", Side: Buy, Qty: 100, RefID: "ref-1",
	})
	if err != nil || o.ID != "o1" {
		t.Fatalf("got %+v, %v", o, err)
	}
	args := f.lastArgs(t, "place_equity_order")
	want := map[string]any{
		"account_number": agentic, "symbol": "XLK", "side": "buy",
		"quantity": "100", "type": "market", "time_in_force": "gfd",
		"market_hours": "regular_hours", "ref_id": "ref-1",
	}
	for k, v := range want {
		if args[k] != v {
			t.Errorf("%s = %v, want %v", k, args[k], v)
		}
	}
	if _, ok := args["limit_price"]; ok {
		t.Error("a market order must not carry a limit price")
	}

	// A limit is sent as a limit order.
	_, _ = c.PlaceEquity(context.Background(), EquityOrderRequest{Symbol: "SATA", Side: Sell, Qty: 1.5, Limit: 99.93})
	args = f.lastArgs(t, "place_equity_order")
	if args["type"] != "limit" || args["limit_price"] != "99.93" || args["quantity"] != "1.5" {
		t.Errorf("limit order args: %+v", args)
	}
}

func TestOrderRequestsRefuseWhatTheyCannotSend(t *testing.T) {
	c, _ := client(nil)
	ctx := context.Background()

	bad := []EquityOrderRequest{
		{Side: Buy, Qty: 1},                   // no symbol
		{Symbol: "XLK", Qty: 1},               // no side
		{Symbol: "XLK", Side: "hold", Qty: 1}, // not a side
		{Symbol: "XLK", Side: Buy},            // no quantity
		{Symbol: "XLK", Side: Buy, Qty: -1},   // negative
	}
	for i, r := range bad {
		if _, err := c.PlaceEquity(ctx, r); !errors.Is(err, ErrShape) {
			t.Errorf("equity case %d: want ErrShape, got %v", i, err)
		}
	}

	badOpts := []OptionOrderRequest{
		{Side: Sell, Contracts: 1, Limit: 1},      // no contract
		{OptionID: "o", Contracts: 1, Limit: 1},   // no side
		{OptionID: "o", Side: Sell, Limit: 1},     // no contracts
		{OptionID: "o", Side: Sell, Contracts: 1}, // no limit: options are limit-only
	}
	for i, r := range badOpts {
		if _, err := c.PlaceOption(ctx, r); !errors.Is(err, ErrShape) {
			t.Errorf("option case %d: want ErrShape, got %v", i, err)
		}
	}
}

func TestOptionOrderArgs(t *testing.T) {
	c, f := client(nil)
	f.body["place_option_order"] = `{"data":{"id":"p1","state":"queued"}}`

	_, err := c.PlaceOption(context.Background(), OptionOrderRequest{
		OptionID: "opt-1", ChainSymbol: "XLK", Side: Sell, Open: true,
		Contracts: 1, Limit: 0.85, RefID: "ref-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	args := f.lastArgs(t, "place_option_order")
	if args["type"] != "limit" || args["price"] != "0.85" || args["quantity"] != "1" {
		t.Errorf("args: %+v", args)
	}
	// The place tool does not take the review tool's fee-pricing fields.
	if _, ok := args["chain_symbol"]; ok {
		t.Error("chain_symbol belongs to review, not place")
	}
	legs, ok := args["legs"].([]any)
	if !ok || len(legs) != 1 {
		t.Fatalf("legs: %+v", args["legs"])
	}
	leg := legs[0].(map[string]any)
	if leg["option_id"] != "opt-1" || leg["side"] != "sell" || leg["position_effect"] != "open" {
		t.Errorf("leg: %+v", leg)
	}

	// Closing sends the opposite side and effect.
	_, _ = c.PlaceOption(context.Background(), OptionOrderRequest{
		OptionID: "opt-1", Side: Buy, Open: false, Contracts: 1, Limit: 1.40,
	})
	leg = f.lastArgs(t, "place_option_order")["legs"].([]any)[0].(map[string]any)
	if leg["side"] != "buy" || leg["position_effect"] != "close" {
		t.Errorf("closing leg: %+v", leg)
	}
}

func TestReviewDoesNotCarryTheIdempotencyKey(t *testing.T) {
	c, f := client(nil)
	f.body["review_equity_order"] = `{"data":{"alerts":[]}}`
	if _, err := c.ReviewEquity(context.Background(), EquityOrderRequest{
		Symbol: "XLK", Side: Buy, Qty: 100, RefID: "ref-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.lastArgs(t, "review_equity_order")["ref_id"]; ok {
		t.Error("a review must not spend the order's idempotency key")
	}
}

func TestNumFormatting(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{{100, "100"}, {1.5, "1.5"}, {0.85, "0.85"}, {12.345678, "12.345678"}, {99.93, "99.93"}} {
		if got := num(c.in); got != c.want {
			t.Errorf("num(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
