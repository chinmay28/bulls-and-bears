package risk

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/chinmay28/bulls-and-bears/server/internal/broker"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata"
)

// Limits are the two thresholds a spec carries with it, because they are part
// of what was backtested: the per-leg notional cap and the gross leverage.
type Limits struct {
	// MaxNotionalPerLegUSD caps one order's notional; zero means no cap.
	MaxNotionalPerLegUSD float64
	// GrossLeverage caps Σ|position notional| after the intents, as a
	// multiple of equity; zero means no cap.
	GrossLeverage float64
}

// RuleGate is the Gate: the §6 rules, in order, with the thresholds from a
// Config and a spec's Limits. It has no clock and no I/O; State carries the
// moment and the account, which is what lets a test and a replay ask it about
// any situation.
type RuleGate struct {
	cfg    Config
	limits Limits
}

// NewGate builds a gate. The Config is taken as given: LoadConfig has already
// validated it, and DefaultConfig is valid by construction.
func NewGate(cfg Config, limits Limits) *RuleGate {
	return &RuleGate{cfg: cfg, limits: limits}
}

// Kill is the halt-level check, separate from Check so the runner can trip
// the kill switch on a day with no intents at all: a drawdown from the high
// water past the switch, or too many daily-limit days in a row.
func (g *RuleGate) Kill(state State) (reason string, halt bool) {
	if state.HighWaterEquity > 0 {
		dd := (state.HighWaterEquity - state.Equity) / state.HighWaterEquity
		if dd >= g.cfg.DrawdownKillSwitch {
			return fmt.Sprintf("drawdown %.1f%% from high water %.2f: kill switch at %.0f%%",
				dd*100, state.HighWaterEquity, g.cfg.DrawdownKillSwitch*100), true
		}
	}
	if g.cfg.ConsecutiveDailyLimitHits > 0 && state.DailyLimitHits >= g.cfg.ConsecutiveDailyLimitHits {
		return fmt.Sprintf("daily loss limit hit %d days in a row: halted for the rest of the week", state.DailyLimitHits), true
	}
	return "", false
}

// DailyLimitHit reports whether today's loss has reached the daily limit.
func (g *RuleGate) DailyLimitHit(state State) bool {
	if state.StartOfDayEquity <= 0 {
		return false
	}
	return (state.StartOfDayEquity-state.Equity)/state.StartOfDayEquity >= g.cfg.DailyLossLimit
}

// Check applies the rules to every intent, in order, and answers each one.
// The rules run in the §6 order and the first one an intent fails is its
// reason; intents that pass everything come back allowed with the quantity
// they may trade, which gross leverage may have scaled down.
func (g *RuleGate) Check(_ context.Context, intents []broker.Order, state State) []Decision {
	out := make([]Decision, len(intents))
	for i, o := range intents {
		out[i] = Decision{IntentID: o.IntentID, Qty: o.Qty}
	}
	if reason, halt := g.Kill(state); halt {
		for i := range out {
			out[i].Reason = reason
		}
		return out
	}

	quotes := map[string]marketdata.Quote{}
	for _, q := range state.Quotes {
		quotes[q.Symbol] = q
	}
	positions := map[string]float64{}
	for _, p := range state.Positions {
		positions[p.Symbol] += p.Qty
	}
	dailyLimit := g.DailyLimitHit(state)
	ordersLeft := g.cfg.MaxOrdersPerDay - state.OrdersToday

	// Which intents survive the per-intent rules, and at what price.
	price := make([]float64, len(intents))
	alive := make([]bool, len(intents))
	for i, o := range intents {
		q, ok := quotes[o.Symbol]
		switch {
		case !ok:
			out[i].Reason = "no quote for " + o.Symbol
			continue
		case q.At.IsZero() || state.Now.Sub(q.At) > g.cfg.StaleQuote:
			out[i].Reason = fmt.Sprintf("quote for %s is %s old, limit %s", o.Symbol, state.Now.Sub(q.At).Round(1e9), g.cfg.StaleQuote)
			continue
		}
		price[i] = fillPrice(q, o.Side)
		if price[i] <= 0 {
			out[i].Reason = "quote for " + o.Symbol + " has no usable price"
			continue
		}
		notional := o.Qty * price[i]
		if g.limits.MaxNotionalPerLegUSD > 0 && notional > g.limits.MaxNotionalPerLegUSD {
			out[i].Reason = fmt.Sprintf("notional %.2f is over the per-leg cap %.2f", notional, g.limits.MaxNotionalPerLegUSD)
			continue
		}
		if dailyLimit {
			held := positions[o.Symbol]
			switch closes(held, signed(o)) {
			case closeWhole:
				// allowed: a close after the daily limit
			case closePart:
				out[i].Reason = fmt.Sprintf("daily loss limit reached: %v %s would close the %v position and open the other way", o.Qty, o.Symbol, held)
				continue
			default:
				out[i].Reason = "daily loss limit reached: no new positions today"
				continue
			}
		}
		if g.cfg.MaxOrdersPerDay > 0 && ordersLeft <= 0 {
			out[i].Reason = fmt.Sprintf("orders per day limit %d reached", g.cfg.MaxOrdersPerDay)
			continue
		}
		ordersLeft--
		alive[i] = true
	}

	// Gross leverage: scale what survives so the book after the intents fits
	// under the cap. Closes are never the problem, and are never scaled.
	if g.limits.GrossLeverage > 0 && state.Equity > 0 {
		cap := g.limits.GrossLeverage * state.Equity
		f := g.scale(intents, alive, price, positions, quotes, cap)
		if f < 1 {
			for i, o := range intents {
				if !alive[i] || closes(positions[o.Symbol], signed(o)) == closeWhole {
					continue
				}
				if f <= 0 {
					out[i].Reason = fmt.Sprintf("gross leverage: book is already at the cap of %.2f× equity", g.limits.GrossLeverage)
					alive[i] = false
					continue
				}
				out[i].Qty = o.Qty * f
				out[i].Reason = fmt.Sprintf("scaled to %.0f%% for gross leverage %.2f× equity", f*100, g.limits.GrossLeverage)
			}
		}
	}

	for i, o := range intents {
		if !alive[i] {
			continue
		}
		out[i].Allowed = true
		if out[i].Reason == "" {
			out[i].Reason = "within limits"
		}
		if state.Live && g.cfg.ConfirmAboveUSD > 0 && out[i].Qty*price[i] > g.cfg.ConfirmAboveUSD {
			out[i].NeedsConfirm = true
		}
		_ = o
	}
	return out
}

// scale finds the largest f in [0, 1] such that the gross notional after the
// alive opening intents, scaled by f, fits under cap. Closes are applied in
// full. Bisection: the function is monotonic in f for openers.
func (g *RuleGate) scale(intents []broker.Order, alive []bool, price []float64, positions map[string]float64, quotes map[string]marketdata.Quote, cap float64) float64 {
	gross := func(f float64) float64 {
		after := map[string]float64{}
		for s, q := range positions {
			after[s] = q
		}
		for i, o := range intents {
			if !alive[i] {
				continue
			}
			d := signed(o)
			if closes(positions[o.Symbol], d) != closeWhole {
				d *= f
			}
			after[o.Symbol] += d
		}
		total := 0.0
		for s, q := range after {
			p, ok := markOf(quotes[s])
			if !ok {
				// A held symbol without a quote is marked at the intent's
				// price if one exists this round, else it cannot be counted.
				for i, o := range intents {
					if o.Symbol == s && price[i] > 0 {
						p = price[i]
					}
				}
			}
			total += math.Abs(q) * p
		}
		return total
	}
	if gross(1) <= cap {
		return 1
	}
	if gross(0) >= cap {
		return 0
	}
	lo, hi := 0.0, 1.0
	for i := 0; i < 40; i++ {
		mid := (lo + hi) / 2
		if gross(mid) <= cap {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

type closeKind int

const (
	opener closeKind = iota
	closeWhole
	closePart // reduces to zero and beyond: a flip
)

// closes classifies a signed trade against a held quantity: a trade the
// other way that does not pass through zero is a close; one that flips the
// sign is a close plus an opener; anything else opens.
func closes(held, delta float64) closeKind {
	if held == 0 || (held > 0) == (delta > 0) {
		return opener
	}
	if math.Abs(delta) <= math.Abs(held) {
		return closeWhole
	}
	return closePart
}

func signed(o broker.Order) float64 {
	if o.Side == broker.Sell {
		return -o.Qty
	}
	return o.Qty
}

// fillPrice is what a side would pay: the ask for a buy, the bid for a sell,
// Last when the quote has no book.
func fillPrice(q marketdata.Quote, side broker.Side) float64 {
	switch {
	case side == broker.Buy && q.Ask > 0:
		return q.Ask
	case side == broker.Sell && q.Bid > 0:
		return q.Bid
	}
	return q.Last
}

func markOf(q marketdata.Quote) (float64, bool) {
	if q.Last > 0 {
		return q.Last, true
	}
	if q.Bid > 0 && q.Ask > 0 {
		return (q.Bid + q.Ask) / 2, true
	}
	return 0, false
}

// Divergence classifies the gap between the paper and live books as a
// fraction of the paper equity: "ok", "warn" or "halt".
func Divergence(paperEquity, liveEquity float64, cfg Config) (level, reason string) {
	if paperEquity <= 0 {
		return "ok", "no paper equity to compare against"
	}
	gap := math.Abs(paperEquity-liveEquity) / paperEquity
	switch {
	case gap >= cfg.DivergenceHalt:
		return "halt", fmt.Sprintf("paper and live equity are %.1f%% apart, halt at %.0f%%", gap*100, cfg.DivergenceHalt*100)
	case gap >= cfg.DivergenceWarn:
		return "warn", fmt.Sprintf("paper and live equity are %.1f%% apart, warn at %.0f%%", gap*100, cfg.DivergenceWarn*100)
	}
	return "ok", fmt.Sprintf("paper and live equity are %.1f%% apart", gap*100)
}

// Symbols lists the symbols a set of intents and positions touch, sorted:
// the quotes the gate needs.
func Symbols(intents []broker.Order, state State) []string {
	set := map[string]bool{}
	for _, o := range intents {
		set[o.Symbol] = true
	}
	for _, p := range state.Positions {
		set[p.Symbol] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

var _ Gate = (*RuleGate)(nil)
