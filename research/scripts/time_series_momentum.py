"""Time-series momentum over a small ETF universe, with GLD as the haven.

    uv run python scripts/time_series_momentum.py                 # fetch from Yahoo
    uv run python scripts/time_series_momentum.py --csv-dir DIR   # Kaggle-format CSVs, no network
    uv run python scripts/time_series_momentum.py --universe SPY,QQQ,IWM,EFA,EEM,TLT,GLD

Every ``rebalance_days`` bars each risk asset is held if its own trailing
``lookback``-bar return is positive and rests in the haven otherwise
(docs/CONTRACTS.md, ``time_series_momentum``). The lookback and the rebalance
interval are chosen on the training window from a grid of eight; the test
window is touched once, under the next-open fill. The spec is promoted only
when the out-of-sample Sharpe clears 1.0 on Yahoo data. The sweep, the
out-of-sample numbers and the cost stress go under ``--out-dir``.
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
from tt.backtest.engine import Fill
from tt.stats.kelly import half_kelly
from tt.strategies import time_series_momentum as tsm

BASE = "time_series_momentum"
DEFAULT_UNIVERSE = ["SPY", "QQQ", "IWM", "EFA", "EEM", "TLT", "GLD"]
START = dt.date(2005, 1, 1)
FILL = Fill.NEXT_OPEN
# Three to twelve months of momentum, weekly or monthly turns: eight candidates.
LOOKBACKS = [63, 126, 189, 252]
REBALANCE = [5, 21]


def main() -> int:
    args = study.parse_args(__doc__ or "", default_universe=DEFAULT_UNIVERSE,
                            default_train_to=dt.date(2019, 12, 31))
    universe: list[str] = args.universe
    name = study.spec_name(BASE, universe, DEFAULT_UNIVERSE)
    data = study.load(args, universe, START)
    aligned = tsm.align(data.bars, universe)
    opens = study.opens_for(aligned, data.bars, universe)
    win = study.windows(aligned["date"], args.train_to)
    if win is None:
        return 2
    print(f"{name}: {data.source_note}; {len(aligned)} aligned bars; "
          f"train {win.train[0]}..{win.train[1]}, test {win.test[0]}..{win.test[1]}; fill {FILL.value}")

    train_bars = {s: study.slice_window(df, *win.train) for s, df in data.bars.items()}
    train_prices = tsm.align(train_bars, universe)
    train_opens = study.opens_for(train_prices, train_bars, universe)
    candidates = []
    for lb, rb in itertools.product(LOOKBACKS, REBALANCE):
        p = tsm.Params(lb, rb)
        tr = tsm.replay(train_bars, universe, p, 1.0)
        res = study.backtest_weights(train_prices, tsm.weights_frame(tr, universe), win.train[0],
                                     fill=FILL, opens=train_opens)
        candidates.append(study.Candidate({"lookback": lb, "rebalance_days": rb}, res, tsm.switches(tr, universe)))
    chosen = study.choose(candidates, prefer_larger=("lookback",))
    print("train, the grid:")
    for c in candidates:
        d = study.diagnostics(c.result)
        print(f"  lookback {int(c.params['lookback']):3d} every {int(c.params['rebalance_days']):2d}: "
              f"Sharpe {d['sharpe']:.2f}, max DD {d['max_drawdown']:.1%}, turnover {d['turnover']:.1f}x, "
              f"{c.switches} switches{'  <- chosen' if c is chosen else ''}")
    print("train buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(train_prices, universe, s, win.train[0], fill=FILL, opens=train_opens):.2f}"
        for s in universe))
    best = tsm.Params(int(chosen.params["lookback"]), int(chosen.params["rebalance_days"]))

    r = metrics.daily_returns(chosen.result.equity["equity"].to_numpy())
    var = float(r.var(ddof=1))
    gross = max(half_kelly(float(r.mean()), var, cap=1.0) if var > 0 else 1.0, 0.1)
    print(f"chosen lookback {best.lookback} every {best.rebalance_days}; half-Kelly leverage {gross:.3f}")

    tr = tsm.replay(data.bars, universe, best, gross)
    w = tsm.weights_frame(tr, universe)
    oos = study.backtest_weights(aligned, w, win.test[0], fill=FILL, opens=opens)
    keep = (pd.to_datetime(tr["date"]).dt.date >= win.test[0]).to_numpy()
    print(study.describe("test", oos, {"switches": tsm.switches(tr[keep].reset_index(drop=True), universe),
                                       "trades": len(oos.trades)}))
    print("test buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(aligned, universe, s, win.test[0], fill=FILL, opens=opens):.2f}" for s in universe))
    stress = study.cost_stress(aligned, w, win.test[0], fill=FILL, opens=opens)
    study.print_stress(stress)
    print("wrote", study.record(args, name, candidates=candidates, chosen=chosen, oos=oos, stress=stress))

    return study.emit(
        args=args, name=name, strategy=tsm.NAME, universe=universe,
        params={"lookback": best.lookback, "rebalance_days": best.rebalance_days},
        gross=gross, win=win, oos=oos,
        signals=tsm.signals(data.bars, universe, best, gross), data=data, script="time_series_momentum.py",
        fill=FILL, study=BASE,
    )


if __name__ == "__main__":
    raise SystemExit(main())
