package rotation

import (
	"fmt"

	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// Gate applies docs/PLAN.md §6 to the rotation's orders.
//
// It is a second gate rather than a use of risk.RuleGate, and the reason is
// that the existing one prices intents through broker.Order — a symbol, a
// side and a share count — which cannot express a contract. Pushing an
// option leg through it would not fail; it would produce a confident wrong
// number, valuing a covered call as if it were a hundred shares of premium.
// So the equity-level rules, which are pure arithmetic on the account and
// mean the same thing here, are delegated to risk.RuleGate unchanged, and
// only the per-intent rules are re-stated. Both read the same risk.Config,
// so the thresholds still live in one risk.yaml (docs/DISCOVERY.md).
//
// Two judgements are recorded here rather than left implicit:
//
// The daily loss limit stops *opening* orders and allows closes. For this
// strategy an opening order is a buy — the XLK lot, or a sweep into SATA.
// Selling a covered call carries the position effect "open", but it collects
// premium against shares already held: it reduces the lot's exposure and is
// the only way the recovery can make progress. Blocking it would trap a
// losing lot with no route out, which is the opposite of what the rule is
// for, so it counts as risk-reducing.
//
// A sweep into SATA is treated as opening, and so is refused after a losing
// day's limit is hit. The consequence is that proceeds sit in cash until the
// next session. That is the literal rule, and idle cash is the safe side of
// it.
type Gate struct {
	cfg  risk.Config
	kill *risk.RuleGate
	// MaxOrderNotionalUSD refuses any single order above this. Zero is
	// uncapped, which is the default because the lot's size is fixed by the
	// rules rather than by a spec; an operator who wants a ceiling on what
	// one XLK lot may cost sets it.
	MaxOrderNotionalUSD float64
}

// NewGate builds a gate from the same config the rest of the runtime uses.
func NewGate(cfg risk.Config, maxOrderNotionalUSD float64) *Gate {
	return &Gate{
		cfg:                 cfg,
		kill:                risk.NewGate(cfg, risk.Limits{}),
		MaxOrderNotionalUSD: maxOrderNotionalUSD,
	}
}

// GateState is what the rules need to know about the account and the day.
// It is deliberately not risk.State: that carries positions and quotes for
// the leverage rule, which this gate does not apply, because the strategy is
// unleveraged by construction — every buy is already capped at settled cash.
type GateState struct {
	Equity, StartOfDayEquity, HighWaterEquity float64
	OrdersToday                               int
	DailyLimitHits                            int
	// Live is true when orders will really be placed; the confirmation
	// threshold applies only then.
	Live bool
	// Confirmed is the run's --yes: a person has agreed in advance to
	// orders above the confirmation threshold.
	Confirmed bool
}

// Opening reports whether an intent increases exposure. See the type comment
// for why a written call does not.
func Opening(in xlksata.Intent) bool { return in.Kind == xlksata.BuyEquity }

// Notional is the cash an intent puts at stake, which for an option is the
// premium times the contract multiplier.
func Notional(in xlksata.Intent, price float64) float64 {
	switch in.Kind {
	case xlksata.SellCallToOpen, xlksata.BuyCallToClose:
		return in.Limit * contractMultiplier * in.Qty
	}
	return price * in.Qty
}

// Check answers one intent. The rules run in the §6 order and the first one
// the intent fails is its reason.
func (g *Gate) Check(intentID string, in xlksata.Intent, notional float64, st GateState) risk.Decision {
	d := risk.Decision{IntentID: intentID, Qty: in.Qty}

	if reason, halt := g.kill.Kill(risk.State{
		Equity:          st.Equity,
		HighWaterEquity: st.HighWaterEquity,
		DailyLimitHits:  st.DailyLimitHits,
	}); halt {
		d.Reason = reason
		return d
	}
	if g.cfg.MaxOrdersPerDay > 0 && st.OrdersToday >= g.cfg.MaxOrdersPerDay {
		d.Reason = fmt.Sprintf("%d orders already today, limit %d", st.OrdersToday, g.cfg.MaxOrdersPerDay)
		return d
	}
	if Opening(in) && g.kill.DailyLimitHit(risk.State{
		Equity: st.Equity, StartOfDayEquity: st.StartOfDayEquity,
	}) {
		d.Reason = fmt.Sprintf("daily loss limit reached (%.2f from %.2f at the open); no opening orders today",
			st.Equity, st.StartOfDayEquity)
		return d
	}
	if g.MaxOrderNotionalUSD > 0 && notional > g.MaxOrderNotionalUSD {
		d.Reason = fmt.Sprintf("order notional %.2f is over the %.2f cap", notional, g.MaxOrderNotionalUSD)
		return d
	}

	d.Allowed = true
	if st.Live && g.cfg.ConfirmAboveUSD > 0 && notional > g.cfg.ConfirmAboveUSD {
		d.NeedsConfirm = true
		d.Reason = fmt.Sprintf("notional %.2f is over the %.2f confirmation threshold", notional, g.cfg.ConfirmAboveUSD)
		if !st.Confirmed {
			d.Allowed = false
			d.Reason += "; run with -yes to place it unattended"
		}
	}
	return d
}
