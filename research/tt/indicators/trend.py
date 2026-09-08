"""Simple moving average and Donchian channels."""

from __future__ import annotations

import numpy as np
import pandas as pd


def sma(px: np.ndarray, lookback: int) -> np.ndarray:
    """``(C_t − C_{t−L}) / L`` from the running sum; NaN for t < L − 1.

    The running-sum form is the one both languages evaluate in exactly the
    same order, so a price sitting on its average reads the same on both.
    """
    if lookback < 2:
        raise ValueError("lookback must be >= 2")
    p = np.asarray(px, dtype=float)
    n = len(p)
    out = np.full(n, np.nan)
    if n < lookback:
        return out
    cs = np.cumsum(p)
    out[lookback - 1] = cs[lookback - 1] / lookback
    out[lookback:] = (cs[lookback:] - cs[:-lookback]) / lookback
    return out


def donchian_upper(high: np.ndarray, lookback: int) -> np.ndarray:
    """``max(high_{t−L} … high_{t−1})``: the prior bars only; NaN for t < L."""
    return _prior_extreme(high, lookback, "max")


def donchian_lower(low: np.ndarray, lookback: int) -> np.ndarray:
    """``min(low_{t−L} … low_{t−1})``: the prior bars only; NaN for t < L."""
    return _prior_extreme(low, lookback, "min")


def _prior_extreme(x: np.ndarray, lookback: int, which: str) -> np.ndarray:
    if lookback < 1:
        raise ValueError("lookback must be >= 1")
    xs = pd.Series(np.asarray(x, dtype=float))
    rolled = xs.rolling(lookback).max() if which == "max" else xs.rolling(lookback).min()
    # The window ending at t−1, read at t.
    return np.asarray(rolled.shift(1).to_numpy())
