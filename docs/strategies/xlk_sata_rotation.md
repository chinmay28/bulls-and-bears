# xlk_sata_rotation

The `docs/STRATEGY_TEMPLATE.md` entry for the XLK/SATA rotation, filled in
from the rule set as it was given rather than from a study. **Nothing here is
backtested.** The Result section below is empty and the strategy therefore
cannot be armed by the runtime: `docs/PLAN.md` §4.2 refuses a spec with no
provenance or an out-of-sample Sharpe under 1.0, and this one has neither.
That refusal is the point, and §"Invalidation" says what would lift it.

## Name

`xlk_sata_rotation`, implemented in `server/internal/strategy/xlksata`. It is
deliberately **not** registered in the `strategy` registry; see
`docs/DISCOVERY.md`, "A strategy the weights interface cannot express".

## Hypothesis

Stated, not borrowed, which is the first thing to be uneasy about — the
plan's §0 loop starts by sourcing a simple strategy from the literature and
this one arrives without a citation or a counterparty. As given, it is two
bets stacked:

1. **An intraday drift bet.** XLK bought at 07:12 PT is up at least +0.15%
   by 12:07 PT often enough to pay. Who is on the other side, and why they
   would persistently sell the first two and a half hours of a liquid
   sector ETF at a discount, is not stated. +0.15% is roughly one round trip
   of the spread plus a little; the edge, if any, is small enough that
   execution costs decide it.
2. **A recovery bet.** A day that misses becomes a covered-call position
   held until the combined trade makes +1.00%. This converts a small
   frequent gain into a rare large loss: the recovery has no time stop and
   no drawdown stop, so a lot that gaps down 15% is held, written against,
   and held again, for as long as it takes. The distribution is the one
   every martingale has — a high win rate and an unbounded tail.

The parking leg (idle capital in SATA) is a separate bet again, and its
income is deliberately excluded from the XLK trade's accounting.

## Universe

`XLK` as the risk leg and `SATA` as the parking leg. Both are single names
fixed by the rules, not a universe chosen by a study; a different pair is a
different strategy. Both quote through the Robinhood MCP
(`docs/DISCOVERY.md`). XLK is a large liquid sector ETF with weekly options;
SATA is used only as a place to hold cash and is never a signal.

## Signal

`Engine.Decide(Snapshot) -> Plan`, pure: no I/O, no clock, no randomness, as
§1 requires. It is a state machine over one lot, not a weight vector.

- **flat** — every idle dollar belongs in SATA.
- **held** — the 100-share lot opened this session, before its review.
- **recovery** — a lot that missed its review, chasing the combined target.

Three decision points a day:

| phase | when | what |
|-------|------|------|
| entry | 07:12 PT | if flat, sell the smallest whole number of SATA shares that funds exactly 100 XLK, then buy 100 XLK at market |
| review | 12:07 PT | if the lot is at or above **+0.15%** on the entry price, sell it; otherwise enter recovery |
| manage | otherwise | work the recovery; sweep idle cash to SATA |

The lot is always exactly 100 shares, there is never a second lot, the
position is never averaged down or added to, and no option is ever sold
without the shares behind it.

### Combined profit

The recovery target is **+1.00% of the original purchase value**
(`100 x entry price`). Combined profit is

```
100 x (price - entry) + realised option cash + XLK dividends - costs
```

less the cost of buying back a call still open, priced at its ask. SATA
income is not in it, by rule.

### The covered call

While in recovery and short no call, one call is written when a contract
passes every test: tradable with a two-sided market; 2–7 days to expiry;
delta within 0.20–0.30; a strike at or above the entry price; and the
**assignment test** — being called away at that strike, counting premium
already collected, this call's credit and any dividends, must still leave
the trade at or above +1.00%. Survivors are ranked by distance from a 0.25
delta, ties to the larger credit. Options are sold with limit orders, at the
midpoint rounded down to the chain's tick and never below the bid.

The assignment test is the rule that keeps the recovery from capping itself
below the number it exists to reach, and it is the reason a perfect-delta
contract is sometimes refused.

## Sizing

**No Kelly estimate exists**, because there is no backtest to estimate one
from. Size is fixed by the rules at exactly 100 shares — about $18,800 at a
187.87 close — which is a position size chosen without reference to edge or
to account equity, the opposite of what §0 step 3 asks for. Whatever is not
funding the lot sits in SATA. Margin is never used: the engine refuses any
buy that exceeds settled cash.

## Exit

- **Quick:** at the 12:07 review, at or above +0.15%.
- **Recovery:** whenever combined profit is realisable at or above +1.00%.
  A short call is bought back first, on its own cycle, so the shares are
  never sold out from under it.
- **Assignment:** if the call is assigned, the trade is complete and the
  proceeds sweep to SATA. The assignment test means this lands at or above
  +1.00% whenever the test was applied.
- **There is no time stop and no loss stop.** This is the strategy's largest
  departure from the plan, whose every other rule set carries a hard time
  stop (`max_hold_days`).

## Invalidation

What would have to be true before this is armed on real money:

1. A backtest over a window that includes 2018, 2020 and 2022, measuring the
   07:12→12:07 drift net of the spread, and reporting the **full distribution
   of recovery durations and drawdowns**, not the win rate. A rule of this
   shape will show a high win rate whatever the tail does; the win rate is
   not evidence.
2. Out-of-sample Sharpe at or above 1.0 net of costs, per §4.2, on the whole
   thing including the recoveries — not on the winning days alone.
3. A stated maximum: how long a recovery may run, and how far the lot may
   fall, before it is closed at a loss. Without one the position size is the
   only bound on the loss, and the drawdown kill switch in §6 is the only
   thing that would stop it.
4. Paper first, per Phase 3, for the 10 trading days the DoD asks for. The
   engine runs identically against `PaperBroker`, so this costs only time.

## Result

Empty. No study has been run, no spec has been generated, no golden file
exists, and the runtime will refuse to arm it until one does.
