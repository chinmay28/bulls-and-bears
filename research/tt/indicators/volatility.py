"""Realised volatility, true range and ATR."""

from __future__ import annotations

import numpy as np

from tt.indicators.rolling import rolling_sample_sd


def realised_vol(px: np.ndarray, lookback: int) -> np.ndarray:
    """Sample sd of the daily simple returns over ``lookback`` returns ending at t.

    NaN for t < L. Not annualised: the strategies that use it compare
    assets with each other, or a target with it, and the constant cancels.
    """
    p = np.asarray(px, dtype=float)
    out = np.full(len(p), np.nan)
    if len(p) < 2:
        return out
    r = p[1:] / p[:-1] - 1
    out[1:] = rolling_sample_sd(r, lookback)
    return out


def true_range(high: np.ndarray, low: np.ndarray, close: np.ndarray) -> np.ndarray:
    """``max(h − l, |h − c_{t−1}|, |l − c_{t−1}|)``; NaN at t = 0. Adjusted inputs."""
    h = np.asarray(high, dtype=float)
    lo = np.asarray(low, dtype=float)
    c = np.asarray(close, dtype=float)
    n = len(c)
    out = np.full(n, np.nan)
    for t in range(1, n):
        out[t] = max(h[t] - lo[t], abs(h[t] - c[t - 1]), abs(lo[t] - c[t - 1]))
    return out


def atr(high: np.ndarray, low: np.ndarray, close: np.ndarray, period: int) -> np.ndarray:
    """Wilder's ATR: the mean of the first ``period`` true ranges, then smoothed."""
    if period < 1:
        raise ValueError("period must be >= 1")
    tr = true_range(high, low, close)
    n = len(tr)
    out = np.full(n, np.nan)
    if n <= period:
        return out
    acc = 0.0
    for t in range(1, period + 1):
        acc += tr[t]
    cur = acc / period
    out[period] = cur
    for t in range(period + 1, n):
        cur = (cur * (period - 1) + tr[t]) / period
        out[t] = cur
    return out
