"""Risk-parity trend over a small ETF universe, with GLD as the haven.

    uv run python scripts/risk_parity_trend.py                 # fetch from Yahoo
    uv run python scripts/risk_parity_trend.py --csv-dir DIR   # Kaggle-format CSVs, no network
    uv run python scripts/risk_parity_trend.py --universe SPY,QQQ,IWM,TLT,GLD

Every ``rebalance_days`` bars the risk assets above their
``trend_lookback``-bar average pool their sleeves and split them by one over
their ``vol_lookback``-bar realised volatility; the other sleeves rest in the
haven (docs/CONTRACTS.md, ``risk_parity_trend``). The two windows are chosen
on the training window from a grid of six; the test window is touched once,
under the next-open fill. The spec is promoted only when the out-of-sample
Sharpe clears 1.0 on Yahoo data. The sweep, the out-of-sample numbers and
the cost stress go under ``--out-dir``.
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
from tt.strategies import risk_parity as rp

BASE = "risk_parity_trend"
DEFAULT_UNIVERSE = ["SPY", "QQQ", "IWM", "TLT", "GLD"]
START = dt.date(2005, 1, 1)
FILL = Fill.NEXT_OPEN
# Six candidates: three trend windows, two volatility windows, monthly turns.
TREND = [126, 200, 252]
VOL = [21, 63]
REBALANCE = 21


def main() -> int:
    args = study.parse_args(__doc__ or "", default_universe=DEFAULT_UNIVERSE,
                            default_train_to=dt.date(2019, 12, 31))
    universe: list[str] = args.universe
    name = study.spec_name(BASE, universe, DEFAULT_UNIVERSE)
    data = study.load(args, universe, START)
    aligned = rp.align(data.bars, universe)
    opens = study.opens_for(aligned, data.bars, universe)
    win = study.windows(aligned["date"], args.train_to)
    if win is None:
        return 2
    print(f"{name}: {data.source_note}; {len(aligned)} aligned bars; "
          f"train {win.train[0]}..{win.train[1]}, test {win.test[0]}..{win.test[1]}; fill {FILL.value}")

    train_bars = {s: study.slice_window(df, *win.train) for s, df in data.bars.items()}
    train_prices = rp.align(train_bars, universe)
    train_opens = study.opens_for(train_prices, train_bars, universe)
    candidates = []
    for trend, vol in itertools.product(TREND, VOL):
        p = rp.Params(trend, vol, REBALANCE)
        tr = rp.replay(train_bars, universe, p, 1.0)
        res = study.backtest_weights(train_prices, rp.weights_frame(tr, universe), win.train[0],
                                     fill=FILL, opens=train_opens)
        candidates.append(study.Candidate(
            {"trend_lookback": trend, "vol_lookback": vol, "rebalance_days": REBALANCE}, res,
            rp.switches(tr, universe)))
    chosen = study.choose(candidates, prefer_larger=("trend_lookback", "vol_lookback"))
    print("train, the grid:")
    for c in candidates:
        d = study.diagnostics(c.result)
        print(f"  trend {int(c.params['trend_lookback']):3d} vol {int(c.params['vol_lookback']):2d}: "
              f"Sharpe {d['sharpe']:.2f}, max DD {d['max_drawdown']:.1%}, turnover {d['turnover']:.1f}x, "
              f"{c.switches} switches{'  <- chosen' if c is chosen else ''}")
    print("train buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(train_prices, universe, s, win.train[0], fill=FILL, opens=train_opens):.2f}"
        for s in universe))
    best = rp.Params(int(chosen.params["trend_lookback"]), int(chosen.params["vol_lookback"]), REBALANCE)

    r = metrics.daily_returns(chosen.result.equity["equity"].to_numpy())
    var = float(r.var(ddof=1))
    gross = max(half_kelly(float(r.mean()), var, cap=1.0) if var > 0 else 1.0, 0.1)
    print(f"chosen trend {best.trend_lookback} vol {best.vol_lookback} every {REBALANCE}; "
          f"half-Kelly leverage {gross:.3f}")

    tr = rp.replay(data.bars, universe, best, gross)
    w = rp.weights_frame(tr, universe)
    oos = study.backtest_weights(aligned, w, win.test[0], fill=FILL, opens=opens)
    keep = (pd.to_datetime(tr["date"]).dt.date >= win.test[0]).to_numpy()
    print(study.describe("test", oos, {"switches": rp.switches(tr[keep].reset_index(drop=True), universe)}))
    print("test buy-and-hold Sharpe: " + ", ".join(
        f"{s} {study.hold(aligned, universe, s, win.test[0], fill=FILL, opens=opens):.2f}" for s in universe))
    stress = study.cost_stress(aligned, w, win.test[0], fill=FILL, opens=opens)
    study.print_stress(stress)
    print("wrote", study.record(args, name, candidates=candidates, chosen=chosen, oos=oos, stress=stress))

    return study.emit(
        args=args, name=name, strategy=rp.NAME, universe=universe,
        params={"trend_lookback": best.trend_lookback, "vol_lookback": best.vol_lookback,
                "rebalance_days": best.rebalance_days},
        gross=gross, win=win, oos=oos,
        signals=rp.signals(data.bars, universe, best, gross), data=data, script="risk_parity_trend.py",
        fill=FILL, study=BASE,
    )


if __name__ == "__main__":
    raise SystemExit(main())
