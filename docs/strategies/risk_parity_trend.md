# risk_parity_trend

The `docs/STRATEGY_TEMPLATE.md` entry for the risk-parity trend rule, filled
in by `research/scripts/risk_parity_trend.py`.

## Name

`risk_parity_trend`, strategy `risk_parity_trend`, golden directory
`golden/risk_parity_trend/`. A run on another universe names its spec
`risk_parity_trend_<symbols>`.

## Hypothesis

Borrowed, not invented. The sizing is inverse-volatility "risk parity":
Asness, Frazzini and Pedersen, *Leverage Aversion and Risk Parity*
(Financial Analysts Journal, 2012) show that weighting assets by one over
their volatility, rather than by capital, earns a higher Sharpe than the
market portfolio because leverage-averse investors overpay for the
high-volatility assets; Bridgewater's All Weather is the same idea as a
product, and Bloomberg publishes risk-parity indices built on it. The
filter is Faber's (2007): an asset below its own long moving average is not
held at all. Harvey et al., *The Impact of Volatility Targeting* (J.
Portfolio Management, 2018) find that scaling exposure by recent volatility
cuts the left tail of equity and credit strategies and raises their Sharpe.
Together: hold what is trending, sized so each sleeve carries a similar
amount of risk. The counterparty is whoever holds the wildest asset at full
weight through its drawdown. This is the first strategy here whose weights
are continuous rather than on/off.

## Universe

`[SPY, QQQ, IWM, TLT, GLD]` by default: three US equity ETFs and long
Treasuries, with gold as the haven, chosen so the volatilities differ enough
for the sizing to matter. Large, liquid ETFs, so Yahoo's survivorship bias
does not apply. Any risk assets and any haven.

## Signal

docs/CONTRACTS.md, `risk_parity_trend`. Every `rebalance_days` bars the
risk assets closing strictly above their `trend_lookback`-bar average, with
a positive `vol_lookback`-bar realised volatility, are eligible. Their
sleeves (G/k each) are pooled and split in proportion to one over each
asset's volatility; every ineligible sleeve rests in the haven. Between
rebalances the weights stand.

The grid is trend windows of 126, 200 and 252 bars and volatility windows
of 21 and 63 bars, monthly rebalancing fixed — six candidates, chosen by
training-window Sharpe with ties going to the smaller drawdown, the lower
turnover, then the longer windows. Researched under the next-open fill.

## Sizing

Half-Kelly from the training returns, capped at 1.0: long-only, unlevered.
Within that, each eligible sleeve's share is set by its volatility. No
per-asset cap at the strategy level; `max_notional_per_leg_usd 2000` and
the gate's gross-leverage rule bound the book.

## Exit

The next rebalance at which the asset is no longer above its average.

## Invalidation

Out-of-sample Sharpe under 1.0. A window in which the low-volatility asset
(Treasuries) sells off together with equities is the sizing's known
failure (2022): the rule then carries its largest weight in the asset that
is falling. Weights that do not sum to G, go negative, or fail to change
between rebalances are bugs, not results.

## Result

On the Kaggle mirror (2005-02-25 to 2017-11-10, docs/DISCOVERY.md), train
2005–2013, test 2014-01-01 to 2017-11-10, next-open fill, 5 bp slippage per
side:

| window | Sharpe | CAGR | max drawdown | sleeve switches |
|---|---|---|---|---|
| train, trend 252, vol 63 | 0.68 | | −26.7% | 59 over 9 years |
| train, trend 126, vol 21 | 0.63 | | −32.6% | 87 over 9 years |
| **test** | **0.75** | 6.9% | −18.3% | 25 over 4 years |
| test, hold SPY / QQQ / IWM / TLT / GLD | 0.95 / 1.13 / 0.56 / 0.67 / 0.11 | | | |
| test at 10 bp / 20 bp slippage | 0.72 / 0.67 | | | |

Not promoted: out-of-sample Sharpe 0.75, the best of the three trend rules
on this window and the least sensitive to slippage (monthly turns, 25
switches in four years). Every candidate was within 0.05 on the training
window and the drawdown fell with the longer trend window, so the choice
is robust in the sense the neighbour table asks about; what it lacks on
2014–2017 is a bear market to sit out. Run it from the Research tab with
the training cutoff at 2019-12-31 for the 2020–2026 answer, which contains
both the reversal and the 2022 case the invalidation clause names.
