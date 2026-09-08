"""Wilder's RSI, exactly as docs/CONTRACTS.md "Indicators" defines it."""

from __future__ import annotations

import numpy as np


def wilder_rsi(px: np.ndarray, period: int) -> np.ndarray:
    """RSI over ``period`` daily changes; NaN for t < period.

    The first averages are plain means of the first ``period`` gains and
    losses; each later one is ``(prev · (period − 1) + new) / period``. Then
    100 when only gains, 50 when nothing moved, else ``100 − 100 / (1 + RS)``.
    The arithmetic is done in that order, bar by bar, so the Go side agrees
    bit for bit.
    """
    if period < 1:
        raise ValueError("period must be >= 1")
    p = np.asarray(px, dtype=float)
    n = len(p)
    out = np.full(n, np.nan)
    if n <= period:
        return out
    gain = 0.0
    loss = 0.0
    for t in range(1, period + 1):
        d = p[t] - p[t - 1]
        gain += d if d > 0 else 0.0
        loss += -d if d < 0 else 0.0
    gain /= period
    loss /= period
    out[period] = _rsi(gain, loss)
    for t in range(period + 1, n):
        d = p[t] - p[t - 1]
        up = d if d > 0 else 0.0
        down = -d if d < 0 else 0.0
        gain = (gain * (period - 1) + up) / period
        loss = (loss * (period - 1) + down) / period
        out[t] = _rsi(gain, loss)
    return out


def _rsi(gain: float, loss: float) -> float:
    if loss == 0:
        return 100.0 if gain > 0 else 50.0
    rs = gain / loss
    return 100.0 - 100.0 / (1.0 + rs)
