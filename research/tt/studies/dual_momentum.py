"""The dual-momentum sweep, shared by ``dual_momentum.py`` and ``sector_rotation.py``."""

from __future__ import annotations

import datetime as dt
import itertools
from dataclasses import dataclass

import pandas as pd

from tt import study
from tt.backtest import metrics
from tt.backtest.engine import Fill
from tt.stats.kelly import half_kelly
from tt.strategies import momentum


@dataclass(frozen=True)
class Setup:
    """What one dual-momentum study decides for itself."""

    base: str
    script: str
    default_universe: list[str]
    start: dt.date
    fill: Fill
    lookbacks: list[int]
    top_ks: list[int]
    rebalances: list[int]
    default_train_to: dt.date = dt.date(2019, 12, 31)


def run(doc: str, setup: Setup) -> int:
    """Parse the flags, sweep the grid on the training window, judge once, emit."""
    args = study.parse_args(doc, default_universe=setup.default_universe,
                            default_train_to=setup.default_train_to)
    universe: list[str] = args.universe
    name = study.spec_name(setup.base, universe, setup.default_universe)
    data = study.load(args, universe, setup.start)
    aligned = momentum.align(data.bars, universe)
    opens = study.opens_for(aligned, data.bars, universe) if setup.fill is Fill.NEXT_OPEN else None
    win = study.windows(aligned["date"], args.train_to)
    if win is None:
        return 2
    print(f"{name}: {data.source_note}; {len(aligned)} aligned bars; "
          f"train {win.train[0]}..{win.train[1]}, test {win.test[0]}..{win.test[1]}; fill {setup.fill.value}")

    train_bars = {s: study.slice_window(df, *win.train) for s, df in data.bars.items()}
    train_prices = momentum.align(train_bars, universe)
    train_opens = study.opens_for(train_prices, train_bars, universe) if setup.fill is Fill.NEXT_OPEN else None
    k = len(universe) - 1
    candidates = []
    for lb, top, reb in itertools.product(setup.lookbacks, [t for t in setup.top_ks if t <= k], setup.rebalances):
        p = momentum.Params(lb, top, reb)
        tr = momentum.replay(train_bars, universe, p, 1.0)
        res = study.backtest_weights(train_prices, momentum.weights_frame(tr, universe), win.train[0],
                                     fill=setup.fill, opens=train_opens)
        candidates.append(study.Candidate({"lookback": lb, "top_k": top, "rebalance_days": reb}, res,
                                          momentum.switches(tr, universe)))
    chosen = study.choose(candidates, prefer_larger=("lookback",))
    print("train, the grid:")
    for c in candidates:
        d = study.diagnostics(c.result)
        print(f"  lookback {int(c.params['lookback']):3d} top {int(c.params['top_k'])} every "
              f"{int(c.params['rebalance_days']):2d}: Sharpe {d['sharpe']:.2f}, max DD {d['max_drawdown']:.1%}, "
              f"turnover {d['turnover']:.1f}x, {c.switches} changes{'  <- chosen' if c is chosen else ''}")
    print("train buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(train_prices, universe, s, win.train[0], fill=setup.fill, opens=train_opens):.2f}"
        for s in universe))
    best = momentum.Params(int(chosen.params["lookback"]), int(chosen.params["top_k"]),
                           int(chosen.params["rebalance_days"]))

    r = metrics.daily_returns(chosen.result.equity["equity"].to_numpy())
    var = float(r.var(ddof=1))
    gross = max(half_kelly(float(r.mean()), var, cap=1.0) if var > 0 else 1.0, 0.1)
    print(f"chosen lookback {best.lookback} top {best.top_k} every {best.rebalance_days}; "
          f"half-Kelly leverage {gross:.3f}")

    tr = momentum.replay(data.bars, universe, best, gross)
    w = momentum.weights_frame(tr, universe)
    oos = study.backtest_weights(aligned, w, win.test[0], fill=setup.fill, opens=opens)
    keep = (pd.to_datetime(tr["date"]).dt.date >= win.test[0]).to_numpy()
    print(study.describe("test", oos, {"changes": momentum.switches(tr[keep].reset_index(drop=True), universe)}))
    print("test buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(aligned, universe, s, win.test[0], fill=setup.fill, opens=opens):.2f}"
        for s in universe))
    stress = study.cost_stress(aligned, w, win.test[0], fill=setup.fill, opens=opens)
    study.print_stress(stress)
    print("wrote", study.record(args, name, candidates=candidates, chosen=chosen, oos=oos, stress=stress))

    return study.emit(
        args=args, name=name, strategy=momentum.NAME, universe=universe,
        params={"lookback": best.lookback, "top_k": best.top_k, "rebalance_days": best.rebalance_days},
        gross=gross, win=win, oos=oos,
        signals=momentum.signals(data.bars, universe, best, gross), data=data, script=setup.script,
        fill=setup.fill, study=setup.base,
    )
