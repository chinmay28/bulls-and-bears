package xlksata

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Pick is a contract that passed every test, and the limit it would be
// written at.
type Pick struct {
	Call  CallQuote
	Limit float64
	// Assigned is the combined profit fraction the lot would realise if the
	// call were assigned at its strike. By construction it is at or above
	// the recovery target: that is the test that let this contract through.
	Assigned float64
}

// SelectCall chooses the call to write against the lot, or explains why none
// qualifies. Every rule is a filter, and the survivors are ranked by how
// close their delta is to the target.
//
// The rules, in the order they are applied: tradable, with a real bid; 2–7
// days to expiry; delta inside 0.20–0.30; a strike at or above the entry
// price, so assignment can never sell the lot at a loss; and the assignment
// test — being called away at that strike, counting the premium already
// collected, this call's credit and any dividends, must still leave the
// trade at or above the combined target. A contract that fails the last test
// is a contract that could cap the lot below the number the recovery exists
// to reach, which is why it is not written even when its delta is perfect.
func (e *Engine) SelectCall(s Snapshot, st State) (*Pick, string) {
	if len(s.Calls) == 0 {
		return nil, "no candidate contracts were supplied"
	}
	var picks []Pick
	// Counters make the "why not" answer specific, which is the difference
	// between a log line an operator can act on and one they cannot.
	var notTradable, wrongDTE, wrongDelta, lowStrike, noBid, failsAssign int

	for _, c := range s.Calls {
		if !c.Tradable || c.Quote.Bid <= 0 || c.Quote.Ask <= 0 {
			if !c.Tradable {
				notTradable++
			} else {
				noBid++
			}
			continue
		}
		if d := dteDays(s.Now, c.Expiration); d < e.cfg.MinDTE || d > e.cfg.MaxDTE {
			wrongDTE++
			continue
		}
		if c.Delta < e.cfg.MinDelta || c.Delta > e.cfg.MaxDelta {
			wrongDelta++
			continue
		}
		if c.Strike < st.EntryPrice {
			lowStrike++
			continue
		}
		limit := e.sellLimit(c.Quote)
		if limit <= 0 {
			noBid++
			continue
		}
		assigned := e.assignedPct(st, c.Strike, limit)
		if assigned < e.cfg.RecoveryTarget {
			failsAssign++
			continue
		}
		picks = append(picks, Pick{Call: c, Limit: limit, Assigned: assigned})
	}

	if len(picks) == 0 {
		return nil, fmt.Sprintf(
			"%d contracts, none qualified: %d untradable, %d without a two-sided market, %d outside %d-%d DTE, %d outside %.2f-%.2f delta, %d struck below the %.2f entry, %d would not reach %+.2f%% if assigned",
			len(s.Calls), notTradable, noBid, wrongDTE, e.cfg.MinDTE, e.cfg.MaxDTE, wrongDelta,
			e.cfg.MinDelta, e.cfg.MaxDelta, lowStrike, st.EntryPrice, failsAssign, e.cfg.RecoveryTarget*100)
	}

	// Closest to the target delta wins. The remaining keys only exist to
	// make the choice total: a pure function may not depend on map order or
	// on how the caller happened to sort the chain.
	sort.Slice(picks, func(i, j int) bool {
		a, b := picks[i], picks[j]
		if da, db := math.Abs(a.Call.Delta-e.cfg.TargetDelta), math.Abs(b.Call.Delta-e.cfg.TargetDelta); math.Abs(da-db) > 1e-9 {
			return da < db
		}
		if math.Abs(a.Limit-b.Limit) > 1e-9 {
			return a.Limit > b.Limit
		}
		if !a.Call.Expiration.Equal(b.Call.Expiration) {
			return a.Call.Expiration.Before(b.Call.Expiration)
		}
		return a.Call.OptionID < b.Call.OptionID
	})

	p := picks[0]
	return &p, fmt.Sprintf(
		"write the %s %.2f call at %.2f: delta %.3f against a %.2f target, %d DTE, strike %.2f is above the %.2f entry, and assignment there returns %+.3f%%",
		p.Call.Expiration.Format("2006-01-02"), p.Call.Strike, p.Limit, p.Call.Delta, e.cfg.TargetDelta,
		dteDays(s.Now, p.Call.Expiration), p.Call.Strike, st.EntryPrice, p.Assigned*100)
}

// assignedPct is the combined profit fraction if the lot is called away at
// strike, having received credit per share for the call being considered.
func (e *Engine) assignedPct(st State, strike, credit float64) float64 {
	basis := e.Basis(st)
	if basis <= 0 {
		return 0
	}
	lot := e.lot()
	gain := lot*(strike-st.EntryPrice) + st.OptionPnL + credit*lot + st.Dividends - st.Costs
	return gain / basis
}

// sellLimit is the price a call is offered at: the midpoint, rounded down to
// the chain's tick so the credit is never overstated, and never below the
// standing bid.
func (e *Engine) sellLimit(q Quote) float64 {
	limit := e.roundTickDown(q.Mid())
	if bid := e.roundTickDown(q.Bid); limit < bid {
		limit = bid
	}
	return limit
}

// dteDays is calendar days from the snapshot's day to the expiration day,
// both read in UTC. Expirations are dates, so this is a date subtraction
// rather than a duration.
func dteDays(now, exp time.Time) int {
	d := func(t time.Time) time.Time {
		t = t.UTC()
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return int(math.Round(d(exp).Sub(d(now)).Hours() / 24))
}

// The Apply functions fold a fill into state. They are pure and separate
// from Decide because only the caller knows a fill landed, and at what
// price: Decide answers from the book, and the book is not updated by
// deciding.

// ApplyEntry records the lot's opening fill. Everything the combined figure
// counts starts from zero here — a new lot inherits nothing from the last.
func (e *Engine) ApplyEntry(price float64, at time.Time) State {
	return State{Mode: Held, EntryPrice: price, EntryDate: at}
}

// ApplyExit clears the lot. The proceeds are cash, and cash sweeps to SATA.
func (e *Engine) ApplyExit(State) State { return State{} }

// ApplyCallOpened records a written call and the credit received for it.
func (e *Engine) ApplyCallOpened(st State, c ShortCall) State {
	st.ShortCall = &c
	st.OptionPnL += c.Credit * e.lot()
	return st
}

// ApplyCallClosed records buying the call back at price per share.
func (e *Engine) ApplyCallClosed(st State, price float64) State {
	st.OptionPnL -= price * e.lot()
	st.ShortCall = nil
	return st
}

// ApplyDividend adds dividend cash received on the lot.
func (e *Engine) ApplyDividend(st State, amount float64) State {
	st.Dividends += amount
	return st
}

// ApplyCost charges commissions and fees against the lot.
func (e *Engine) ApplyCost(st State, amount float64) State {
	st.Costs += amount
	return st
}
