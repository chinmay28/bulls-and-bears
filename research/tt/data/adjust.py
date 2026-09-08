"""Adjusted open, high and low, per docs/CONTRACTS.md "Adjusted OHLC".

The bar files carry raw ``open``/``high``/``low``/``close`` and a
split-and-dividend adjusted ``adjclose``. A strategy that looks at highs or
lows, or a backtest that fills at the open, needs those on the same footing
as ``adjclose``, or a split reads as a crash and a dividend as a gap. The
factor is ``adjclose / close`` bar by bar, and each raw price is multiplied by
it in that order; the Go side (``bars.AdjustedOpen`` and friends) does the
same arithmetic, so the two agree bit for bit.
"""

from __future__ import annotations

import numpy as np
import pandas as pd


def factor(df: pd.DataFrame) -> np.ndarray:
    """``adjclose_t / close_t`` per bar. A non-positive close is a data error."""
    close = df["close"].to_numpy(dtype=float)
    if not np.all(close > 0):
        raise ValueError("adjust: a close is not positive; the bars failed their invariants")
    return np.asarray(df["adjclose"].to_numpy(dtype=float) / close)


def adj_open(df: pd.DataFrame) -> np.ndarray:
    """``open_t · f_t``."""
    return np.asarray(df["open"].to_numpy(dtype=float) * factor(df))


def adj_high(df: pd.DataFrame) -> np.ndarray:
    """``high_t · f_t``."""
    return np.asarray(df["high"].to_numpy(dtype=float) * factor(df))


def adj_low(df: pd.DataFrame) -> np.ndarray:
    """``low_t · f_t``."""
    return np.asarray(df["low"].to_numpy(dtype=float) * factor(df))


def aligned(
    bars: dict[str, pd.DataFrame], universe: list[str], column: str
) -> pd.DataFrame:
    """Inner-join one adjusted column of every symbol on date.

    ``column`` is ``open``, ``high`` or ``low``; the result has a ``date``
    column and one column per symbol holding that price times the bar's
    adjustment factor, on the dates every symbol has.
    """
    fn = {"open": adj_open, "high": adj_high, "low": adj_low}[column]
    out: pd.DataFrame | None = None
    for s in universe:
        f = pd.DataFrame({"date": bars[s]["date"], s: fn(bars[s])})
        out = f if out is None else out.merge(f, on="date", how="inner")
    assert out is not None
    return out.sort_values("date").reset_index(drop=True)
