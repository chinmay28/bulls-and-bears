"""The daily-bar backtest loop, per docs/CONTRACTS.md "Backtest engine".

At each aligned bar's close the book is marked, the targets for that bar are
read, and the book is rebalanced to them at that close with the cost model
applied to the notional traded. Returns accrue from the next bar. Go's
``bnb backtest`` runs the same arithmetic and the golden equity curve holds
the two together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

STARTING_EQUITY = 10_000.0


@dataclass(frozen=True)
class CostModel:
    """Commission per order and slippage in basis points of notional."""

    commission_usd: float = 0.0
    slippage_bps: float = 0.0


@dataclass(frozen=True)
class Result:
    """The equity curve (post-rebalance, one row per bar) and every trade."""

    equity: pd.DataFrame
    trades: pd.DataFrame


def run(
    prices: pd.DataFrame,
    weights: pd.DataFrame,
    costs: CostModel,
    starting_equity: float = STARTING_EQUITY,
) -> Result:
    """Backtest target ``weights`` over ``prices``.

    Both frames are indexed by row, aligned, with one column per symbol
    (``prices`` in adjclose, ``weights`` as signed fractions of equity) and a
    ``date`` column on ``prices``.
    """
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
        target = w[t] * equity / p[t]
        delta = target - shares
        traded = float(np.dot(np.abs(delta), p[t]))
        orders = int(np.count_nonzero(delta))
        cost = traded * costs.slippage_bps / 10_000 + costs.commission_usd * orders
        cash = cash - float(np.dot(delta, p[t])) - cost
        for i, s in enumerate(symbols):
            if delta[i] != 0:
                trades.append(
                    {"date": prices["date"].iloc[t], "symbol": s, "qty": delta[i], "price": p[t, i]}
                )
        shares = target
        equity_out[t] = cash + float(np.dot(shares, p[t]))
    equity_df = pd.DataFrame({"date": prices["date"], "equity": equity_out})
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
