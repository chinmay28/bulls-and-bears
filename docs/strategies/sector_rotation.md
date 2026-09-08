# sector_rotation

The `docs/STRATEGY_TEMPLATE.md` entry for sector rotation, filled in by
`research/scripts/sector_rotation.py`. A study, not a new strategy: the
runtime strategy is `dual_momentum`, and the spec names `sector_rotation` in
its `provenance.research_study` so that re-validation runs this script.

## Name

`sector_rotation`, strategy `dual_momentum`, golden directory
`golden/sector_rotation/`. A run on another universe names its spec
`sector_rotation_<symbols>`.

## Hypothesis

Borrowed, not invented: Moskowitz and Grinblatt, *Do Industries Explain
Momentum?* (J. Finance, 1999) find that much of stock momentum is industry
momentum, and that buying the past winners among industries and selling the
losers earns a spread on its own; Faber's *Relative Strength Strategies for
Investing* (2010) applies the same ranking to sector and asset-class ETFs
with a trend filter. The rule here is Antonacci's dual momentum unchanged —
relative momentum to pick the sectors, absolute momentum against the haven
to stay out of all of them — over a universe whose members actually differ
from each other, which the original `dual_momentum` study's four broad
equity ETFs did not. The counterparty is whoever rotates out of last
quarter's winning sector.

## Universe

`[XLB, XLE, XLF, XLI, XLK, XLP, XLU, XLV, XLY, GLD]` by default: the nine
Select Sector SPDRs listed in 1998, gold as the haven. The two newer
sectors, XLRE (2015) and XLC (2018), are left out by default because the
inner join would start the history at their listing; pass them with
`--universe` once the training window can afford it. Sector ETFs are
reconstituted as the S&P 500 changes, so the universe itself has no
survivorship problem, but a sector ETF's history is the history of a
changing basket.

## Signal

docs/CONTRACTS.md, `dual_momentum`: every `rebalance_days` bars rank the
sectors by trailing `lookback`-bar return, hold the top `top_k` of those
that beat the haven's own return, rest the remaining sleeves in the haven.

The grid is lookbacks of 126, 189 and 252 bars, two or three sectors held,
monthly rebalancing fixed — six candidates. Researched under the next-open
fill.

## Sizing

Half-Kelly from the training returns, capped at 1.0: long-only, unlevered.
`max_notional_per_leg_usd 2000`.

## Exit

The next rebalance at which the sector is out of the top `top_k` or behind
the haven.

## Invalidation

Out-of-sample Sharpe under 1.0. Sector momentum's known failure is the same
as any momentum rule's: the reversal after a crash, and a leadership change
between rebalances. A test window that still clears the floor with one is
the result to trust.

## Result

On the Kaggle mirror (2005-02-25 to 2017-11-10, docs/DISCOVERY.md), train
2005–2013, test 2014-01-01 to 2017-11-10, next-open fill, 5 bp slippage per
side:

| window | Sharpe | CAGR | max drawdown | holding changes |
|---|---|---|---|---|
| train, lookback 126, top 3, monthly | 0.65 | | −34.5% | 133 over 9 years |
| train, lookback 252, top 3, monthly | 0.62 | | −28.6% | 95 over 9 years |
| **test** | **0.74** | 9.0% | −12.3% | 86 over 4 years |
| test, hold XLK / XLU / XLP / XLE / GLD | 1.24 / 1.01 / 0.85 / −0.05 / 0.11 | | | |
| test at 10 bp / 20 bp slippage | 0.71 / 0.63 | | | |

Not promoted: out-of-sample Sharpe 0.74. Holding three sectors beat holding
two on every lookback in the training window, and the rule sidestepped
XLE's 2014–2016 collapse, which is the relative half doing what the
`dual_momentum` write-up said it could not do on four near-identical ETFs;
the drawdown is the smallest of any study on this window. What it did not
do is keep up with XLK in a market led by one sector. Run it from the
Research tab with the training cutoff at 2019-12-31 for the 2020–2026
answer.
