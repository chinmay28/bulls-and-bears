"""Donchian channel breakout over a small ETF universe, with GLD as the haven.

    uv run python scripts/donchian_breakout.py                 # fetch from Yahoo
    uv run python scripts/donchian_breakout.py --csv-dir DIR   # Kaggle-format CSVs, no network
    uv run python scripts/donchian_breakout.py --universe SPY,QQQ,IWM,EFA,EEM,TLT,GLD

Each risk asset is held from a close above the highest adjusted high of the
prior ``entry_lookback`` bars until a close below the lowest adjusted low of
the prior ``exit_lookback`` bars, and rests in the haven otherwise
(docs/CONTRACTS.md, ``donchian_breakout``). Five entry/exit pairs are tried
on the training window; the test window is touched once, under the next-open
fill. The spec is promoted only when the out-of-sample Sharpe clears 1.0 on
Yahoo data. The sweep, the out-of-sample numbers and the cost stress go
under ``--out-dir``.
"""

from __future__ import annotations

import datetime as dt
import sys
from pathlib import Path

import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt import study
from tt.backtest import metrics
from tt.backtest.engine import Fill
from tt.stats.kelly import half_kelly
from tt.strategies import donchian

BASE = "donchian_breakout"
DEFAULT_UNIVERSE = ["SPY", "QQQ", "IWM", "EFA", "EEM", "TLT", "GLD"]
START = dt.date(2005, 1, 1)
FILL = Fill.NEXT_OPEN
# The Turtles' 20/10 and 55/20, and slower pairs: five candidates, not a grid.
PAIRS = [(20, 10), (40, 20), (55, 20), (100, 50), (126, 63)]


def main() -> int:
    args = study.parse_args(__doc__ or "", default_universe=DEFAULT_UNIVERSE,
                            default_train_to=dt.date(2019, 12, 31))
    universe: list[str] = args.universe
    name = study.spec_name(BASE, universe, DEFAULT_UNIVERSE)
    data = study.load(args, universe, START)
    aligned = donchian.align(data.bars, universe)
    opens = study.opens_for(aligned, data.bars, universe)
    win = study.windows(aligned["date"], args.train_to)
    if win is None:
        return 2
    print(f"{name}: {data.source_note}; {len(aligned)} aligned bars; "
          f"train {win.train[0]}..{win.train[1]}, test {win.test[0]}..{win.test[1]}; fill {FILL.value}")

    train_bars = {s: study.slice_window(df, *win.train) for s, df in data.bars.items()}
    train_prices = donchian.align(train_bars, universe)
    train_opens = study.opens_for(train_prices, train_bars, universe)
    candidates = []
    for entry, exit_ in PAIRS:
        p = donchian.Params(entry, exit_)
        tr = donchian.replay(train_bars, universe, p, 1.0)
        res = study.backtest_weights(train_prices, donchian.weights_frame(tr, universe), win.train[0],
                                     fill=FILL, opens=train_opens)
        candidates.append(study.Candidate({"entry_lookback": entry, "exit_lookback": exit_}, res,
                                          donchian.switches(tr, universe)))
    chosen = study.choose(candidates, prefer_larger=("entry_lookback",))
    print("train, the candidates:")
    for c in candidates:
        d = study.diagnostics(c.result)
        print(f"  entry {int(c.params['entry_lookback']):3d} exit {int(c.params['exit_lookback']):3d}: "
              f"Sharpe {d['sharpe']:.2f}, max DD {d['max_drawdown']:.1%}, turnover {d['turnover']:.1f}x, "
              f"{c.switches} switches{'  <- chosen' if c is chosen else ''}")
    print("train buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(train_prices, universe, s, win.train[0], fill=FILL, opens=train_opens):.2f}"
        for s in universe))
    best = donchian.Params(int(chosen.params["entry_lookback"]), int(chosen.params["exit_lookback"]))

    r = metrics.daily_returns(chosen.result.equity["equity"].to_numpy())
    var = float(r.var(ddof=1))
    gross = max(half_kelly(float(r.mean()), var, cap=1.0) if var > 0 else 1.0, 0.1)
    print(f"chosen entry {best.entry_lookback} exit {best.exit_lookback}; half-Kelly leverage {gross:.3f}")

    tr = donchian.replay(data.bars, universe, best, gross)
    w = donchian.weights_frame(tr, universe)
    oos = study.backtest_weights(aligned, w, win.test[0], fill=FILL, opens=opens)
    keep = (pd.to_datetime(tr["date"]).dt.date >= win.test[0]).to_numpy()
    print(study.describe("test", oos, {"switches": donchian.switches(tr[keep].reset_index(drop=True), universe)}))
    print("test buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(aligned, universe, s, win.test[0], fill=FILL, opens=opens):.2f}" for s in universe))
    stress = study.cost_stress(aligned, w, win.test[0], fill=FILL, opens=opens)
    study.print_stress(stress)
    print("wrote", study.record(args, name, candidates=candidates, chosen=chosen, oos=oos, stress=stress))

    return study.emit(
        args=args, name=name, strategy=donchian.NAME, universe=universe,
        params={"entry_lookback": best.entry_lookback, "exit_lookback": best.exit_lookback},
        gross=gross, win=win, oos=oos,
        signals=donchian.signals(data.bars, universe, best, gross), data=data, script="donchian_breakout.py",
        fill=FILL, study=BASE,
    )


if __name__ == "__main__":
    raise SystemExit(main())
