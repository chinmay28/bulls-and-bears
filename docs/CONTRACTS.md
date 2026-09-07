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

Both run the same daily loop over the aligned bars: at each bar's close,
compute targets from history up to and including that bar, and rebalance to
them at that bar's `adjclose` with the spec's cost model applied to the
notional traded (`slippage_bps` of notional, plus `commission_usd` per
order). Returns accrue from the next bar. Starting equity is 10,000.
