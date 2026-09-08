# time_series_momentum

The `docs/STRATEGY_TEMPLATE.md` entry for time-series momentum, filled in by
`research/scripts/time_series_momentum.py`.

## Name

`time_series_momentum`, strategy `time_series_momentum`, golden directory
`golden/time_series_momentum/`. A run on another universe names its spec
`time_series_momentum_<symbols>`.

## Hypothesis

Borrowed, not invented: Moskowitz, Ooi and Pedersen, *Time Series Momentum*
(J. Financial Economics, 2012): across 58 futures markets an asset's own
past 12-month excess return predicts its next 1–12 months, with a Sharpe
near 1.0 for the diversified long-short portfolio. Hurst, Ooi and Pedersen,
*A Century of Evidence on Trend-Following Investing* (J. Portfolio
Management, 2017) find the 1-, 3- and 12-month versions positive in every
decade since 1880; Greyserman and Kaminski (*Trend Following with Managed
Futures*, 2014) take the 12-month sign rule back eight centuries. The
counterparty is whoever sells into a rise and buys into a fall at the
monthly horizon. This is the long-only, unlevered version: each sleeve asks
only whether its own asset is up over the lookback, never how it compares
with another asset or with the haven, which is what separates it from
`dual_momentum`.

## Universe

`[SPY, QQQ, IWM, EFA, EEM, TLT, GLD]` by default: US large, growth and
small caps, developed and emerging markets outside the US, long Treasuries,
and gold as the haven. Large, liquid ETFs, so Yahoo's survivorship bias does
not apply. Any risk assets and any haven.

## Signal

docs/CONTRACTS.md, `time_series_momentum`. Every `rebalance_days` bars each
risk asset is held if its trailing `lookback`-bar return is strictly
positive, and rests in the haven otherwise; a return of exactly zero rests.
Between rebalances the sleeves stand. Each held sleeve is G/k; the haven
takes the rest. The threshold is zero and is not a parameter.

The grid is lookbacks of 63, 126, 189 and 252 bars and rebalancing every 5
or 21 bars — eight candidates, chosen by training-window Sharpe with ties
going to the smaller drawdown, the lower turnover, then the longer
lookback. Researched under the next-open fill.

## Sizing

Half-Kelly from the training returns, capped at 1.0: long-only, unlevered.
`max_notional_per_leg_usd 2000`.

## Exit

The next rebalance bar at which the asset's trailing return is no longer
positive.

## Invalidation

Out-of-sample Sharpe under 1.0. Time-series momentum's known failure is a
sharp V-shaped reversal, when every sleeve is in the haven as the recovery
starts (2009, 2020); a test window that holds one and still clears the
floor is the result to trust. Turnover that a 10 bp slippage stress halves
the Sharpe of means the edge is thinner than the base cost model says.

## Result

On the Kaggle mirror (2005-02-25 to 2017-11-10, docs/DISCOVERY.md), train
2005–2013, test 2014-01-01 to 2017-11-10, next-open fill, 5 bp slippage per
side:

| window | Sharpe | CAGR | max drawdown | sleeve switches |
|---|---|---|---|---|
| train, lookback 63, every 5 bars | 0.69 | | −34.4% | 264 over 9 years |
| train, lookback 252, every 21 bars | 0.66 | | −28.8% | 58 over 9 years |
| **test** | **0.46** | 4.0% | −21.7% | 132 over 4 years |
| test, hold SPY / QQQ / IWM / EFA / EEM / TLT / GLD | 0.95 / 1.13 / 0.56 / 0.35 / 0.38 / 0.67 / 0.11 | | | |
| test at 10 bp / 20 bp slippage | 0.40 / 0.27 | | | |

Not promoted: out-of-sample Sharpe 0.46. The training window chose the
fastest candidate on the strength of 2008, where a three-month lookback got
out sooner; in a four-year window with nothing to sit out, that speed only
paid slippage (the turnover figure in `robustness.json` counts the daily
drift rebalancing of seven sleeves as well as the 132 signal changes). The
slowest candidate, 252 bars every 21, was within 0.03 of it on the training
window and is the one the literature actually describes; the neighbour
table in `robustness.json` records that the choice was not robust. Run it
from the Research tab with the training cutoff at 2019-12-31 for the
2020–2026 answer, which contains the reversal the invalidation clause
asks about.
