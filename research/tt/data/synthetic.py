"""A seeded, cointegrated pair for tests and for goldens when Yahoo is out of reach.

B is a geometric random walk; A is ``h * B`` plus a mean-reverting spread
(an Ornstein-Uhlenbeck process in discrete time), so a pairs strategy has
something to trade and the answer is the same on every machine.
"""

from __future__ import annotations

import datetime as dt

import numpy as np
import pandas as pd

from tt.data import bars as barsio


def cointegrated_pair(
    n: int = 800,
    *,
    seed: int = 7,
    hedge: float = 1.6,
    start: dt.date = dt.date(2015, 1, 2),
    b0: float = 40.0,
    daily_vol: float = 0.012,
    spread_theta: float = 0.08,
    spread_sigma: float = 0.9,
) -> dict[str, pd.DataFrame]:
    """Return ``{"A": bars, "B": bars}`` over ``n`` business days."""
    rng = np.random.default_rng(seed)
    dates = pd.bdate_range(start, periods=n).date
    b = b0 * np.exp(np.cumsum(rng.normal(0.0002, daily_vol, n)))
    spread = np.zeros(n)
    for t in range(1, n):
        spread[t] = spread[t - 1] - spread_theta * spread[t - 1] + rng.normal(0, spread_sigma)
    a = hedge * b + spread
    fetched = pd.Timestamp("2026-09-06T00:00:00Z")
    return {"A": _ohlc(dates, a, rng, fetched), "B": _ohlc(dates, b, rng, fetched)}


def _ohlc(
    dates: np.ndarray, close: np.ndarray, rng: np.random.Generator, fetched: pd.Timestamp
) -> pd.DataFrame:
    n = len(close)
    open_ = np.concatenate([[close[0]], close[:-1]])
    wiggle = np.abs(rng.normal(0, 0.003, n)) * close
    high = np.maximum(open_, close) + wiggle
    low = np.minimum(open_, close) - wiggle
    df = pd.DataFrame(
        {
            "date": dates,
            "open": open_,
            "high": high,
            "low": low,
            "close": close,
            "adjclose": close,
            "volume": rng.integers(100_000, 5_000_000, n),
            "source": "synthetic",
            "fetched_at": fetched,
        }
    )
    return barsio.normalize(df)
