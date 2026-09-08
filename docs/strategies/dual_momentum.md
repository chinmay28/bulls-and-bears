# dual_momentum

The `docs/STRATEGY_TEMPLATE.md` entry for Antonacci's dual momentum, filled
in by `research/scripts/dual_momentum.py`.

## Name

`dual_momentum`, strategy `dual_momentum`, golden directory
`golden/dual_momentum/`. A run on another universe names its spec
`dual_momentum_<symbols>`.

## Hypothesis

Borrowed, not invented: Antonacci, *Dual Momentum Investing* (2014), and
the time-series momentum literature behind it (Moskowitz, Ooi and Pedersen,
2012: past 12-month return predicts the next 1–12 months across 58 futures).
Relative momentum picks the recent winners among the risk assets; absolute
momentum, the comparison with the haven's own return, keeps the book out of
them when nothing has beaten the haven. The counterparty is whoever sells
strength and buys weakness at the monthly horizon. Note that this is the
sign the literature supports for the stock/gold ratio, and the opposite of
`etf_gld_ratio`.

## Universe

`[SPY, QQQ, VTI, XLK, GLD]` by default. Antonacci's own universe is US
equities, international equities and bonds with T-bills as the haven
(`SPY, EFA, AGG, BIL` on this tab); the default here is the operator's,
whose four equity ETFs move nearly together, so the relative half of the
rule has little to choose between.

## Signal

docs/CONTRACTS.md, `dual_momentum`. Every `rebalance_days` bars, rank the
risk assets by their trailing `lookback`-bar return; hold the top `top_k` of
those whose return beats the haven's; put the rest of the book in the haven.
Between rebalances the holdings stand. Each held asset is G/top_k.

The grid is lookbacks of 63, 126, 189 and 252 bars, one or two holdings,
and weekly or monthly rebalancing, chosen by training-window Sharpe.

## Sizing

Half-Kelly from the training returns, capped at 1.0: long-only, unlevered.
`max_notional_per_leg_usd 2000`.

## Exit

The next rebalance at which the asset is no longer in the top `top_k`, or no
longer ahead of the haven.

## Invalidation

Out-of-sample Sharpe under 1.0. Momentum's known failure is the sharp
reversal after a crash (2009), when the rule is still in the haven as the
recovery starts; a test window that contains one and still clears the floor
is the result to trust.

## Result

On the Kaggle mirror (2005-02-25 to 2017-11-10, docs/DISCOVERY.md), train
2005–2013, test 2014-01-01 to 2017-11-10, 5 bp slippage per side:

| window | Sharpe | CAGR | max drawdown | holding changes |
|---|---|---|---|---|
| train, lookback 252, top 2, every 5 bars | 0.89 | | | 68 over 9 years |
| **test** | **0.58** | 7.8% | −21.7% | 32 over 4 years |
| test, hold SPY / QQQ / VTI / XLK / GLD | 0.95 / 1.13 / 0.91 / 1.25 / 0.12 | | | |

Not promoted: out-of-sample Sharpe 0.58. On four near-identical equity ETFs
the relative half of the rule is mostly noise and the absolute half sat in
gold through part of a bull market. The rule's own universe (`SPY, EFA,
AGG, BIL`) is the one to try from the Research tab.
