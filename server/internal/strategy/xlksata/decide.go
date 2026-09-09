package xlksata

import (
	"fmt"
	"math"
	"time"
)

// Decide is the strategy. It reads a snapshot and answers with the orders to
// place now, the state those orders were decided against, and why.
//
// It is pure and it is safe to call repeatedly: every decision is a function
// of the observed book, so a cycle that placed an order and a cycle that did
// not both start from what the broker actually says. Nothing is placed while
// an order is still working, which is what keeps a repeated call from
// doubling a position.
func (e *Engine) Decide(s Snapshot) (Plan, error) {
	if err := e.validate(s); err != nil {
		return Plan{}, err
	}
	st, note, err := e.reconcile(s)
	if err != nil {
		return Plan{}, err
	}
	// Only a call the book still carries needs a quote. Asking before
	// reconcile would halt on an assignment or an expiry, where the
	// contract is gone and there is nothing left to price.
	if st.ShortCall != nil {
		if err := e.checkQuote("short call", s.ShortCallQuote, s.Now); err != nil {
			return Plan{}, err
		}
	}
	if s.WorkingOrders > 0 {
		return Plan{Next: st, Reason: join(note, fmt.Sprintf("%d order(s) still working; placing nothing", s.WorkingOrders))}, nil
	}

	var p Plan
	switch s.Phase {
	case PhaseEntry:
		p = e.entry(s, st)
	case PhaseReview:
		p = e.review(s, st)
	case PhaseManage:
		p = e.manage(s, st)
	default:
		return Plan{}, fmt.Errorf("%w: unknown phase %d", ErrHalt, s.Phase)
	}
	p.Reason = join(note, p.Reason)
	return p, nil
}

// validate refuses to decide on numbers it cannot trust. A stale or crossed
// quote is not a quote, and the SATA leg is only required when the book is
// flat, which is the only time SATA is traded.
func (e *Engine) validate(s Snapshot) error {
	if s.Now.IsZero() {
		return fmt.Errorf("%w: snapshot has no timestamp", ErrHalt)
	}
	if err := e.checkQuote(Risk, s.XLK, s.Now); err != nil {
		return err
	}
	if s.State.Mode == Flat {
		if err := e.checkQuote(Park, s.SATA, s.Now); err != nil {
			return err
		}
	}
	if s.Cash < 0 {
		return fmt.Errorf("%w: cash is negative (%.2f); this book does not use margin", ErrHalt, s.Cash)
	}
	if s.State.Open() && s.State.EntryPrice <= 0 {
		return fmt.Errorf("%w: %s lot is open with no entry price", ErrHalt, s.State.Mode)
	}
	return nil
}

func (e *Engine) checkQuote(what string, q Quote, now time.Time) error {
	if !q.ok() {
		return fmt.Errorf("%w: %s quote is not usable (bid %.4f, ask %.4f)", ErrHalt, what, q.Bid, q.Ask)
	}
	if q.At.IsZero() {
		return fmt.Errorf("%w: %s quote carries no timestamp", ErrHalt, what)
	}
	if age := now.Sub(q.At); age > e.cfg.MaxQuoteAge {
		return fmt.Errorf("%w: %s quote is %s old, past the %s limit", ErrHalt, what, age.Round(time.Second), e.cfg.MaxQuoteAge)
	}
	if q.At.After(now.Add(time.Minute)) {
		return fmt.Errorf("%w: %s quote is timestamped %s in the future", ErrHalt, what, q.At.Sub(now).Round(time.Second))
	}
	return nil
}

// reconcile checks the state file against the book and folds in what the
// book has already decided: a call that was assigned, and one that expired
// or was closed elsewhere. Any other disagreement halts — a book that is not
// what the state says it is cannot be traded from.
func (e *Engine) reconcile(s Snapshot) (State, string, error) {
	st := s.State
	lot := e.lot()

	if len(s.ShortCalls) > 1 {
		return st, "", fmt.Errorf("%w: broker reports %d short calls; this strategy writes at most one", ErrHalt, len(s.ShortCalls))
	}
	held, broker := st.ShortCall, (*ShortCall)(nil)
	if len(s.ShortCalls) == 1 {
		broker = &s.ShortCalls[0]
	}

	switch {
	case held == nil && broker != nil:
		return st, "", fmt.Errorf("%w: broker reports a short %s %.2f call this strategy did not write", ErrHalt, broker.Expiration.Format("2006-01-02"), broker.Strike)

	case held != nil && broker != nil:
		if held.OptionID != broker.OptionID {
			return st, "", fmt.Errorf("%w: short call is %s at the broker, %s in state", ErrHalt, broker.OptionID, held.OptionID)
		}

	case held != nil && broker == nil:
		// The call is gone. Assignment took the shares with it; anything
		// else left them alone and the premium is simply kept.
		switch {
		case s.XLKShares == 0:
			gain := lot*(held.Strike-st.EntryPrice) + st.OptionPnL + st.Dividends - st.Costs
			pct := 0.0
			if b := e.Basis(st); b > 0 {
				pct = gain / b
			}
			note := fmt.Sprintf("assigned at %.2f: %+.2f on the lot, %+.2f%%", held.Strike, gain, pct*100)
			if pct < e.cfg.RecoveryTarget {
				note += fmt.Sprintf(" (below the %.2f%% target; the assignment strike test should have prevented this)", e.cfg.RecoveryTarget*100)
			}
			return State{}, note, nil
		case s.XLKShares == lot:
			st.ShortCall = nil
			return st, fmt.Sprintf("short %.2f call is gone with the shares intact; %.2f/share of premium is kept", held.Strike, held.Credit), nil
		default:
			return st, "", fmt.Errorf("%w: short call gone and %g %s shares held, which is neither an assignment nor an expiry", ErrHalt, s.XLKShares, Risk)
		}
	}

	// The shares themselves must be what the state says.
	if st.Open() && s.XLKShares != lot {
		return st, "", fmt.Errorf("%w: state holds a %g-share %s lot, broker reports %g", ErrHalt, lot, Risk, s.XLKShares)
	}
	if !st.Open() && s.XLKShares != 0 {
		return st, "", fmt.Errorf("%w: state is flat, broker reports %g %s shares", ErrHalt, s.XLKShares, Risk)
	}
	return st, "", nil
}

// entry is 07:12 PT. A lot is opened only into a flat book, and only out of
// SATA: enough is sold to fund exactly the lot, and not a share more.
func (e *Engine) entry(s Snapshot, st State) Plan {
	if st.Open() {
		return Plan{Next: st, Reason: fmt.Sprintf("an %s lot is already open (%s); no second lot", Risk, st.Mode)}
	}
	lot := e.lot()
	cost := lot * s.XLK.Ask

	if s.Cash >= cost {
		return Plan{
			Next: st,
			Intents: []Intent{{
				Kind: BuyEquity, Symbol: Risk, Qty: lot,
				Why: fmt.Sprintf("open the lot: %g shares at about %.2f, funded from %.2f cash", lot, s.XLK.Ask, s.Cash),
			}},
			Reason: fmt.Sprintf("flat with %.2f cash against a %.2f lot; buying", s.Cash, cost),
		}
	}

	// Sell the smallest whole number of SATA shares that covers the
	// shortfall, oversized by the funding buffer so a market fill on either
	// side still leaves the lot payable.
	short := cost*(1+e.cfg.FundingBuffer) - s.Cash
	qty := math.Ceil(short / s.SATA.Bid)
	if qty > s.SATAShares {
		qty = s.SATAShares
	}
	if qty <= 0 {
		return Plan{Next: st, Reason: fmt.Sprintf("flat with %.2f cash and no %s to sell; cannot fund a %.2f lot", s.Cash, Park, cost)}
	}
	if s.Cash+qty*s.SATA.Bid < cost {
		return Plan{Next: st, Reason: fmt.Sprintf(
			"flat, but %.2f cash plus all %g %s shares (about %.2f) does not fund a %.2f lot; holding",
			s.Cash, s.SATAShares, Park, qty*s.SATA.Bid, cost)}
	}
	return Plan{
		Next: st,
		Intents: []Intent{{
			Kind: SellEquity, Symbol: Park, Qty: qty,
			Why: fmt.Sprintf("free %.2f to fund %g %s at about %.2f", qty*s.SATA.Bid, lot, Risk, s.XLK.Ask),
		}},
		Reason: fmt.Sprintf("flat; selling %g %s to fund the lot, buying %s once it settles", qty, Park, Risk),
	}
}

// review is 12:07 PT. A lot that has made the quick target is sold; one that
// has not enters recovery and is worked from there in the same cycle.
func (e *Engine) review(s Snapshot, st State) Plan {
	switch st.Mode {
	case Flat:
		return e.sweep(st, s, "flat at the review")
	case Recovery:
		return e.recover(s, st)
	}

	ret := (s.XLK.Bid - st.EntryPrice) / st.EntryPrice
	if ret >= e.cfg.QuickTarget {
		return Plan{
			Next: st,
			Intents: []Intent{{
				Kind: SellEquity, Symbol: Risk, Qty: e.lot(),
				Why: fmt.Sprintf("%+.3f%% against the %.2f entry clears the %+.2f%% target", ret*100, st.EntryPrice, e.cfg.QuickTarget*100),
			}},
			Reason: fmt.Sprintf("review: %s is %+.3f%% on the lot; selling and sweeping to %s", Risk, ret*100, Park),
		}
	}

	st.Mode = Recovery
	p := e.recover(s, st)
	p.Reason = join(fmt.Sprintf("review: %s is %+.3f%%, short of %+.2f%%; entering recovery for %+.2f%% combined",
		Risk, ret*100, e.cfg.QuickTarget*100, e.cfg.RecoveryTarget*100), p.Reason)
	return p
}

// manage is every other armed moment: work an open recovery, and keep idle
// cash in SATA.
func (e *Engine) manage(s Snapshot, st State) Plan {
	switch st.Mode {
	case Recovery:
		return e.recover(s, st)
	case Held:
		ret := (s.XLK.Bid - st.EntryPrice) / st.EntryPrice
		return Plan{Next: st, Reason: fmt.Sprintf("lot is %+.3f%%, holding for the review", ret*100)}
	}
	return e.sweep(st, s, "flat")
}

// recover chases the combined target: take it when it is there, otherwise
// write one call against the lot when a contract qualifies, otherwise hold.
func (e *Engine) recover(s Snapshot, st State) Plan {
	callAsk := 0.0
	if st.ShortCall != nil {
		callAsk = s.ShortCallQuote.Ask
	}
	pct := e.CombinedPct(st, s.XLK.Bid, callAsk)

	if pct >= e.cfg.RecoveryTarget {
		if st.ShortCall != nil {
			// The shares cannot be sold out from under a short call, so the
			// call is bought back first and the lot goes next cycle.
			limit := e.roundTickUp(callAsk)
			return Plan{
				Next: st,
				Intents: []Intent{{
					Kind: BuyCallToClose, Symbol: Risk, Qty: 1, Limit: limit, OptionID: st.ShortCall.OptionID,
					Why: fmt.Sprintf("combined is %+.3f%% including the %.2f buy-back; closing the call before the shares", pct*100, callAsk),
				}},
				Reason: fmt.Sprintf("recovery: %+.3f%% combined clears %+.2f%%; closing the short %.2f call first", pct*100, e.cfg.RecoveryTarget*100, st.ShortCall.Strike),
			}
		}
		return Plan{
			Next: st,
			Intents: []Intent{{
				Kind: SellEquity, Symbol: Risk, Qty: e.lot(),
				Why: fmt.Sprintf("combined is %+.3f%% on a %.2f basis", pct*100, e.Basis(st)),
			}},
			Reason: fmt.Sprintf("recovery: %+.3f%% combined clears %+.2f%%; selling the lot and sweeping to %s", pct*100, e.cfg.RecoveryTarget*100, Park),
		}
	}

	if st.ShortCall != nil {
		return Plan{Next: st, Reason: fmt.Sprintf(
			"recovery: %+.3f%% combined against %+.2f%%, short the %s %.2f call; holding",
			pct*100, e.cfg.RecoveryTarget*100, st.ShortCall.Expiration.Format("2006-01-02"), st.ShortCall.Strike)}
	}

	pick, why := e.SelectCall(s, st)
	if pick == nil {
		return Plan{Next: st, Reason: fmt.Sprintf("recovery: %+.3f%% combined, no call qualifies (%s); holding %s", pct*100, why, Risk)}
	}
	return Plan{
		Next: st,
		Intents: []Intent{{
			Kind: SellCallToOpen, Symbol: Risk, Qty: 1, Limit: pick.Limit, OptionID: pick.Call.OptionID,
			Why: why,
		}},
		Reason: fmt.Sprintf("recovery: %+.3f%% combined; writing the %s %.2f call for %.2f", pct*100,
			pick.Call.Expiration.Format("2006-01-02"), pick.Call.Strike, pick.Limit),
	}
}

// sweep puts idle cash into SATA, which is where every dollar not funding a
// lot belongs. It runs at the review and on manage cycles, never at entry,
// where the cash is about to become a lot.
func (e *Engine) sweep(st State, s Snapshot, why string) Plan {
	// Buy against the ask with the funding buffer on top, so a market fill
	// above the ask still cannot overdraw into margin.
	px := s.SATA.Ask * (1 + e.cfg.FundingBuffer)
	qty := math.Floor(s.Cash/px*1e6) / 1e6
	if qty <= 0 || qty*s.SATA.Ask < 1 {
		return Plan{Next: st, Reason: fmt.Sprintf("%s, %.2f cash is too little to sweep", why, s.Cash)}
	}
	return Plan{
		Next: st,
		Intents: []Intent{{
			Kind: BuyEquity, Symbol: Park, Qty: qty,
			Why: fmt.Sprintf("idle capital belongs in %s: %.2f cash at about %.2f", Park, s.Cash, s.SATA.Ask),
		}},
		Reason: fmt.Sprintf("%s; sweeping %.2f into %s", why, s.Cash, Park),
	}
}

// roundTickUp rounds a price up to the chain's increment, which is what a
// buy limit wants: never quote below the tick you meant to pay.
func (e *Engine) roundTickUp(p float64) float64 {
	t := e.tick(p)
	return round(math.Ceil(p/t-tickEpsilon) * t)
}

// roundTickDown rounds down, which is what a sell limit wants: never ask for
// a price the chain will reject, and never overstate the credit.
func (e *Engine) roundTickDown(p float64) float64 {
	t := e.tick(p)
	return round(math.Floor(p/t+tickEpsilon) * t)
}

// tickEpsilon absorbs the binary representation error in dividing a decimal
// price by a decimal tick: 0.85/0.01 is 84.999999999999986, and flooring that
// would quote 0.84 for a price already on the tick.
const tickEpsilon = 1e-9

func (e *Engine) tick(p float64) float64 {
	if p < e.cfg.TickCutoff {
		return e.cfg.TickBelow
	}
	return e.cfg.TickAbove
}

// round trims the float noise a division leaves behind, to the cent below
// the tick sizes in play.
func round(p float64) float64 { return math.Round(p*10000) / 10000 }

func join(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}
