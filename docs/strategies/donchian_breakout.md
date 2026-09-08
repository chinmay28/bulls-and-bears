# donchian_breakout

The `docs/STRATEGY_TEMPLATE.md` entry for the Donchian channel breakout,
filled in by `research/scripts/donchian_breakout.py`.

## Name

`donchian_breakout`, strategy `donchian_breakout`, golden directory
`golden/donchian_breakout/`. A run on another universe names its spec
`donchian_breakout_<symbols>`.

## Hypothesis

Borrowed, not invented: Richard Donchian's four-week rule (1960s), the
Turtle trading rules as Curtis Faith published them (*Way of the Turtle*,
2007: enter on a 20-day breakout, exit on a 10-day low; or 55/20), and
Clenow's *Following the Trend* (2013), whose core model enters on a 50-day
high with a trend filter and exits on a trailing stop. Carver's
`pysystemtrade` carries the same idea as its `breakout` rule. A close above
every high of the recent past says a trend has started that the moving
average will only confirm later; the exit on a close below the recent lows
gives the trend room without a fixed stop. The counterparty is whoever
fades a new high. The rule is the same family as `sma_trend` but a
different trigger, and the first strategy here to read highs and lows,
adjusted so a split cannot fake a breakout.

## Universe

`[SPY, QQQ, IWM, EFA, EEM, TLT, GLD]` by default, the same seven as
`time_series_momentum` so the two trend rules can be compared. Large, liquid
ETFs, so Yahoo's survivorship bias does not apply. Any risk assets and any
haven.

## Signal

docs/CONTRACTS.md, `donchian_breakout`. For each risk asset, the highest
adjusted high of the prior `entry_lookback` bars and the lowest adjusted
low of the prior `exit_lookback` bars, never including the bar being
judged. The sleeve enters on a close strictly above the entry channel,
leaves for the haven on a close strictly below the exit channel, and rests
in the haven while the entry channel is undefined. Each held sleeve is
G/k; the haven takes the rest.

Five entry/exit pairs are tried, not a grid: 20/10 and 55/20 (the
Turtles'), 40/20, 100/50 and 126/63. Chosen by training-window Sharpe with
ties going to the smaller drawdown, the lower turnover, then the longer
entry window. Researched under the next-open fill.

## Sizing

Half-Kelly from the training returns, capped at 1.0: long-only, unlevered.
`max_notional_per_leg_usd 2000`.

## Exit

The close below the exit channel. No time stop: a breakout rule's exit is
the trailing low.

## Invalidation

Out-of-sample Sharpe under 1.0. A breakout rule pays for every false
breakout; a range-bound test window with a Sharpe near zero is the rule
working as designed and still not worth trading. A slippage stress that
turns the Sharpe negative at 20 bp says the turnover is the whole story.

## Result

On the Kaggle mirror (2005-02-25 to 2017-11-10, docs/DISCOVERY.md), train
2005–2013, test 2014-01-01 to 2017-11-10, next-open fill, 5 bp slippage per
side:

| window | Sharpe | CAGR | max drawdown | sleeve switches |
|---|---|---|---|---|
| train, entry 55 / exit 20 | 0.61 | | −28.6% | 242 over 9 years |
| train, entry 126 / exit 63 | 0.55 | | −27.8% | 92 over 9 years |
| **test** | **0.06** | 0.1% | −18.2% | 118 over 4 years |
| test, hold SPY / QQQ / IWM / EFA / EEM / TLT / GLD | 0.95 / 1.13 / 0.56 / 0.35 / 0.38 / 0.67 / 0.11 | | | |
| test at 10 bp / 20 bp slippage | 0.01 / −0.11 | | | |

Not promoted: out-of-sample Sharpe 0.06. Every candidate was within 0.08
of the others on the training window — the neighbour table in
`robustness.json` says the choice of pair hardly matters — and none of
them had anything to do in 2014–2017 but pay for false breakouts in a
market that went up without ever making a 20-day low that stuck. That is
the invalidation clause's range-bound case, not a bug, and the same window
that rejected `time_series_momentum`. Run it from the Research tab with the
training cutoff at 2019-12-31 for the 2020–2026 answer.
