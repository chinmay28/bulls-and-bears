# Where the strategies come from

The plan's first rule is *borrow, don't invent* (docs/PLAN.md §0). This is
the catalogue behind that rule: the strategies this toolkit has, the ones
its expansion plan schedules next, and the ones surveyed and set aside, each
with the book, paper, index or open-source project it was borrowed from and
one line on how it fits a runtime that trades US ETFs once a day on daily
bars, long-only until the broker's short support is confirmed, from a pure
function of the bars.

The survey behind this was done in September 2026 from search-index text of
the sources; where a parameter comes from a secondary summary rather than
the primary text it is marked *(verify)*. Nothing here is investment advice,
and every result quoted is the source's, not ours: our own numbers are in
the per-strategy write-ups.

## Implemented

| strategy | family | borrowed from | write-up |
|---|---|---|---|
| `pairs_zscore` | pairs / stat-arb | Chan, *Quantitative Trading* (2008), Ex. 3.6, 7.2, 7.5; Gatev, Goetzmann & Rouwenhorst, *Pairs Trading* (RFS 2006) | `docs/strategies/` (via `research/scripts/gld_gdx.py`) |
| `ratio_reversion` | cross-asset relative value | Chan (2013) ch. 3, Bollinger-band reversion on a ratio | `etf_gld_ratio.md` |
| `sma_trend` | trend following | Faber, *A Quantitative Approach to Tactical Asset Allocation* (JWM 2007); Bloomberg BTST's moving-average template | `sma_trend.md` |
| `dual_momentum` | relative + absolute momentum | Antonacci, *Dual Momentum Investing* (2014); Jegadeesh & Titman (JF 1993) | `dual_momentum.md`, `sector_rotation.md` |
| `time_series_momentum` | absolute momentum | Moskowitz, Ooi & Pedersen, *Time Series Momentum* (JFE 2012); Hurst, Ooi & Pedersen, *A Century of Evidence on Trend-Following Investing* (JPM 2017); Greyserman & Kaminski (2014) | `time_series_momentum.md` |
| `donchian_breakout` | breakout trend following | Donchian's four-week rule; Faith, *Way of the Turtle* (2007); Clenow, *Following the Trend* (2013); `pysystemtrade`'s breakout rule | `donchian_breakout.md` |
| `risk_parity_trend` | dynamic sizing | Asness, Frazzini & Pedersen, *Leverage Aversion and Risk Parity* (FAJ 2012); Harvey et al., *The Impact of Volatility Targeting* (JPM 2018); Bloomberg's risk-parity indices (equal risk from one-year exponentially weighted volatility) | `risk_parity_trend.md` |
| `rsi2_reversion` | short-horizon mean reversion | Connors & Alvarez, *Short Term Trading Strategies That Work* (2009) ch. 9, *High Probability ETF Trading* (2009); replicated in [handiko/RSI-2-Stock-Trading-Strategy](https://github.com/handiko/RSI-2-Stock-Trading-Strategy), QuantConnect's library, StockCharts' ChartSchool | `rsi2_reversion.md` |

Every one of these is a pure function of adjusted daily bars, long-only,
one order a day, and researched under the next-open fill from
`time_series_momentum` on (docs/CONTRACTS.md, Execution).

## Scheduled next (the expansion plan's remaining phases)

| strategy | family | borrowed from | what it needs first |
|---|---|---|---|
| `bollinger_reversion` | single-asset mean reversion | Chan (2013) ch. 3; Lento, Gradojevic & Wright (AFEL 2007) find the *contrarian* use of the bands is the one that works on indices | nothing: the z-score indicator exists; lower priority than the two below |
| `residual_zscore` | factor-neutral residual reversion | Avellaneda & Lee, *Statistical arbitrage in the US equities market* (QF 2010); Chan (2013) ch. 4 | short support confirmed by discovery; a stock's history from Yahoo is survivorship-biased and the write-up must say so |
| `johansen_basket` | multi-asset cointegration | Chan (2013) ch. 4 index arbitrage; Johansen (1991) | `residual_zscore`'s three-leg weight machinery; indexed scalar params (`beta_0` …) |
| `regime_switch` | regime-conditioned allocation | Daniel & Moskowitz, *Momentum Crashes* (JFE 2016) for the bear × variance gate | six validated base strategies, and strategy composition in the spec |
| `strategy_ensemble` | multi-strategy combination | fixed weights first; inverse strategy-volatility next (risk parity over strategies, Asness et al. 2012) | five base strategies with comparable out-of-sample histories |

## Surveyed and set aside

Grouped by why. "Fits" means it could be built from the bars alone;
"set aside" means it was judged a variant of something already here, or
outside the runtime's constraints.

### Variants of what exists (indicator diversity, not strategy diversity)

- **EWMAC** (Carver, *Systematic Trading*, 2015; `pysystemtrade` and
  [systematictradingexamples](https://github.com/robcarver17/systematictradingexamples/blob/master/tradingrules.py)):
  the fast-minus-slow EMA divided by price volatility, capped and combined
  across speeds. A better-engineered `sma_trend`; its forecast-scaling
  framework is what an ensemble would borrow, not a new return driver.
- **Lempérière et al., *Two centuries of trend following*** (JIS 2014):
  the sign of price minus a five-month EMA over the vol. A one-parameter
  `time_series_momentum`.
- **Baltas & Kosowski** (2012/2020): time-series momentum with a t-statistic
  trend signal and Yang-Zhang OHLC volatility. A refinement to try inside
  `time_series_momentum` once it has a live record.
- **Faber's GTAA-13 Aggressive** (SSRN 1585517), **Keller's PAA / VAA /
  DAA / BAA / HAA** (SSRN 2759734, 3002624, 3212862, 4166845, 4346906;
  replicated in [oronimbus/tactical-asset-allocation](https://github.com/oronimbus/tactical-asset-allocation),
  [ldwhite/TacticalAssetAllocation](https://github.com/ldwhite/TacticalAssetAllocation),
  [dxcv/TYCo](https://github.com/dxcv/TYCo)) and **Antonacci's GEM**: each is
  `dual_momentum` with a different score (13612W, 13612U, an SMA ratio), a
  canary asset, or a breadth-driven cash fraction. The right way in is a
  `score` param and a `canary` on the existing strategy, not five packages.
- **George & Hwang, *The 52-Week High and Momentum Investing*** (JF 2004):
  rank on price over the 52-week high. An alternative momentum score for
  the same rotation engine.
- **Clenow, *Stocks on the Move*** (2015): a regression-slope × R²
  momentum score with an ATR sizing. Same engine, different score, on a
  stock universe with survivorship problems.
- **Connors' cumulative RSI, Double 7s, RSI-25/75, R3, %b, MDU, 3-day
  high/low, TPS** (2009): each is `rsi2_reversion` with a different
  trigger. `Double 7s` (buy a 7-day closing low above the 200-day average,
  sell a 7-day closing high) is the one worth a param on the existing
  strategy if the RSI version earns a live record.
- **MACD, Williams %R, stochastic, CCI, Parabolic SAR, DMI/ADX, Ichimoku**:
  the technical templates Bloomberg's BTST ranks by historical return
  (MACD placed fourth on the growth/value ratio in Bloomberg's own note),
  and the ones the expansion plan explicitly declines to make first-class
  strategies. Indicator primitives if a contract ever needs them.

### Fits, but needs something the runtime does not have yet

- **Turn-of-the-month** (Lakonishok & Smidt, RFS 1988; McConnell & Xu, FAJ
  2008: hold equities from the close of the second-to-last trading day of
  the month through the third trading day of the next) and **Dash for
  Cash** (Etula, Rinne, Suominen & Vaittinen, RFS 2020: out from T−9 to
  T−4, in from T−4 to T+3). Two orders a month, closes only, but "the
  last trading day" needs the exchange calendar ahead of time, which the
  strategy interface deliberately does not get. A calendar-day entry
  (first bar on or after the 25th) is a pure function of the bars and a
  fair approximation; a candidate once the runtime has an open session.
- **Sell in May** (Bouman & Jacobsen, AER 2002): long from the last
  October close to the last April close. Needs only the bar's month; two
  trades a year; too few trades for the out-of-sample floor to mean
  anything on a four-year test window.
- **Volatility-managed portfolios** (Moreira & Muir, JF 2017: exposure
  ∝ 1 / last month's realised variance) and **volatility targeting**
  (Harvey et al. 2018): overlays rather than strategies. `risk_parity_trend`
  carries the inverse-volatility idea; a target-volatility overlay on any
  sleeve is the ensemble's job.
- **Kalman-filter pairs** (Chan 2013, Ex. 3.3, EWA/EWC): a dynamic hedge
  ratio for `pairs_zscore`. The right fix for a stale `hedge_ratio` param
  when the pairs study is revisited.
- **Cross-sectional reversal on a fixed ETF basket** (Khandani & Lo 2007;
  Lehmann, QJE 1990; Jegadeesh, JF 1990): buy the k worst one-day (or
  one-week) returns among the sector SPDRs. Long-only version fits; daily
  turnover makes it a cost model with a strategy attached.
- **Pair selection by the distance method** (Gatev et al. 2006) over a
  30–50 ETF universe: a research-side way to choose pairs for the existing
  engine, not a runtime strategy.

### Does not fit the constraints

- **Buy-on-gap** (Chan 2013 ch. 4) and **overnight returns** (Cooper, Cliff
  & Gulen 2008; Lou, Polk & Skouras, JFE 2019: the equity premium is earned
  close-to-open): need an order at the open and another at the close.
- **Pre-FOMC drift** (Lucca & Moench, JF 2015): needs the FOMC calendar and
  an intraday exit, and Kurov et al. (2021) find it gone after 2011.
- **Cross-sectional stock momentum and value** (Jegadeesh & Titman 1993;
  Asness, Moskowitz & Pedersen, *Value and Momentum Everywhere*, JF 2013)
  on a stock universe: point-in-time constituents, which Yahoo does not
  keep. On a fixed ETF list they collapse into the rotation engine.

## Sources by kind

**GitHub projects.** [QuantConnect's strategy library](https://www.quantconnect.com/learning/articles/investment-strategy-library)
and its [Quantpedia tutorials](https://www.quantconnect.com/tutorials/strategy-library/quantpedia)
(asset-class momentum, short-term reversal, pairs by cointegration, and the
seasonality rules); [pysystemtrade](https://github.com/robcarver17/pysystemtrade)
(EWMAC and breakout as trading rules, forecast scaling, vol targeting);
[handiko/RSI-2-Stock-Trading-Strategy](https://github.com/handiko/RSI-2-Stock-Trading-Strategy)
and the ChartSchool / QuantifiedStrategies write-ups of RSI(2);
[oronimbus/tactical-asset-allocation](https://github.com/oronimbus/tactical-asset-allocation),
[ldwhite/TacticalAssetAllocation](https://github.com/ldwhite/TacticalAssetAllocation)
and [dxcv/TYCo](https://github.com/dxcv/TYCo) for the Keller and Antonacci
allocation rules; [burakbayramli/books](https://github.com/burakbayramli/books)
for Chan's MATLAB listings; [masaok/kaggle-boris-huge-stock-market-dataset](https://github.com/masaok/kaggle-boris-huge-stock-market-dataset)
for the data every study here was first run on. None of their code is
vendored: the contracts are written from the rules, and both
implementations from the contracts.

**Bloomberg.** The terminal's `BTST` ranks its template technical
strategies (moving averages, MACD, RSI, Bollinger bands, and the rest) by
historical return and risk for any security, and `BT` backtests one; both
are the reason the plan wants a research comparison view *after* the
strategy families exist rather than before. Bloomberg's own systematic
indices document the rules this catalogue borrows: the risk-parity indices
weight their constituents for equal risk measured as one-year daily
exponentially weighted volatility, and the Bloomberg Systematic Strategies
family publishes alternative-risk-premia benchmarks (trend, carry, value)
whose methodology papers are public. Bloomberg's insight note on growth
versus value found an exponential moving average the best of its
templates at catching that ratio's reversals — which is `ratio_reversion`'s
premise with a trend rule instead of a band.

**Books.** Chan, *Quantitative Trading* (2008) and *Algorithmic Trading*
(2013); Connors & Alvarez, *Short Term Trading Strategies That Work* (2009)
and *High Probability ETF Trading* (2009); Carver, *Systematic Trading*
(2015); Clenow, *Following the Trend* (2013) and *Stocks on the Move*
(2015); Faber, *The Ivy Portfolio* (2009); Faith, *Way of the Turtle*
(2007); Antonacci, *Dual Momentum Investing* (2014); Greyserman &
Kaminski, *Trend Following with Managed Futures* (2014); Zakamulin,
*Market Timing with Moving Averages* (2017), whose finding — no
moving-average rule beats buy-and-hold out of sample with significance,
the benefit is drawdown — is the expectation every trend write-up here is
read against.

**Papers.** Cited inline above; the ones whose rules are implemented are
Moskowitz, Ooi & Pedersen (2012), Hurst, Ooi & Pedersen (2017), Asness,
Frazzini & Pedersen (2012), Harvey et al. (2018), Faber (2007), Gatev et
al. (2006), Moskowitz & Grinblatt (1999).
