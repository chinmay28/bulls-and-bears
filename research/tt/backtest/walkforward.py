"""Rolling train/test windows: fit the hedge ratio on train, judge on test."""

from __future__ import annotations

import datetime as dt
from dataclasses import dataclass

import pandas as pd

from tt.backtest import metrics
from tt.backtest.engine import CostModel, run_pairs
from tt.stats.cointegration import engle_granger
from tt.strategies import pairs


@dataclass(frozen=True)
class Window:
    """One fold: train on [train_from, train_to], test on (train_to, test_to]."""

    train_from: dt.date
    train_to: dt.date
    test_to: dt.date


@dataclass(frozen=True)
class Fold:
    """What one window produced."""

    window: Window
    hedge_ratio: float
    sharpe: float
    max_drawdown: float
    equity: pd.DataFrame


def windows(start: dt.date, end: dt.date, train_years: int, test_years: int) -> list[Window]:
    """Consecutive folds stepping by ``test_years``."""
    out: list[Window] = []
    t0 = start
    while True:
        t1 = dt.date(t0.year + train_years, t0.month, t0.day) - dt.timedelta(days=1)
        t2 = dt.date(t1.year + test_years, t1.month, t1.day)
        if t2 > end:
            break
        out.append(Window(t0, t1, t2))
        t0 = dt.date(t0.year + test_years, t0.month, t0.day)
    return out


def _slice(df: pd.DataFrame, lo: dt.date, hi: dt.date) -> pd.DataFrame:
    d = pd.to_datetime(df["date"]).dt.date
    return df[(d >= lo) & (d <= hi)].reset_index(drop=True)


def walk_forward(
    bars: dict[str, pd.DataFrame],
    universe: list[str],
    params: pairs.Params,
    gross_leverage: float,
    costs: CostModel,
    folds: list[Window],
) -> list[Fold]:
    """Re-estimate the hedge ratio per fold and backtest the test window."""
    a, b = universe
    out: list[Fold] = []
    for w in folds:
        train = pairs.align({a: _slice(bars[a], w.train_from, w.train_to),
                             b: _slice(bars[b], w.train_from, w.train_to)}, universe)
        eg = engle_granger(train["a"].to_numpy(), train["b"].to_numpy())
        p = pairs.Params(eg.hedge_ratio, params.lookback, params.entry_z, params.exit_z,
                         params.max_hold_days)
        # The test replay needs lookback bars of history before the test
        # window opens, so the state machine and the z-score are warm.
        lo = w.train_to - dt.timedelta(days=int(params.lookback * 2))
        test_bars = {a: _slice(bars[a], lo, w.test_to), b: _slice(bars[b], lo, w.test_to)}
        tr = pairs.replay(test_bars, universe, p, gross_leverage)
        al = pairs.align(test_bars, universe)
        keep = pd.to_datetime(al["date"]).dt.date > w.train_to
        res = run_pairs(tr[keep].reset_index(drop=True), al[keep].reset_index(drop=True),
                        universe, costs)
        eq = res.equity["equity"].to_numpy()
        out.append(Fold(w, eg.hedge_ratio, metrics.sharpe(metrics.daily_returns(eq)),
                        metrics.max_drawdown(eq), res.equity))
    return out
