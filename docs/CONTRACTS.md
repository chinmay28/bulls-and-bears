# Cross-language contracts

The signal and the metrics are implemented twice — Python for research, Go
for the runtime — and checked against each other through golden files
(docs/PLAN.md §4.3, §4.4). Both implementations follow the definitions here,
to the letter. A change to any of them is a change to both languages and to
the goldens, in one commit.

## Bars

Parquet, one file per symbol, schema in docs/PLAN.md §4.1: `date` (date32),
`open`/`high`/`low`/`close`/`adjclose` (float64), `volume` (int64), `source`
(string), `fetched_at` (timestamp[us], UTC). Rows oldest first.

Alignment of two symbols is an inner join on `date`. Bars a symbol has that
the other lacks are dropped before anything is computed.

## Adjusted OHLC

`adjclose` is split- and dividend-adjusted; `open`, `high`, `low` and `close`
are raw. Anything that reads a high, a low or an open — a channel, a
next-open fill — uses the adjusted value, or a split reads as a crash. Per
bar:

- `f_t = adjclose_t / close_t` (`close_t > 0` by the invariants; a
  non-positive close is a data error, not a factor of zero)
- `adjopen_t = open_t · f_t`, `adjhigh_t = high_t · f_t`, `adjlow_t = low_t · f_t`

Both sides compute the factor first and multiply once, in that order, so
the values agree bit for bit. Python: `tt.data.adjust`; Go: `bars.AdjustFactor`,
`bars.AdjustedOpen`, `bars.AdjustedHigh`, `bars.AdjustedLow`. The stored bar
schema is unchanged.

## Indicators

The rolling calculations the strategies below share, defined once and
implemented in `research/tt/indicators` and `server/internal/indicator`
from this text. Every function is pure, takes one series oldest first, and
returns one value per bar; "undefined" is NaN in Python and a false
`defined` flag in Go, and a strategy treats an undefined value as "stay
flat". A window "of L ending at t" is the values at `t−L+1 … t`, inclusive.
`golden/indicators/` holds a synthetic series and every indicator's output
from the Python side; the Go tests reproduce them to `1e-9`.

- **Rolling mean, sample sd, z-score** of a series `x`: over the window of
  L ending at t; sd with ddof = 1; undefined for `t < L−1`. The z-score is
  `(x_t − mean_t) / sd_t`, undefined where the sd is exactly 0. (L ≥ 2.)
- **Rolling return** of a price `p` over L bars: `p_t / p_{t−L} − 1`,
  undefined for `t < L`. (L ≥ 1.)
- **SMA** of a price over L bars: `(C_t − C_{t−L}) / L` from the running sum
  `C` (`C_{−1} = 0`), undefined for `t < L−1`, evaluated in that order on
  both sides so a price exactly on its average reads the same.
- **Donchian channels** over L bars use the **prior** bars only:
  `upper_t = max(adjhigh_{t−L} … adjhigh_{t−1})`,
  `lower_t = min(adjlow_{t−L} … adjlow_{t−1})`, undefined for `t < L`.
  The current bar is never in its own channel.
- **Realised volatility** over L bars: the sample sd (ddof = 1) of the
  daily simple returns `r_u = p_u / p_{u−1} − 1` over the window of L
  returns ending at t (`u = t−L+1 … t`), undefined for `t < L`; not
  annualised. Zero is a defined value; a strategy that divides by it treats
  it as ineligible.
- **True range** at t ≥ 1: `max(adjhigh_t − adjlow_t, |adjhigh_t −
  adjclose_{t−1}|, |adjlow_t − adjclose_{t−1}|)`; undefined at t = 0.
  **ATR** over N: the plain mean of the first N true ranges (t = 1 … N) at
  t = N, then `(ATR_{t−1} · (N−1) + TR_t) / N`; undefined for t < N.
- **Wilder RSI** over N (N ≥ 1): `d_t = p_t − p_{t−1}`, `gain_t = max(d_t,
  0)`, `loss_t = max(−d_t, 0)`. At t = N the average gain and loss are the
  plain means of `gain_1 … gain_N` and `loss_1 … loss_N`; after that
  `avg_t = (avg_{t−1} · (N−1) + new_t) / N` for each. Then, in this order:
  if `avg_loss = 0` and `avg_gain > 0`, RSI = 100; if both are 0, RSI = 50;
  otherwise `RS = avg_gain / avg_loss` and `RSI = 100 − 100 / (1 + RS)`.
  Undefined for t < N.

## `pairs_zscore`

Universe is exactly two symbols, `[A, B]`, in spec order. Params:
`hedge_ratio` (h), `lookback` (L, integer ≥ 2), `entry_z`, `exit_z`
(0 ≤ exit_z < entry_z), `max_hold_days` (integer ≥ 1). Sizing:
`gross_leverage` (G).

All prices are `adjclose`. On the aligned series, for each date t:

- `spread_t = adjclose_A,t − h · adjclose_B,t`
- `mean_t`, `sd_t`: mean and **sample** standard deviation (ddof = 1) of the
  spread over the trailing window of L bars ending at and including t.
  Undefined (and the target is flat) while fewer than L bars exist, or when
  `sd_t == 0`.
- `z_t = (spread_t − mean_t) / sd_t`

State `s ∈ {−1, 0, +1}` is the spread position; `held` counts bars since
entry. Replayed from the first aligned bar, in order; the strategy derives its
state from history alone and ignores the broker's positions. Per bar, exits
are evaluated before entries and an exit and an entry never happen on the
same bar:

- `s = +1` (long spread): exit to 0 if `z_t ≥ −exit_z` or `held ≥ max_hold_days`;
  else `held += 1`.
- `s = −1` (short spread): exit to 0 if `z_t ≤ exit_z` or `held ≥ max_hold_days`;
  else `held += 1`.
- `s = 0`: enter `+1` if `z_t ≤ −entry_z`; enter `−1` if `z_t ≥ entry_z`;
  `held = 0` on entry.
- Undefined `z_t` forces `s = 0`.

Target weights (fraction of equity, signed) for the bar:

- `n_A = adjclose_A,t`, `n_B = h · adjclose_B,t`, `gross = n_A + n_B`
- `w_A = s · G · n_A / gross`
- `w_B = −s · G · n_B / gross`

so `|w_A| + |w_B| = G` when in a position, and both are 0 when flat.

Golden `expected_signals.csv` columns: `date` (YYYY-MM-DD), `symbol`,
`target_weight` (float, full precision). One row per symbol per aligned date,
including the flat ones. Tolerance for Go against Python: `1e-9` absolute.

## Metrics

From an equity curve `e_0 … e_n` (one value per trading day, oldest first):

- Daily returns `r_t = e_t / e_{t−1} − 1` for t ≥ 1.
- `Sharpe = mean(r) / sd(r) · √252`, sample sd (ddof = 1), rf = 0. Undefined
  (NaN in Python, an error in Go) with fewer than 2 returns or `sd = 0`.
- `MaxDrawdown = min_t (e_t / max_{s≤t} e_s − 1)`: a number ≤ 0.
- `MaxDrawdownDuration`: the longest run of consecutive days on which
  `e_t < max_{s≤t} e_s` — the most days spent below a previous high before
  it was exceeded, counted in bars. Zero for a curve that never dips.

Golden `equity_curve.csv` columns: `date`, `equity`. Golden `metrics.json`:
`{"sharpe": …, "max_drawdown": …, "max_drawdown_duration": …}`. Tolerance
`1e-6`.

## Backtest engine (Python and `bnb backtest`)

Both run the same daily loop over the aligned bars. The strategy is replayed
over the **whole** history it is given, so its state at the first traded bar
is what the runtime's would be; the book opens at the first bar on or after
the spec's `test_window.from`. Per bar, in this order, with `p` the bar's
`adjclose` per symbol and `w` the bar's target weights:

1. `equity = cash + Σ shares · p` (yesterday's shares at today's close)
2. `target_shares = w · equity / p`; `delta = target_shares − shares`
3. `traded = Σ |delta| · p`; `orders` = number of symbols with `delta ≠ 0`
4. `cost = traded · slippage_bps / 10 000 + commission_usd · orders`
5. `cash = cash − Σ delta · p − cost`; `shares = target_shares`
6. record `equity_after = cash + Σ shares · p` for the bar

Starting cash is 10,000. Fractional shares are assumed. The golden
`equity_curve.csv` is this `equity_after` series and the two engines agree
on it to `1e-6`.

## Execution

A spec's `execution` block says when the signal is read and when the order
fills. `signal_at` is always `close`: a strategy's targets for bar t are a
function of bars up to and including t. `fill_at` is one of:

- `same_close_legacy`: the loop above. The targets read at bar t's close
  are filled at that same `adjclose`. This is what the first four strategies
  were researched with and what the runtime's close-time run approximates
  (it reads the last quote a few minutes before the close, appends it as the
  day's bar, and trades at it). A spec **without** an `execution` block is
  `same_close_legacy`; every spec research writes now says which it is.
- `next_open`: the targets read at bar t's close are filled at bar t+1's
  `adjopen` (Adjusted OHLC above). A signal cannot trade at a price that was
  known when it was computed. Per bar, in this order, with `o` the bar's
  `adjopen` and `p` its `adjclose` per symbol:

  1. If a target `w` is pending (read at the previous bar's close):
     `equity = cash + Σ shares · o`; `target_shares = w · equity / o`;
     `delta = target_shares − shares`; `traded = Σ |delta| · o`; `orders`,
     `cost` as above; `cash = cash − Σ delta · o − cost`;
     `shares = target_shares`.
  2. record `equity_after = cash + Σ shares · p` for the bar
  3. the bar's own target `w_t` becomes the pending target.

  The first bar of the run has no pending target and does not trade; the
  last bar's target is never filled. The run opens at the first bar on or
  after the spec's `test_window.from` with nothing pending, so its first fill
  is at the second bar's open.

The golden `equity_curve.csv` is the `equity_after` series of whichever fill
the golden `spec.yaml` names; `golden/next_open/` is a synthetic fixture —
gaps, a split-like factor change, target flips, a no-op target, a final
unfilled signal — that holds the two `next_open` engines to `1e-6` on their
own.

## `ratio_reversion`

Universe is `[R_1 … R_k, H]` in spec order: one or more risk assets and,
last, the haven they rest in (GLD in the first spec). k ≥ 1, no symbol
twice. Params: `lookback` (L, integer ≥ 2), `entry_z` (> 0), `exit_z`
(> −entry_z), `max_hold_days` (integer ≥ 1). Sizing: `gross_leverage` (G).

All prices are `adjclose`. Alignment is the inner join of every symbol on
date. On the aligned series, for each risk asset i and date t:

- `x_i,t = ln(adjclose_i,t) − ln(adjclose_H,t)`, the log price ratio
- `mean_i,t`, `sd_i,t`: mean and **sample** standard deviation (ddof = 1) of
  `x_i` over the trailing window of L bars ending at and including t.
  Undefined while fewer than L bars exist, or when `sd_i,t == 0`.
- `z_i,t = (x_i,t − mean_i,t) / sd_i,t`

Each risk asset has its own sleeve with state `s_i ∈ {0, 1}`: 1 holds the
risk asset, 0 holds the haven; `held_i` counts bars since entry. Replayed
from the first aligned bar, in order, from history alone. Per bar the exit is
evaluated before the entry and the two never happen on the same bar:

- `s_i = 1`: exit to 0 if `z_i,t ≥ exit_z` or `held_i ≥ max_hold_days`;
  else `held_i += 1`.
- `s_i = 0`: enter 1 if `z_i,t ≤ −entry_z`; `held_i = 0` on entry.
- Undefined `z_i,t` forces `s_i = 0`.

The rule is long-only and contrarian: a risk asset is held while it is
cheap against the haven relative to its own recent history, and the haven
otherwise. There is no short side.

Target weights (fraction of equity) for the bar, with `flat` the number of
sleeves in state 0:

- `w_i = G / k` if `s_i = 1`, else 0
- `w_H = G · flat / k`

so the weights always sum to G and none is negative. Both sides evaluate
these as written, left to right, so the rounding agrees.

Golden `expected_signals.csv` columns are the same as for `pairs_zscore`:
`date`, `symbol`, `target_weight`, one row per symbol per aligned date,
including the haven. Tolerance for Go against Python: `1e-9` absolute.

## `sma_trend`

Universe is `[R_1 … R_k, H]` in spec order, k ≥ 1, no symbol twice: the risk
assets and, last, the haven. Params: `lookback` (L, integer ≥ 2), `band`
(b, 0 ≤ b < 1). Sizing: `gross_leverage` (G). Alignment is the inner join
of every symbol on date; all prices are `adjclose`.

For each risk asset i, with `C_i,t = Σ_{u≤t} adjclose_i,u` the cumulative
sum accumulated in date order from the first aligned bar (`C_i,−1 = 0`):

- `sma_i,t = (C_i,t − C_i,t−L) / L`, undefined while fewer than L bars exist.

Both sides compute the average from that running sum, in that order, so
they agree bit for bit: a price exactly on its average reads the same in
both languages.

Each risk asset has a sleeve with state `s_i ∈ {0, 1}`, replayed from the
first aligned bar. Per bar:

- `s_i = 1`: exit to 0 if `adjclose_i,t < sma_i,t · (1 − b)`.
- `s_i = 0`: enter 1 if `adjclose_i,t > sma_i,t · (1 + b)`.
- Undefined `sma_i,t` forces `s_i = 0`.

Both comparisons are strict: a price on the average, or inside the band,
leaves the sleeve where it is.

Weights, with `flat` the number of sleeves in state 0: `w_i = G / k` if
`s_i = 1`, else 0; `w_H = G · flat / k`. Golden columns and tolerance as for
`ratio_reversion`.

## `dual_momentum`

Universe is `[R_1 … R_k, H]` in spec order, k ≥ 1, no symbol twice.
Params: `lookback` (L, integer ≥ 1), `top_k` (integer, 1 ≤ top_k ≤ k),
`rebalance_days` (R, integer ≥ 1). Sizing: `gross_leverage` (G). Alignment
is the inner join of every symbol on date; all prices are `adjclose`.
Aligned bars are indexed `t = 0, 1, …` from the first.

- For every symbol x and `t ≥ L`: `r_x,t = adjclose_x,t / adjclose_x,t−L − 1`.
  Undefined for `t < L`.
- Bar t is a **rebalance bar** when `t ≥ L` and `(t − L) mod R = 0`.
- On a rebalance bar the held set becomes: the risk assets with
  `r_i,t > r_H,t`, ordered by `r_i,t` descending with ties in universe order
  (a stable sort), truncated to the first `top_k`. On any other bar the held
  set is unchanged. Before the first rebalance bar nothing is held.

Weights, with `n` the number held: `w_i = G / top_k` for a held risk asset,
0 otherwise; `w_H = G · (top_k − n) / top_k`. Golden columns and tolerance
as for `ratio_reversion`.

## `time_series_momentum`

Universe is `[R_1 … R_k, H]` in spec order, k ≥ 1, no symbol twice: the
risk assets and, last, the haven. Params: `lookback` (L, integer ≥ 1),
`rebalance_days` (R, integer ≥ 1). Sizing: `gross_leverage` (G). Alignment
is the inner join of every symbol on date; all prices are `adjclose`;
aligned bars are indexed `t = 0, 1, …`.

Each risk asset has a sleeve that asks only about its own trend
(Moskowitz, Ooi and Pedersen, 2012), never about the others or the haven:

- `r_i,t = adjclose_i,t / adjclose_i,t−L − 1` (Indicators, rolling return),
  undefined for `t < L`.
- Bar t is a **rebalance bar** when `t ≥ L` and `(t − L) mod R = 0`.
- On a rebalance bar `s_i = 1` if `r_i,t > 0`, else 0: a return of exactly
  zero, or an undefined one, rests in the haven. On any other bar `s_i` is
  unchanged. Before the first rebalance bar every sleeve is 0.

Weights, with `flat` the number of sleeves in state 0: `w_i = G / k` if
`s_i = 1`, else 0; `w_H = G · flat / k`. Golden columns and tolerance as for
`ratio_reversion`. Researched and backtested under `fill_at: next_open`.

## `donchian_breakout`

Universe is `[R_1 … R_k, H]` in spec order, k ≥ 1, no symbol twice.
Params: `entry_lookback` (E, integer ≥ 2), `exit_lookback` (X, integer ≥ 2,
X < E). Sizing: `gross_leverage` (G). Alignment is the inner join of every
symbol on date. The channels read the **adjusted** high and low (Adjusted
OHLC); the close is `adjclose`.

For each risk asset i and aligned bar t (Indicators, Donchian channels):

- `upper_i,t = max(adjhigh_i,t−E … adjhigh_i,t−1)`, undefined for `t < E`
- `lower_i,t = min(adjlow_i,t−X … adjlow_i,t−1)`, undefined for `t < X`

The current bar is never in its own channel. Each risk asset has a sleeve
with state `s_i ∈ {0, 1}`, replayed from the first aligned bar. Per bar the
exit is evaluated before the entry and the two never happen on the same bar:

- Undefined `upper_i,t` forces `s_i = 0`.
- `s_i = 1`: exit to 0 if `adjclose_i,t < lower_i,t`.
- `s_i = 0`: enter 1 if `adjclose_i,t > upper_i,t`.

Both comparisons are strict: a close exactly on a channel leaves the sleeve
where it is. Weights, with `flat` the number of sleeves in state 0:
`w_i = G / k` if `s_i = 1`, else 0; `w_H = G · flat / k`. Golden columns and
tolerance as for `ratio_reversion`. Researched under `fill_at: next_open`.

## `risk_parity_trend`

Universe is `[R_1 … R_k, H]` in spec order, k ≥ 1, no symbol twice.
Params: `trend_lookback` (T, integer ≥ 2), `vol_lookback` (V, integer ≥ 2),
`rebalance_days` (R, integer ≥ 1). Sizing: `gross_leverage` (G). Alignment
is the inner join of every symbol on date; all prices are `adjclose`;
aligned bars are indexed `t = 0, 1, …`.

The first strategy whose weights are not on/off: an eligible asset's share
of the book is set by its own volatility, so the quiet ones carry more of
it and the wild ones less (inverse-volatility "risk parity"), behind a
trend filter. For each risk asset i and bar t (Indicators):

- `sma_i,t`: the SMA over T bars, undefined for `t < T−1`.
- `σ_i,t`: the realised volatility over V returns, undefined for `t < V`.
- i is **eligible** at t when both are defined, `adjclose_i,t > sma_i,t`
  (strict) and `σ_i,t > 0`.

Bar t is a **rebalance bar** when `t ≥ W` and `(t − W) mod R = 0`, with
`W = max(T, V)`. On a rebalance bar, with `A` the eligible assets in
universe order and `n = |A|`:

- `raw_i = 1 / σ_i,t` for i in A; `sum_raw = Σ_A raw_i`, accumulated in
  universe order
- `active_budget = G · n / k`, evaluated as `(G · n) / k`
- `w_i = active_budget · raw_i / sum_raw`, evaluated as
  `(active_budget · raw_i) / sum_raw`, for i in A; `w_i = 0` otherwise
- `w_H = G · (k − n) / k`, evaluated as `(G · (k − n)) / k`

so each ineligible sleeve's share rests in the haven and the eligible
sleeves' shares are pooled and split by inverse volatility, never
negative, never levered. On any other bar every weight is unchanged from
the previous bar. Before the first rebalance bar `w_H = G` and every
`w_i = 0`. Both sides evaluate these in the stated order, so the rounding
agrees. Golden columns and tolerance as for `ratio_reversion`. Researched
under `fill_at: next_open`.

## `rsi2_reversion`

Universe is `[R_1 … R_k, H]` in spec order, k ≥ 1, no symbol twice.
Params: `trend_lookback` (T, integer ≥ 2), `rsi_entry` (0 ≤ rsi_entry),
`rsi_exit` (rsi_entry < rsi_exit ≤ 100), `max_hold_days` (integer ≥ 1).
The RSI period is **2** and is not a parameter. Sizing: `gross_leverage`
(G). Alignment is the inner join of every symbol on date; all prices are
`adjclose`.

Connors' short-horizon pullback: buy a dip in an uptrend, sell the bounce.
For each risk asset i and bar t (Indicators):

- `rsi_i,t`: Wilder RSI over 2, undefined for `t < 2`.
- `sma_i,t`: the SMA over T bars, undefined for `t < T−1`.

Each risk asset has a sleeve with state `s_i ∈ {0, 1}` and `held_i`, the
bars since entry. Replayed from the first aligned bar; per bar the exit is
evaluated before the entry and the two never happen on the same bar:

- Undefined `rsi_i,t` or `sma_i,t` forces `s_i = 0`.
- `s_i = 1`: exit to 0 if `rsi_i,t > rsi_exit`, or `held_i ≥ max_hold_days`,
  or `adjclose_i,t < sma_i,t`; else `held_i += 1`.
- `s_i = 0`: enter 1 if `adjclose_i,t > sma_i,t` and `rsi_i,t < rsi_entry`;
  `held_i = 0` on entry.

Every comparison is strict: an RSI exactly on a threshold, or a close
exactly on the average, leaves the sleeve where it is. Weights, with `flat`
the number of sleeves in state 0: `w_i = G / k` if `s_i = 1`, else 0;
`w_H = G · flat / k`. Golden columns and tolerance as for
`ratio_reversion`. Researched under `fill_at: next_open`, and reported with
a slippage stress, because a rule that holds for days lives or dies on
costs.
