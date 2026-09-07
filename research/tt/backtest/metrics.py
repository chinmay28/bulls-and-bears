"""Sharpe, max drawdown and its duration, per docs/CONTRACTS.md."""

from __future__ import annotations

import math

import numpy as np

TRADING_DAYS = 252


def daily_returns(equity: np.ndarray) -> np.ndarray:
    """``e_t / e_{t-1} - 1`` for t >= 1."""
    e = np.asarray(equity, dtype=float)
    if len(e) < 2:
        return np.array([])
    return np.asarray(e[1:] / e[:-1] - 1)


def sharpe(returns: np.ndarray) -> float:
    """Annualized, sample standard deviation, rf = 0. NaN when undefined."""
    r = np.asarray(returns, dtype=float)
    if len(r) < 2:
        return math.nan
    sd = float(np.std(r, ddof=1))
    if sd == 0 or math.isnan(sd):
        return math.nan
    return float(np.mean(r)) / sd * math.sqrt(TRADING_DAYS)


def max_drawdown(equity: np.ndarray) -> float:
    """``min_t (e_t / max_{s<=t} e_s - 1)``; 0 for a curve that never dips."""
    e = np.asarray(equity, dtype=float)
    if len(e) == 0:
        return 0.0
    peak = np.maximum.accumulate(e)
    return float(np.min(e / peak - 1))


def max_drawdown_duration(equity: np.ndarray) -> int:
    """Longest run of consecutive days below the running high."""
    e = np.asarray(equity, dtype=float)
    if len(e) == 0:
        return 0
    peak = np.maximum.accumulate(e)
    longest = run = 0
    for under in e < peak:
        run = run + 1 if under else 0
        longest = max(longest, run)
    return longest


def summary(equity: np.ndarray) -> dict[str, float | int]:
    """The three numbers together, keyed the way ``metrics.json`` is."""
    return {
        "sharpe": sharpe(daily_returns(equity)),
        "max_drawdown": max_drawdown(equity),
        "max_drawdown_duration": max_drawdown_duration(equity),
    }
