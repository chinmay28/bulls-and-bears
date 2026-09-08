"""Connors' RSI(2) pullback over a few equity ETFs, with GLD as the haven.

    uv run python scripts/rsi2_reversion.py                 # fetch from Yahoo
    uv run python scripts/rsi2_reversion.py --csv-dir DIR   # Kaggle-format CSVs, no network
    uv run python scripts/rsi2_reversion.py --universe SPY,QQQ,IWM,GLD

Each risk asset is bought when it closes above its ``trend_lookback``-bar
average with a two-bar RSI under ``rsi_entry``, and sold back for the haven
when the RSI rises over ``rsi_exit``, the ``max_hold_days`` time stop hits,
or the close falls under the average (docs/CONTRACTS.md, ``rsi2_reversion``).
The thresholds and the time stop are chosen on the training window from a
grid of eighteen; the trend window and the RSI period are fixed. The test
window is touched once, under the next-open fill, and reported at 5, 10 and
20 bp of slippage because a rule that holds for days lives on costs. The
spec is promoted only when the out-of-sample Sharpe clears 1.0 on Yahoo
data at the base cost. The sweep, the out-of-sample numbers and the cost
stress go under ``--out-dir``.
"""

from __future__ import annotations

import datetime as dt
import itertools
import statistics
import sys
from pathlib import Path

import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt import study
from tt.backtest import metrics
from tt.backtest.engine import Fill
from tt.stats.kelly import half_kelly
from tt.strategies import rsi2

BASE = "rsi2_reversion"
DEFAULT_UNIVERSE = ["SPY", "QQQ", "IWM", "GLD"]
START = dt.date(2005, 1, 1)
FILL = Fill.NEXT_OPEN
TREND = 200
# Connors' own thresholds and their neighbours: eighteen candidates.
ENTRY = [2, 5, 10]
EXIT = [60, 70, 80]
HOLD = [3, 5]


def main() -> int:
    args = study.parse_args(__doc__ or "", default_universe=DEFAULT_UNIVERSE,
                            default_train_to=dt.date(2019, 12, 31))
    universe: list[str] = args.universe
    name = study.spec_name(BASE, universe, DEFAULT_UNIVERSE)
    data = study.load(args, universe, START)
    aligned = rsi2.align(data.bars, universe)
    opens = study.opens_for(aligned, data.bars, universe)
    win = study.windows(aligned["date"], args.train_to)
    if win is None:
        return 2
    print(f"{name}: {data.source_note}; {len(aligned)} aligned bars; "
          f"train {win.train[0]}..{win.train[1]}, test {win.test[0]}..{win.test[1]}; fill {FILL.value}")

    train_bars = {s: study.slice_window(df, *win.train) for s, df in data.bars.items()}
    train_prices = rsi2.align(train_bars, universe)
    train_opens = study.opens_for(train_prices, train_bars, universe)
    candidates = []
    for entry, exit_, hold in itertools.product(ENTRY, EXIT, HOLD):
        p = rsi2.Params(TREND, entry, exit_, hold)
        tr = rsi2.replay(train_bars, universe, p, 1.0)
        res = study.backtest_weights(train_prices, rsi2.weights_frame(tr, universe), win.train[0],
                                     fill=FILL, opens=train_opens)
        candidates.append(study.Candidate(
            {"trend_lookback": TREND, "rsi_entry": entry, "rsi_exit": exit_, "max_hold_days": hold}, res,
            rsi2.switches(tr, universe)))
    chosen = study.choose(candidates)
    print("train, the grid:")
    for c in candidates:
        d = study.diagnostics(c.result)
        print(f"  entry {int(c.params['rsi_entry']):2d} exit {int(c.params['rsi_exit']):2d} "
              f"hold {int(c.params['max_hold_days'])}: Sharpe {d['sharpe']:.2f}, max DD {d['max_drawdown']:.1%}, "
              f"turnover {d['turnover']:.1f}x, {c.switches} switches{'  <- chosen' if c is chosen else ''}")
    print("train buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(train_prices, universe, s, win.train[0], fill=FILL, opens=train_opens):.2f}"
        for s in universe))
    best = rsi2.Params(TREND, float(chosen.params["rsi_entry"]), float(chosen.params["rsi_exit"]),
                       int(chosen.params["max_hold_days"]))

    r = metrics.daily_returns(chosen.result.equity["equity"].to_numpy())
    var = float(r.var(ddof=1))
    gross = max(half_kelly(float(r.mean()), var, cap=1.0) if var > 0 else 1.0, 0.1)
    print(f"chosen entry {best.rsi_entry:g} exit {best.rsi_exit:g} hold {best.max_hold_days}; "
          f"half-Kelly leverage {gross:.3f}")

    tr = rsi2.replay(data.bars, universe, best, gross)
    w = rsi2.weights_frame(tr, universe)
    oos = study.backtest_weights(aligned, w, win.test[0], fill=FILL, opens=opens)
    keep = (pd.to_datetime(tr["date"]).dt.date >= win.test[0]).to_numpy()
    test_tr = tr[keep].reset_index(drop=True)
    holds = rsi2.holding_periods(test_tr, universe)
    years = len(test_tr) / metrics.TRADING_DAYS
    extra = {
        "round_trips": len(holds), "round_trips_per_year": len(holds) / years if years else float("nan"),
        "mean_hold_bars": statistics.fmean(holds) if holds else float("nan"),
        "median_hold_bars": statistics.median(holds) if holds else float("nan"),
    }
    print(study.describe("test", oos, {"round trips": len(holds),
                                       "mean hold": f"{extra['mean_hold_bars']:.1f} bars"}))
    print("test buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(aligned, universe, s, win.test[0], fill=FILL, opens=opens):.2f}" for s in universe))
    stress = study.cost_stress(aligned, w, win.test[0], fill=FILL, opens=opens)
    study.print_stress(stress)
    print("wrote", study.record(args, name, candidates=candidates, chosen=chosen, oos=oos, stress=stress,
                                extra=extra))

    return study.emit(
        args=args, name=name, strategy=rsi2.NAME, universe=universe,
        params={"trend_lookback": best.trend_lookback, "rsi_entry": best.rsi_entry, "rsi_exit": best.rsi_exit,
                "max_hold_days": best.max_hold_days},
        gross=gross, win=win, oos=oos,
        signals=rsi2.signals(data.bars, universe, best, gross), data=data, script="rsi2_reversion.py",
        fill=FILL, study=BASE,
    )


if __name__ == "__main__":
    raise SystemExit(main())
