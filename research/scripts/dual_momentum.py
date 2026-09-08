"""Antonacci's dual momentum over a small ETF universe, with GLD as the haven.

    uv run python scripts/dual_momentum.py                 # fetch from Yahoo
    uv run python scripts/dual_momentum.py --csv-dir DIR   # Kaggle-format CSVs, no network
    uv run python scripts/dual_momentum.py --universe SPY,EFA,AGG,BIL

Every ``rebalance_days`` bars the risk assets are ranked by trailing return;
the top ``top_k`` that beat the haven's own trailing return are held, the rest
of the book sits in the haven (docs/CONTRACTS.md, ``dual_momentum``). The
lookback, the number held and the rebalance interval are chosen on the
training window from a fixed grid; the test window is touched once. The spec
is promoted only when the out-of-sample Sharpe clears 1.0 on Yahoo data.
"""

from __future__ import annotations

import datetime as dt
import itertools
import sys
from pathlib import Path

import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt import study
from tt.backtest import metrics
from tt.stats.kelly import half_kelly
from tt.strategies import momentum

BASE = "dual_momentum"
DEFAULT_UNIVERSE = ["SPY", "QQQ", "VTI", "XLK", "GLD"]
START = dt.date(2005, 1, 1)
# Three to twelve months of momentum, one or two holdings, weekly or monthly turns.
LOOKBACKS = [63, 126, 189, 252]
TOP_K = [1, 2]
REBALANCE = [5, 21]


def main() -> int:
    args = study.parse_args(__doc__ or "", default_universe=DEFAULT_UNIVERSE,
                            default_train_to=dt.date(2019, 12, 31))
    universe: list[str] = args.universe
    name = study.spec_name(BASE, universe, DEFAULT_UNIVERSE)
    data = study.load(args, universe, START)
    aligned = momentum.align(data.bars, universe)
    win = study.windows(aligned["date"], args.train_to)
    if win is None:
        return 2
    print(f"{name}: {data.source_note}; {len(aligned)} aligned bars; "
          f"train {win.train[0]}..{win.train[1]}, test {win.test[0]}..{win.test[1]}")

    train_bars = {s: study.slice_window(df, *win.train) for s, df in data.bars.items()}
    train_prices = momentum.align(train_bars, universe)
    k = len(universe) - 1
    rows = []
    for lb, top, reb in itertools.product(LOOKBACKS, [t for t in TOP_K if t <= k], REBALANCE):
        p = momentum.Params(lb, top, reb)
        tr = momentum.replay(train_bars, universe, p, 1.0)
        res = study.backtest_weights(train_prices, momentum.weights_frame(tr, universe), win.train[0])
        m = study.summary(res)
        rows.append((p, float(m["sharpe"]), momentum.switches(tr, universe), res))
    rows.sort(key=lambda r: r[1], reverse=True)
    print("train, top 6 of the grid:")
    for p, sh, sw, _ in rows[:6]:
        print(f"  lookback {p.lookback:3d} top {p.top_k} every {p.rebalance_days:2d}: "
              f"Sharpe {sh:.2f}, {sw} changes")
    print("train buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(train_prices, universe, s, win.train[0]):.2f}" for s in universe))
    best, _, _, best_res = rows[0]

    r = metrics.daily_returns(best_res.equity["equity"].to_numpy())
    var = float(r.var(ddof=1))
    gross = max(half_kelly(float(r.mean()), var, cap=1.0) if var > 0 else 1.0, 0.1)
    print(f"chosen lookback {best.lookback} top {best.top_k} every {best.rebalance_days}; "
          f"half-Kelly leverage {gross:.3f}")

    tr = momentum.replay(data.bars, universe, best, gross)
    oos = study.backtest_weights(aligned, momentum.weights_frame(tr, universe), win.test[0])
    keep = (pd.to_datetime(tr["date"]).dt.date >= win.test[0]).to_numpy()
    print(study.describe("test", oos, {"changes": momentum.switches(tr[keep].reset_index(drop=True), universe)}))
    print("test buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(aligned, universe, s, win.test[0]):.2f}" for s in universe))

    return study.emit(
        args=args, name=name, strategy=momentum.NAME, universe=universe,
        params={"lookback": best.lookback, "top_k": best.top_k, "rebalance_days": best.rebalance_days},
        gross=gross, win=win, oos=oos,
        signals=momentum.signals(data.bars, universe, best, gross), data=data, script="dual_momentum.py",
    )


if __name__ == "__main__":
    raise SystemExit(main())
