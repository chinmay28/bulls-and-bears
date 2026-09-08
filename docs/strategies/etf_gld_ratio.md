# etf_gld_ratio

The `docs/STRATEGY_TEMPLATE.md` entry for the ETF/GLD ratio idea, filled in by
`research/scripts/etf_gld_ratio.py`. Read the **Result** section first: on the
data available, the idea does not earn a spec.

## Name

`etf_gld_ratio`, strategy `ratio_reversion`, golden directory
`golden/etf_gld_ratio/`.

## Hypothesis

Broad equity ETFs priced in gold (SPY/GLD, QQQ/GLD, VTI/GLD, XLK/GLD) wander
around a slow-moving level. When a ratio has slipped well below its own
recent average, equities are cheap against gold and should be bought; when
it has recovered, the sleeve should sit in GLD. The counterparty is whoever
chases the last few weeks' relative move.

Where it comes from: the operator's idea, not a published strategy. The
published literature on this ratio is on the *other* side. Every tested
rotation between stocks and gold found in the search (QuantifiedStrategies'
S&P/gold 20-month SMA rotation, Faber's 10-month SMA timing, Allocate
Smartly's gold cross-asset momentum, CXO's weekly lumber/gold rule) is
trend-following at monthly horizons; CXO's GLD/GDX ratio study is the nearest
tested analogue of weekly ratio reversion and found nothing (R² ≈ 0). The
in-house statistics do support the contrarian sign at short horizons over
2005–2017 — variance ratios of the log ratio's changes are 0.87–0.90 at 5–20
days and 0.76–0.79 at 60 days, first-lag autocorrelation is −0.03 to −0.05,
and the deviation from a 40–60 day mean predicts the next 20 days' relative
move with a correlation of −0.08 — but that is a weak effect against 26–28%
annualized ratio volatility.

## Universe

`[SPY, QQQ, VTI, XLK, GLD]`: four broad or tech-heavy US equity ETFs, chosen by
the operator, and GLD as the haven. All five are large, liquid ETFs with
1–2 bp spreads, so Yahoo's survivorship bias does not apply. Note that the
four ratios are nearly one signal: the daily changes of the log ratios
correlate 0.95–0.99 with each other, so four sleeves add smoothing, not
diversification.

## Signal

`ratio_reversion`, docs/CONTRACTS.md. For each equity ETF, the z-score of the
log ratio to GLD against a trailing window of `lookback` bars (sample sd).
A sleeve enters the ETF when z ≤ −`entry_z`, leaves for GLD when z ≥ `exit_z`
or after `max_hold_days` bars, and is forced to GLD while z is undefined.
Weights: each of the k sleeves is G/k, in the ETF or in GLD; the book is
always fully invested and never short.

Parameters were chosen on the training window alone from a fixed grid of
180 configurations (lookback 10–120, entry 0.5–2.0, exit −0.5–1.0, time stop
off or ≈0.4·lookback), preferring configurations that switch a sleeve
40–120 times a year across the four sleeves — about one to two trades a
week, as asked. Chosen: `lookback 20, entry_z 2.0, exit_z 0.5,
max_hold_days 8`.

## Sizing

Half-Kelly from the training returns is 1.5 (full Kelly 3.0), capped at 1.0
because the book is long-only and unlevered by construction. `gross_leverage
1.0`, `max_notional_per_leg_usd 2000`.

## Exit

Reversion to `exit_z` above the trailing mean, or the time stop of
`max_hold_days` bars, whichever comes first. No stop-loss: the sleeve's
alternative is GLD, not cash, so the exit is a switch, not a liquidation.

## Invalidation

The runtime's floor: out-of-sample Sharpe under 1.0. A drawdown deeper than
GLD's own over the same window. A paper-vs-backtest gap over 2% of equity.
For this idea specifically: if the contrarian sign loses to buy-and-hold of
the equity sleeve after costs, the "average" the ratio reverts to is moving
faster than the rule can follow, and the idea is a trend idea wearing a
value idea's clothes.

## Result

Data: Kaggle "Huge Stock Market Dataset" mirror (adjusted daily closes),
2005-02-25 to 2017-11-10; see docs/DISCOVERY.md for why not Yahoo. Costs
5 bp slippage per side, no commission. Train 2005-02-25..2013-12-31, test
2014-01-01..2017-11-10. The test window was evaluated once, for the chosen
configuration.

| window | Sharpe | CAGR | max drawdown | sleeve switches / year |
|---|---|---|---|---|
| train, chosen configuration | 0.65 | 12.2% | −33.7% | 46 |
| **test, chosen configuration** | **0.26** | 2.8% | −23.1% | 42 |
| test, hold SPY / QQQ / VTI / XLK | 0.95 / 1.13 / 0.91 / 1.25 | | −13% to −16% | |
| test, hold GLD | 0.12 | | −24% | |

No configuration in the grid reached a train Sharpe of 1.0 (the best was
0.65; buy-and-hold GLD over the same window was 0.63). Year by year the
chosen rule's Sharpe was 0.92, 1.83, 0.20, 1.39, 2.10, −0.11, 1.20, −1.42,
0.32, −0.73, 0.15, 1.90 for 2006–2017: it did well while gold was the
stronger asset (2006–2010) and lost in the years equities ran (2013, 2015).

For the record, the mirror rule (hold the ETF while its ratio is *above* the
trailing mean) was swept on the same grid plus 200/250/500-day lookbacks. Its
best train Sharpe was 1.01 at a 20-day lookback, which fell to −0.10 out of
sample; the two-year lookback (500 bars, entry 1.0, exit 0.5) held up better,
0.97 in train and 0.72 out of sample, but switches about twice a year, not
twice a week.

**Verdict:** `research/out/etf_gld_ratio.rejected.yaml`, out-of-sample
Sharpe 0.26. The spec was not promoted and the runtime would refuse it. The
pipeline is complete — strategy in both languages, parity goldens, this
study — so the question is re-asked from the app: on the **Research** tab,
the ETF/GLD card's **Run study** fetches from Yahoo on the trading machine,
runs this same script with the training window ending on the date in the
card (2019-12-31 by default, so 2020 onward is the one look out of sample),
and installs the spec on Strategies only if it clears the floor. The log of
the run, promoted or not, stays on the tab. From a terminal the same thing
is:

```sh
make golden-ratio RATIO_FLAGS="--train-to 2019-12-31"
```

The 2018–2026 years are the ones this study could not see and the ones in
which gold outran equities hardest; a contrarian ratio rule would have been
buying equities into that, and the 2005–2017 result gives no reason to
expect a different verdict.

Research git SHA: in the rejected spec's `provenance`.
