# rsi2_reversion

The `docs/STRATEGY_TEMPLATE.md` entry for Connors' two-period RSI pullback,
filled in by `research/scripts/rsi2_reversion.py`.

## Name

`rsi2_reversion`, strategy `rsi2_reversion`, golden directory
`golden/rsi2_reversion/`. A run on another universe names its spec
`rsi2_reversion_<symbols>`.

## Hypothesis

Borrowed, not invented: Connors and Alvarez, *Short Term Trading Strategies
That Work* (2009, ch. 9) and *High Probability ETF Trading* (2009). An
equity index above its 200-day average that has just had two sharp down
days tends to bounce over the next few days; the book reports 88% winning
trades on SPY 1993–2007 for the RSI(2) < 5 entry with an exit on a close
above the 5-day average, and the rule is one of the most replicated in the
open-source backtesting projects (QuantConnect's strategy library, the
backtrader and Zipline examples, Bloomberg's BTST ships RSI as a template
study). The counterparty is whoever sells the second down day. This is the
first short-horizon temporal mean-reversion rule here, and different from
`pairs_zscore` and `ratio_reversion`, which revert one asset against
another; it is also the rule most exposed to execution timing, which is why
it was not built before the next-open fill existed.

## Universe

`[SPY, QQQ, IWM, GLD]` by default: the three US equity index ETFs the rule
was written for, and gold as the haven. Large, liquid ETFs, so Yahoo's
survivorship bias does not apply.

## Signal

docs/CONTRACTS.md, `rsi2_reversion`. For each risk asset, Wilder's RSI over
two bars and the `trend_lookback`-bar average. The sleeve enters when the
close is strictly above the average and the RSI strictly under
`rsi_entry`; it leaves for the haven when the RSI is strictly over
`rsi_exit`, `max_hold_days` bars have passed, or the close falls strictly
under the average. Each held sleeve is G/k; the haven takes the rest. The
RSI period (2) and the trend window (200) are fixed.

The grid is entries of 2, 5 and 10, exits of 60, 70 and 80, and time stops
of 3 and 5 bars — eighteen candidates, chosen by training-window Sharpe
with ties going to the smaller drawdown then the lower turnover.
Researched under the next-open fill and reported at 5, 10 and 20 bp of
slippage.

## Sizing

Half-Kelly from the training returns, capped at 1.0: long-only, unlevered.
`max_notional_per_leg_usd 2000`.

## Exit

The RSI recovering over `rsi_exit`, the time stop, or the trend breaking,
whichever comes first.

## Invalidation

Out-of-sample Sharpe under 1.0 at the base cost. A rule that clears the
floor at 5 bp and not at 10 is not a rule, it is a cost model; the stress
table is the invalidation, not a footnote. A mean holding period under two
bars says the exits are firing on noise.

## Staging

Paper only by default, and for longer than the slow rules: before a manual
promotion to live, the paper book should show at least 20 filled round
trips and 30 calendar days, and the paper-versus-backtest gap on those
trips should be inside the slippage the spec was researched with. The
stage system records a state, not a count; until it records the count,
this is the operator's checklist.

## Result

On the Kaggle mirror (2005-02-25 to 2017-11-10, docs/DISCOVERY.md), train
2005–2013, test 2014-01-01 to 2017-11-10, next-open fill, 5 bp slippage per
side:

| window | Sharpe | CAGR | max drawdown | round trips |
|---|---|---|---|---|
| train, entry 10 / exit 70 / hold 5 | 0.66 | | −32.6% | 356 switches over 9 years |
| train, entry 2 / exit 70 / hold 5 | 0.64 | | −39.3% | 66 switches over 9 years |
| **test** | **0.33** | 3.7% | −22.0% | 100 over 4 years, mean hold 4.0 bars |
| test, hold SPY / QQQ / IWM / GLD | 0.95 / 1.13 / 0.56 / 0.11 | | | |
| test at 10 bp / 20 bp slippage | 0.21 / −0.04 | | | |

Not promoted: out-of-sample Sharpe 0.33 at 5 bp, and −0.04 at 20 bp. The
eighteen candidates were within 0.06 of each other on the training window,
which says the thresholds do not matter and the edge, if any, is in the
idea; the training window preferred the loosest entry, which trades three
times as often as Connors' own and paid for it out of sample. The stress
table is the finding: over 2014–2017, with a four-bar mean hold, the rule's
gross edge was about the size of 20 bp of slippage. Run it from the
Research tab with the training cutoff at 2019-12-31 for the 2020–2026
answer, and read the 10 bp row before the 5 bp one.
