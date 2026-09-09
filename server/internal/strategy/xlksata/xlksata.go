// Package xlksata is the XLK/SATA rotation: idle capital parks in SATA, one
// 100-share XLK lot is opened each morning the book is flat, and a lot that
// has not cleared its quick target by midday goes into a covered-call
// recovery chasing a combined +1.00% on the original purchase value.
//
// The rules are docs/strategies/xlk_sata_rotation.md, implemented here as a
// pure function — Decide(Snapshot) -> Plan. No I/O, no clock, no randomness,
// as docs/PLAN.md §1 asks of a strategy, so that one body of code answers in
// a test, a dry run and live.
//
// It deliberately does not implement strategy.Strategy and is not in that
// registry. That interface answers in target weights read off daily bars,
// and cannot say "exactly 100 shares", "sell this contract at this limit",
// or "you are in recovery holding a short call against $84 of realised
// premium". docs/DISCOVERY.md records the discrepancy; the plan's answer to
// that case is to adapt the component rather than the architecture, so this
// strategy family gets its own shape and keeps the principle that matters,
// which is purity.
//
// What it does not own: when it is called (internal/sched), where the
// numbers come from (the Robinhood adapters), whether an order is allowed
// (internal/risk), and applying fills to state — the Apply* functions here
// are pure, and the caller is what knows a fill happened.
package xlksata

import (
	"errors"
	"fmt"
	"time"
)

// Name is what a spec's `strategy` field says to get this implementation.
const Name = "xlk_sata_rotation"

// The two symbols are the strategy, not parameters of it: the risk leg is
// XLK and the parking leg is SATA. A different pair is a different study.
const (
	Risk = "XLK"
	Park = "SATA"
)

// ErrHalt wraps every condition that must stop the run rather than guess.
// docs/PLAN.md §1: missing data, stale data or a book that disagrees with
// the state file halts; it never trades on a guess.
var ErrHalt = errors.New("xlksata")

// Mode is where the one XLK lot is in its life.
type Mode int

const (
	// Flat holds no XLK; every idle dollar belongs in SATA.
	Flat Mode = iota
	// Held holds the 100-share lot opened this session, before its review.
	Held
	// Recovery holds a lot that missed its review and is now chasing the
	// combined target, writing calls against itself where one qualifies.
	Recovery
)

func (m Mode) String() string {
	switch m {
	case Flat:
		return "flat"
	case Held:
		return "held"
	case Recovery:
		return "recovery"
	}
	return "mode?"
}

// ShortCall is the one call written against the lot. There is never more
// than one, and never one without the shares behind it.
type ShortCall struct {
	OptionID   string
	Expiration time.Time
	Strike     float64
	// Credit is the premium per share actually received on the open.
	Credit float64
}

// State is what carries across calls and across restarts: the lot, and the
// running account of everything that counts toward its combined profit.
// Zero value is a flat book.
type State struct {
	Mode       Mode
	EntryPrice float64
	EntryDate  time.Time
	// OptionPnL is realised option cash on this lot: credits received less
	// debits paid, in dollars. A call still open has had its credit added
	// and its buy-back not yet subtracted.
	OptionPnL float64
	// Dividends is XLK dividend cash received on the lot, in dollars.
	Dividends float64
	// Costs is commissions and fees charged against the lot, in dollars.
	Costs     float64
	ShortCall *ShortCall
}

// Open reports whether a lot is outstanding.
func (s State) Open() bool { return s.Mode == Held || s.Mode == Recovery }

// Quote is one instrument's top of book.
type Quote struct {
	Bid, Ask, Last float64
	At             time.Time
}

func (q Quote) ok() bool { return q.Bid > 0 && q.Ask > 0 && q.Ask >= q.Bid }

// Mid is the midpoint, which is what an option is marked at.
func (q Quote) Mid() float64 { return (q.Bid + q.Ask) / 2 }

// CallQuote is one candidate contract: the chain's instrument fields and the
// live greeks, exactly as get_option_instruments and get_option_quotes give
// them (docs/DISCOVERY.md).
type CallQuote struct {
	OptionID   string
	Expiration time.Time
	Strike     float64
	Delta      float64
	Quote      Quote
	Tradable   bool
}

// Phase is which of the day's decision points this call is.
type Phase int

const (
	// PhaseEntry is 07:12 PT: open the day's lot if the book is flat.
	PhaseEntry Phase = iota + 1
	// PhaseReview is 12:07 PT: take the quick profit, or enter recovery.
	PhaseReview
	// PhaseManage is any other armed moment: work the recovery and keep
	// idle cash in SATA.
	PhaseManage
)

func (p Phase) String() string {
	switch p {
	case PhaseEntry:
		return "entry"
	case PhaseReview:
		return "review"
	case PhaseManage:
		return "manage"
	}
	return "phase?"
}

// Snapshot is everything observed at one instant. The caller fills it from
// the broker and the market before every decision, which is the "verify
// positions, open orders, buying power and obligations before every order"
// rule made structural: the engine cannot decide without them.
type Snapshot struct {
	Now   time.Time
	Phase Phase
	State State

	XLK, SATA Quote

	// XLKShares and SATAShares are what the broker says is held, and
	// ShortCalls what it says is written. They are checked against State;
	// a disagreement halts.
	XLKShares, SATAShares float64
	ShortCalls            []ShortCall

	// ShortCallQuote is the live quote of the call in State.ShortCall. It
	// prices the buy-back, so the combined figure the target is measured on
	// is what could actually be realised, not a mark.
	ShortCallQuote Quote

	// Cash is settled, spendable cash. Margin is never used, so this is the
	// hard ceiling on every buy.
	Cash float64

	// WorkingOrders counts orders already out on either symbol. Any at all
	// means this cycle places nothing: the previous one is still resolving.
	WorkingOrders int

	// Calls are the candidate XLK calls to write against the lot.
	Calls []CallQuote
}

// Kind is what an intent does.
type Kind int

const (
	BuyEquity Kind = iota + 1
	SellEquity
	SellCallToOpen
	BuyCallToClose
)

func (k Kind) String() string {
	switch k {
	case BuyEquity:
		return "buy_equity"
	case SellEquity:
		return "sell_equity"
	case SellCallToOpen:
		return "sell_call_to_open"
	case BuyCallToClose:
		return "buy_call_to_close"
	}
	return "kind?"
}

// Intent is one order to place. Limit zero means a market order, which the
// rules ask for on the equity legs and forbid on the option legs.
type Intent struct {
	Kind   Kind
	Symbol string
	Qty    float64
	Limit  float64
	// OptionID, Strike and Expiration describe the contract on an option
	// intent, and are zero on an equity one. The contract is carried whole
	// so that whatever settles the fill does not have to look it up again
	// and risk a different answer.
	OptionID   string
	Strike     float64
	Expiration time.Time
	Why        string
}

// Plan is the answer: what to place now, what the state becomes once the
// world has been re-read, and why. Intents is empty far more often than not,
// and an empty plan still carries a Reason — a run that did nothing has to
// be able to say what it was looking at.
type Plan struct {
	Intents []Intent
	// Next is State with anything already observable folded in: an
	// assignment, an expiry, a mode change. Fills are not in it; the caller
	// applies those with the Apply* functions when they land.
	Next   State
	Reason string
}

// Config is the strategy's numbers. Defaults() is the rule set as written.
type Config struct {
	LotShares      int
	QuickTarget    float64
	RecoveryTarget float64

	MinDTE, MaxDTE                  int
	MinDelta, MaxDelta, TargetDelta float64

	// FundingBuffer oversizes the SATA sale so a market fill either side
	// still funds the lot.
	FundingBuffer float64
	// MaxQuoteAge is the staleness beyond which a quote is not a quote.
	// Matches the risk gate's rule in docs/PLAN.md §6.
	MaxQuoteAge time.Duration

	// TickBelow, TickAbove and TickCutoff are the option price increments
	// the chain publishes as min_ticks.
	TickBelow, TickAbove, TickCutoff float64
}

// Defaults is the rule set as stated.
func Defaults() Config {
	return Config{
		LotShares:      100,
		QuickTarget:    0.0015,
		RecoveryTarget: 0.0100,
		MinDTE:         2,
		MaxDTE:         7,
		MinDelta:       0.20,
		MaxDelta:       0.30,
		TargetDelta:    0.25,
		FundingBuffer:  0.005,
		MaxQuoteAge:    15 * time.Minute,
		TickBelow:      0.01,
		TickAbove:      0.05,
		TickCutoff:     3.00,
	}
}

func (c Config) check() error {
	switch {
	case c.LotShares <= 0:
		return fmt.Errorf("%w: lot must be a positive number of shares, got %d", ErrHalt, c.LotShares)
	case c.RecoveryTarget < c.QuickTarget:
		return fmt.Errorf("%w: recovery target %.4f below quick target %.4f", ErrHalt, c.RecoveryTarget, c.QuickTarget)
	case c.MinDTE < 0 || c.MaxDTE < c.MinDTE:
		return fmt.Errorf("%w: dte window [%d,%d] is empty", ErrHalt, c.MinDTE, c.MaxDTE)
	case c.MinDelta <= 0 || c.MaxDelta < c.MinDelta || c.MaxDelta >= 1:
		return fmt.Errorf("%w: delta window [%.2f,%.2f] is not a range of call deltas", ErrHalt, c.MinDelta, c.MaxDelta)
	case c.TargetDelta < c.MinDelta || c.TargetDelta > c.MaxDelta:
		return fmt.Errorf("%w: target delta %.2f outside [%.2f,%.2f]", ErrHalt, c.TargetDelta, c.MinDelta, c.MaxDelta)
	case c.FundingBuffer < 0:
		return fmt.Errorf("%w: funding buffer %.4f is negative", ErrHalt, c.FundingBuffer)
	case c.MaxQuoteAge <= 0:
		return fmt.Errorf("%w: max quote age must be positive", ErrHalt)
	case c.TickBelow <= 0 || c.TickAbove <= 0:
		return fmt.Errorf("%w: option ticks must be positive", ErrHalt)
	}
	return nil
}

// Engine decides. It holds configuration and nothing else: two Engines with
// the same Config are interchangeable, and Decide never mutates the receiver.
type Engine struct{ cfg Config }

// New builds an Engine, refusing a configuration that cannot be followed.
func New(cfg Config) (*Engine, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	return &Engine{cfg: cfg}, nil
}

// Config returns the numbers this Engine follows.
func (e *Engine) Config() Config { return e.cfg }

// lot is the share count as a float, which is what every dollar figure wants.
func (e *Engine) lot() float64 { return float64(e.cfg.LotShares) }

// Basis is the original purchase value of the lot, which every percentage in
// the rules is a percentage of.
func (e *Engine) Basis(st State) float64 { return e.lot() * st.EntryPrice }

// Combined is the lot's profit in dollars if it were resolved against price
// now: share P&L, realised option cash, dividends, less costs, less what it
// would take to buy back a call still open. This is the figure the +1.00%
// target is measured on, and SATA is deliberately not in it.
func (e *Engine) Combined(st State, price float64, callAsk float64) float64 {
	c := e.lot()*(price-st.EntryPrice) + st.OptionPnL + st.Dividends - st.Costs
	if st.ShortCall != nil {
		c -= callAsk * e.lot()
	}
	return c
}

// CombinedPct is Combined over the original purchase value.
func (e *Engine) CombinedPct(st State, price, callAsk float64) float64 {
	basis := e.Basis(st)
	if basis <= 0 {
		return 0
	}
	return e.Combined(st, price, callAsk) / basis
}
