package rotation

import (
	"context"
	"fmt"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/robinhood"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// Reader is the part of the Robinhood client a snapshot needs. It is an
// interface so the assembly can be tested without a broker, and so the
// read half stays visibly separate from the write half.
type Reader interface {
	CheckAccount(ctx context.Context, needOptions bool) (robinhood.Account, error)
	Portfolio(ctx context.Context) (robinhood.Portfolio, error)
	EquityPositions(ctx context.Context) ([]robinhood.EquityPosition, error)
	OptionPositions(ctx context.Context) ([]robinhood.OptionPosition, error)
	EquityQuotes(ctx context.Context, symbols ...string) (map[string]robinhood.Quote, error)
	WorkingOrders(ctx context.Context, since string, symbols ...string) (int, error)
	Chains(ctx context.Context, symbol string) ([]robinhood.Chain, error)
	Calls(ctx context.Context, chainID string, expirations []string) ([]robinhood.Instrument, error)
	OptionQuotes(ctx context.Context, ids ...string) (map[string]robinhood.OptionQuote, error)
	StrikeOf(ctx context.Context, optionID string) (robinhood.Instrument, error)
}

// strikeCeiling bounds how far out of the money a candidate may be. A 0.20
// delta call is nowhere near 10% out at these expiries, so this only keeps
// the quote request from walking the whole chain.
const strikeCeiling = 1.10

// quoteBatch is the server's limit before it starts dropping fields.
const quoteBatch = 20

// Snapshot reads the whole book into the strategy's input. Everything the
// rules say to verify before an order — positions, working orders, buying
// power, option obligations, the market's own quotes — is read here, every
// cycle, and the strategy cannot be called without it.
func Snapshot(ctx context.Context, r Reader, cfg xlksata.Config, st xlksata.State, phase xlksata.Phase, now time.Time) (xlksata.Snapshot, error) {
	s := xlksata.Snapshot{Now: now, Phase: phase, State: st}

	// The account assertion first: nothing else is worth reading if this is
	// the wrong account or one that cannot write the calls the rules need.
	if _, err := r.CheckAccount(ctx, true); err != nil {
		return s, err
	}

	port, err := r.Portfolio(ctx)
	if err != nil {
		return s, err
	}
	s.Cash = port.Spendable()

	pos, err := r.EquityPositions(ctx)
	if err != nil {
		return s, err
	}
	s.XLKShares = robinhood.Shares(pos, xlksata.Risk)
	s.SATAShares = robinhood.Shares(pos, xlksata.Park)

	quotes, err := r.EquityQuotes(ctx, xlksata.Risk, xlksata.Park)
	if err != nil {
		return s, err
	}
	s.XLK = toQuote(quotes[xlksata.Risk])
	s.SATA = toQuote(quotes[xlksata.Park])

	s.WorkingOrders, err = r.WorkingOrders(ctx, now.UTC().Format("2006-01-02"), xlksata.Risk, xlksata.Park)
	if err != nil {
		return s, err
	}

	if s.ShortCalls, err = shortCalls(ctx, r); err != nil {
		return s, err
	}

	// The open call's own quote prices the buy-back, which is what makes the
	// combined figure realisable rather than a mark.
	if st.ShortCall != nil {
		qs, err := r.OptionQuotes(ctx, st.ShortCall.OptionID)
		if err != nil {
			return s, err
		}
		if q, ok := qs[st.ShortCall.OptionID]; ok {
			s.ShortCallQuote = toOptionQuote(q)
		}
	}

	// Candidates are only needed when a call could actually be written: in
	// recovery, with none open. Fetching them otherwise is a chain walk for
	// nothing.
	if st.Mode == xlksata.Recovery && st.ShortCall == nil {
		if s.Calls, err = candidates(ctx, r, cfg, st, now); err != nil {
			return s, err
		}
	}
	return s, nil
}

// shortCalls reads the written contracts the broker says are open. An option
// position carries an id but not a strike, so each one costs an instrument
// lookup; there is at most one, by rule, and the strategy halts if not.
func shortCalls(ctx context.Context, r Reader) ([]xlksata.ShortCall, error) {
	pos, err := r.OptionPositions(ctx)
	if err != nil {
		return nil, err
	}
	var out []xlksata.ShortCall
	for _, p := range pos {
		if !p.Short() {
			continue
		}
		ins, err := r.StrikeOf(ctx, p.OptionID)
		if err != nil {
			return nil, err
		}
		out = append(out, xlksata.ShortCall{
			OptionID:   p.OptionID,
			Expiration: ins.Expiration.Time,
			Strike:     ins.Strike.Float(),
			Credit:     p.PerShare(),
		})
	}
	return out, nil
}

// candidates builds the contracts the strategy may choose between: calls on
// the risk symbol, expiring inside the configured window, struck at or above
// the entry price. Everything past that — delta, the assignment test, the
// ranking — is the strategy's, not this function's.
func candidates(ctx context.Context, r Reader, cfg xlksata.Config, st xlksata.State, now time.Time) ([]xlksata.CallQuote, error) {
	chains, err := r.Chains(ctx, xlksata.Risk)
	if err != nil {
		return nil, err
	}
	var ins []robinhood.Instrument
	for _, ch := range chains {
		if !ch.CanOpenPosition {
			continue
		}
		exps := ch.ExpirationsWithin(now, cfg.MinDTE, cfg.MaxDTE)
		if len(exps) == 0 {
			continue
		}
		got, err := r.Calls(ctx, ch.ID, exps)
		if err != nil {
			return nil, err
		}
		ins = append(ins, got...)
	}

	var keep []robinhood.Instrument
	for _, i := range ins {
		strike := i.Strike.Float()
		if !i.Tradable() || strike < st.EntryPrice || strike > st.EntryPrice*strikeCeiling {
			continue
		}
		keep = append(keep, i)
	}
	if len(keep) == 0 {
		return nil, nil
	}

	quotes := make(map[string]robinhood.OptionQuote, len(keep))
	for start := 0; start < len(keep); start += quoteBatch {
		end := min(start+quoteBatch, len(keep))
		ids := make([]string, 0, end-start)
		for _, i := range keep[start:end] {
			ids = append(ids, i.ID)
		}
		got, err := r.OptionQuotes(ctx, ids...)
		if err != nil {
			return nil, err
		}
		for id, q := range got {
			quotes[id] = q
		}
	}

	out := make([]xlksata.CallQuote, 0, len(keep))
	for _, i := range keep {
		q, ok := quotes[i.ID]
		if !ok {
			// A contract nobody quoted is one nobody is trading. It is
			// dropped rather than passed on with a zero delta, which would
			// read to the strategy as a valid contract far from its target.
			continue
		}
		out = append(out, xlksata.CallQuote{
			OptionID:   i.ID,
			Expiration: i.Expiration.Time,
			Strike:     i.Strike.Float(),
			Delta:      q.Delta.Float(),
			Quote:      toOptionQuote(q),
			Tradable:   i.Tradable(),
		})
	}
	return out, nil
}

func toQuote(q robinhood.Quote) xlksata.Quote {
	return xlksata.Quote{
		Bid:  q.BidPrice.Float(),
		Ask:  q.AskPrice.Float(),
		Last: q.LastTrade.Float(),
		At:   q.Fresh(),
	}
}

func toOptionQuote(q robinhood.OptionQuote) xlksata.Quote {
	return xlksata.Quote{
		Bid:  q.BidPrice.Float(),
		Ask:  q.AskPrice.Float(),
		Last: q.MarkPrice.Float(),
		At:   q.UpdatedAt.Time,
	}
}

// PhaseAt says which decision point a clock time is, in the exchange's own
// timezone. Entry and review are the two the rules name; everything else in
// the session is a manage cycle, and outside the session there is none.
func PhaseAt(now time.Time, loc *time.Location, entry, review time.Duration, window time.Duration) (xlksata.Phase, bool) {
	local := now.In(loc)
	since := time.Duration(local.Hour())*time.Hour +
		time.Duration(local.Minute())*time.Minute +
		time.Duration(local.Second())*time.Second

	switch {
	case since >= entry && since < entry+window:
		return xlksata.PhaseEntry, true
	case since >= review && since < review+window:
		return xlksata.PhaseReview, true
	case since >= marketOpen && since <= marketClose:
		return xlksata.PhaseManage, true
	}
	return 0, false
}

// The regular session, in exchange-local time.
const (
	marketOpen  = 9*time.Hour + 30*time.Minute
	marketClose = 16 * time.Hour
)

// Times is the rotation's schedule, in exchange-local time. The rules are
// stated in Pacific and the schedule is held in Eastern, which is safe
// because both US zones change over on the same dates — 07:12 PT is 10:12 ET
// year round — and which is what the session bounds are already in.
type Times struct {
	Loc    *time.Location
	Entry  time.Duration
	Review time.Duration
	// Window is how long after each fire time the phase still counts, so a
	// late or restarted run still does the right thing.
	Window time.Duration
}

// DefaultTimes is 07:12 and 12:07 Pacific, expressed in New York.
func DefaultTimes() (Times, error) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return Times{}, fmt.Errorf("rotation: no exchange timezone: %w", err)
	}
	return Times{
		Loc:    loc,
		Entry:  10*time.Hour + 12*time.Minute, // 07:12 PT
		Review: 15*time.Hour + 7*time.Minute,  // 12:07 PT
		Window: 5 * time.Minute,
	}, nil
}

// Phase is PhaseAt against this schedule.
func (t Times) Phase(now time.Time) (xlksata.Phase, bool) {
	return PhaseAt(now, t.Loc, t.Entry, t.Review, t.Window)
}
