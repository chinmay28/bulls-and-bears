"""The daily-bar backtest loop, per docs/CONTRACTS.md "Backtest engine".

Two fills, chosen by the spec's ``execution.fill_at``:

- ``same_close_legacy``: at each aligned bar's close the book is marked, the
  targets for that bar are read, and the book is rebalanced to them at that
  same close. This is the loop the first four strategies were researched
  with, and what the runtime's close-time run approximates.
- ``next_open``: the targets read at bar t's close are filled at bar t+1's
  adjusted open; the book is marked at every close. Nothing trades on the
  first bar, and the last bar's signal is never filled.

Go's ``bnb backtest`` runs the same arithmetic and the golden equity curve
holds the two together.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum

import numpy as np
import pandas as pd

STARTING_EQUITY = 10_000.0


class Fill(StrEnum):
    """``execution.fill_at``: where the order fills."""

    SAME_CLOSE = "same_close_legacy"
    NEXT_OPEN = "next_open"


@dataclass(frozen=True)
class CostModel:
    """Commission per order and slippage in basis points of notional."""

    commission_usd: float = 0.0
    slippage_bps: float = 0.0


@dataclass(frozen=True)
class Result:
    """The equity curve (at each bar's close, one row per bar) and every trade."""

    equity: pd.DataFrame
    trades: pd.DataFrame


def run(
    prices: pd.DataFrame,
    weights: pd.DataFrame,
    costs: CostModel,
    starting_equity: float = STARTING_EQUITY,
    *,
    fill: Fill = Fill.SAME_CLOSE,
    opens: pd.DataFrame | None = None,
) -> Result:
    """Backtest target ``weights`` over ``prices``.

    All frames are indexed by row, aligned, with one column per symbol
    (``prices`` in adjclose, ``opens`` in adjusted open, ``weights`` as signed
    fractions of equity) and a ``date`` column on ``prices``. ``opens`` is
    required for ``Fill.NEXT_OPEN`` and ignored otherwise.
    """
    if fill is Fill.NEXT_OPEN:
        if opens is None:
            raise ValueError("next_open needs the adjusted opens")
        return _run_next_open(prices, opens, weights, costs, starting_equity)
    return _run_same_close(prices, weights, costs, starting_equity)


def _run_same_close(
    prices: pd.DataFrame, weights: pd.DataFrame, costs: CostModel, starting_equity: float
) -> Result:
    symbols = [c for c in weights.columns if c != "date"]
    p = prices[symbols].to_numpy(dtype=float)
    w = weights[symbols].to_numpy(dtype=float)
    n = len(p)
    cash = starting_equity
    shares = np.zeros(len(symbols))
    equity_out = np.zeros(n)
    trades: list[dict[str, object]] = []
    for t in range(n):
        equity = cash + float(np.dot(shares, p[t]))
        cash, shares = _rebalance(cash, shares, w[t], p[t], equity, costs, symbols, prices["date"].iloc[t], trades)
        equity_out[t] = cash + float(np.dot(shares, p[t]))
    return _result(prices, equity_out, trades)


def _run_next_open(
    prices: pd.DataFrame,
    opens: pd.DataFrame,
    weights: pd.DataFrame,
    costs: CostModel,
    starting_equity: float,
) -> Result:
    symbols = [c for c in weights.columns if c != "date"]
    c = prices[symbols].to_numpy(dtype=float)
    o = opens[symbols].to_numpy(dtype=float)
    w = weights[symbols].to_numpy(dtype=float)
    n = len(c)
    if len(o) != n or len(w) != n:
        raise ValueError("prices, opens and weights must have the same number of rows")
    cash = starting_equity
    shares = np.zeros(len(symbols))
    equity_out = np.zeros(n)
    trades: list[dict[str, object]] = []
    pending: np.ndarray | None = None
    for t in range(n):
        if pending is not None:
            equity = cash + float(np.dot(shares, o[t]))
            cash, shares = _rebalance(cash, shares, pending, o[t], equity, costs, symbols, prices["date"].iloc[t], trades)
        equity_out[t] = cash + float(np.dot(shares, c[t]))
        pending = w[t]
    return _result(prices, equity_out, trades)


def _rebalance(
    cash: float,
    shares: np.ndarray,
    target_w: np.ndarray,
    px: np.ndarray,
    equity: float,
    costs: CostModel,
    symbols: list[str],
    date: object,
    trades: list[dict[str, object]],
) -> tuple[float, np.ndarray]:
    """Move the book to ``target_w`` at ``px``, charging the cost model."""
    target = target_w * equity / px
    delta = target - shares
    traded = float(np.dot(np.abs(delta), px))
    orders = int(np.count_nonzero(delta))
    cost = traded * costs.slippage_bps / 10_000 + costs.commission_usd * orders
    cash = cash - float(np.dot(delta, px)) - cost
    for i, s in enumerate(symbols):
        if delta[i] != 0:
            trades.append({"date": date, "symbol": s, "qty": delta[i], "price": px[i]})
    return cash, target


def _result(prices: pd.DataFrame, equity: np.ndarray, trades: list[dict[str, object]]) -> Result:
    equity_df = pd.DataFrame({"date": prices["date"], "equity": equity})
    trades_df = pd.DataFrame(trades, columns=["date", "symbol", "qty", "price"])
    return Result(equity_df, trades_df)


def run_pairs(
    replayed: pd.DataFrame,
    aligned: pd.DataFrame,
    universe: list[str],
    costs: CostModel,
    starting_equity: float = STARTING_EQUITY,
) -> Result:
    """Convenience: backtest a ``pairs.replay`` trace over its ``pairs.align`` prices."""
    a, b = universe
    prices = pd.DataFrame({"date": aligned["date"], a: aligned["a"], b: aligned["b"]})
    weights = pd.DataFrame({a: replayed["w_a"], b: replayed["w_b"]})
    return run(prices, weights, costs, starting_equity)
