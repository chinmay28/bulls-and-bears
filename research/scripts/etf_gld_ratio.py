"""The ETF/GLD ratio study: sweep on train, judge out of sample, emit a spec and goldens.

    uv run python scripts/etf_gld_ratio.py                 # fetch from Yahoo
    uv run python scripts/etf_gld_ratio.py --csv-dir DIR   # Kaggle-format CSVs, no network
    uv run python scripts/etf_gld_ratio.py --universe SPY,IAU   # another universe, another haven

The idea under test (docs/strategies/etf_gld_ratio.md): hold a broad equity
ETF while its price ratio to GLD is stretched below the ratio's own trailing
mean, and hold GLD otherwise. Parameters are chosen on the training window
alone, from a fixed grid, preferring configurations that trade about one to
two times a week across the four sleeves; the test window is touched once.

The spec is promoted to specs/etf_gld_ratio.yaml only when the out-of-sample
Sharpe clears the runtime's floor of 1.0 on data fetched from Yahoo;
otherwise it lands in research/out/ as a rejected spec. Goldens are written
either way: parity does not care whether the strategy is any good.
"""

from __future__ import annotations

import datetime as dt
import itertools
import sys
from dataclasses import dataclass
from pathlib import Path

import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt import study
from tt.backtest import metrics
from tt.backtest.engine import Fill, Result
from tt.stats.kelly import half_kelly
from tt.strategies import ratio

BASE = "etf_gld_ratio"
DEFAULT_UNIVERSE = ["SPY", "QQQ", "VTI", "XLK", "GLD"]
START = dt.date(2005, 1, 1)  # GLD listed 2004-11; VTI, XLK, QQQ, SPY are older
# Researched with the same-close loop (docs/CONTRACTS.md, Execution); a re-run
# under next_open is a separate change with its own goldens.
FILL = Fill.SAME_CLOSE
NO_TIME_STOP = 9999

# The grid is fixed before the data is looked at. Lookbacks span a fortnight
# to half a year; the bands are in z units; the time stop is either off or
# about three half-lives of a deviation from an L-bar mean (~0.4 L).
LOOKBACKS = [10, 20, 40, 60, 90, 120]
ENTRIES = [0.5, 1.0, 1.5, 2.0]
EXITS = [-0.5, 0.0, 0.5, 1.0]
# One to two trades a week: each sleeve switch is a sell and a buy, so 40..120
# switches a year across the four sleeves is 80..240 orders, 1.5..4.5 a week.
BAND = (40.0, 120.0)


@dataclass(frozen=True)
class Trial:
    """One configuration's showing on the training window."""

    params: ratio.Params
    sharpe: float
    switches_per_year: float
    result: Result


def grid() -> list[ratio.Params]:
    out: list[ratio.Params] = []
    for lb, ez, xz, stop in itertools.product(LOOKBACKS, ENTRIES, EXITS, (False, True)):
        if not xz > -ez:
            continue
        hold_days = max(5, round(0.4 * lb)) if stop else NO_TIME_STOP
        out.append(ratio.Params(lb, ez, xz, hold_days))
    return out


def choose(trials: list[Trial]) -> tuple[Trial, str]:
    """The best train Sharpe inside the trade-frequency band, else the best overall."""
    lo, hi = BAND
    in_band = [t for t in trials if lo <= t.switches_per_year <= hi]
    if in_band:
        return max(in_band, key=lambda t: t.sharpe), f"best train Sharpe with {lo:.0f}..{hi:.0f} switches/yr"
    return max(trials, key=lambda t: t.sharpe), "best train Sharpe (nothing in the frequency band)"


def main() -> int:
    args = study.parse_args(__doc__ or "", default_universe=DEFAULT_UNIVERSE,
                            default_train_to=dt.date(2013, 12, 31))
    universe: list[str] = args.universe
    name = study.spec_name(BASE, universe, DEFAULT_UNIVERSE)
    data = study.load(args, universe, START)
    aligned = ratio.align(data.bars, universe)
    win = study.windows(aligned["date"], args.train_to)
    if win is None:
        return 2
    print(f"{name}: {data.source_note}; {len(aligned)} aligned bars; "
          f"train {win.train[0]}..{win.train[1]}, test {win.test[0]}..{win.test[1]}, costs {study.COSTS}")

    # Train: the fixed grid, judged on the training window alone.
    train_bars = {s: study.slice_window(df, *win.train) for s, df in data.bars.items()}
    train_prices = ratio.align(train_bars, universe)
    years = len(train_prices) / metrics.TRADING_DAYS
    trials = []
    for p in grid():
        tr = ratio.replay(train_bars, universe, p, 1.0)
        res = study.backtest_weights(train_prices, ratio.weights_frame(tr, universe), win.train[0])
        trials.append(Trial(p, float(study.summary(res)["sharpe"]), ratio.switches(tr, universe) / years, res))
    best, why = choose(trials)
    print(f"\ntrain: {len(trials)} configurations; top 8 by Sharpe:")
    for t in sorted(trials, key=lambda t: t.sharpe, reverse=True)[:8]:
        p = t.params
        print(f"  L={p.lookback:3d} entry={p.entry_z:.1f} exit={p.exit_z:+.1f} stop={p.max_hold_days:4d}: "
              f"Sharpe {t.sharpe:.2f}, switches/yr {t.switches_per_year:.0f}")
    print("train buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(train_prices, universe, s, win.train[0], fill=FILL):.2f}" for s in universe))
    print(f"share of configurations with train Sharpe >= 1.0: {sum(t.sharpe >= 1 for t in trials) / len(trials):.1%}")
    p = best.params
    print(f"\nchosen ({why}): L={p.lookback} entry={p.entry_z} exit={p.exit_z} max_hold={p.max_hold_days}; "
          f"train Sharpe {best.sharpe:.2f}, {best.switches_per_year:.0f} switches/yr")

    # Sizing: half-Kelly from the training returns, capped at 1.0 because the
    # book is long-only and unlevered by construction.
    r = metrics.daily_returns(best.result.equity["equity"].to_numpy())
    var = float(r.var(ddof=1))
    gross = max(half_kelly(float(r.mean()), var, cap=1.0) if var > 0 else 1.0, 0.1)
    print(f"half-Kelly leverage {gross:.3f}")

    # Test: replay over the whole history so the sleeves are warm; the book
    # opens on the first test bar. This is the one look at the test window.
    tr = ratio.replay(data.bars, universe, p, gross)
    oos = study.backtest_weights(aligned, ratio.weights_frame(tr, universe), win.test[0])
    keep = (pd.to_datetime(tr["date"]).dt.date >= win.test[0]).to_numpy()
    print(study.describe("test", oos, {"switches": ratio.switches(tr[keep].reset_index(drop=True), universe)}))
    print("test buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(aligned, universe, s, win.test[0], fill=FILL):.2f}" for s in universe))

    return study.emit(
        args=args, name=name, strategy=ratio.NAME, universe=universe,
        params={"lookback": p.lookback, "entry_z": p.entry_z, "exit_z": p.exit_z,
                "max_hold_days": p.max_hold_days},
        gross=gross, win=win, oos=oos,
        signals=ratio.signals(data.bars, universe, p, gross), data=data, script="etf_gld_ratio.py",
        fill=FILL, study="etf_gld_ratio",
    )


if __name__ == "__main__":
    raise SystemExit(main())
