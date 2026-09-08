# sma_trend

The `docs/STRATEGY_TEMPLATE.md` entry for Faber's moving-average timing rule,
filled in by `research/scripts/sma_trend.py`.

## Name

`sma_trend`, strategy `sma_trend`, golden directory `golden/sma_trend/`. A
run on another universe names its spec `sma_trend_<symbols>`.

## Hypothesis

Borrowed, not invented: Faber, *A Quantitative Approach to Tactical Asset
Allocation* (J. Wealth Management, 2007). An asset held only while its price
is above its 10-month moving average keeps most of the return of holding it
outright and misses the worst of its drawdowns, across asset classes and
across a century of US equities; CXO Advisory's replications found the
result stable across moving-average lengths. The counterparty is whoever
holds through a bear market. The rule is applied one sleeve per ETF, with
GLD rather than cash as the resting place, because that is the pairing the
operator asked about; the haven is a field on the Research tab.

## Universe

`[SPY, QQQ, VTI, XLK, GLD]` by default: four broad or tech-heavy US equity
ETFs and gold as the haven. Any risk assets and any haven; large, liquid
ETFs, so Yahoo's survivorship bias does not apply.

## Signal

docs/CONTRACTS.md, `sma_trend`. For each risk asset, the trailing
`lookback`-bar average of its adjusted close from a running sum. The sleeve
enters when the price closes strictly above the average times `1 + band`,
leaves for the haven when it closes strictly below the average times
`1 − band`, and rests in the haven while the average is undefined. Each of
the k sleeves is G/k, in its ETF or in the haven.

The grid is lookbacks of 50, 100, 150, 200 and 250 bars and bands of 0, 1,
2 and 3 percent, chosen by training-window Sharpe.

## Sizing

Half-Kelly from the training returns, capped at 1.0: long-only, unlevered.
`max_notional_per_leg_usd 2000`.

## Exit

The cross back below the band. No time stop: a trend rule's exit is the
trend ending.

## Invalidation

Out-of-sample Sharpe under 1.0. A drawdown deeper than the haven's own over
the same window. Faber's rule lives on avoiding long bear markets; a test
window without one says little either way.

## Result

On the Kaggle mirror (2005-02-25 to 2017-11-10, docs/DISCOVERY.md), train
2005–2013, test 2014-01-01 to 2017-11-10, 5 bp slippage per side:

| window | Sharpe | CAGR | max drawdown | sleeve switches |
|---|---|---|---|---|
| train, lookback 200, band 2% | 0.62 | | | 64 over 9 years |
| **test** | **1.12** | 13.4% | −14.7% | 22 over 4 years |
| test, hold SPY / QQQ / VTI / XLK / GLD | 0.95 / 1.13 / 0.91 / 1.25 / 0.12 | | | |

The test window cleared the floor, but on data that is not Yahoo's the
script does not promote, and one four-year window in a bull market is not a
verdict: the rule's edge is in the years it sits out, and 2014–2017 had none
to sit out. Run it from the Research tab with the training cutoff at
2019-12-31 for the 2020–2026 answer.
