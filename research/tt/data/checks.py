"""The §4.1 invariants as pure functions.

The same rules the Go reader applies on load, so a file that passes here
loads there. ``violations`` lists every problem; ``check`` raises on the first.
"""

from __future__ import annotations

import datetime as dt
from dataclasses import dataclass

import numpy as np
import pandas as pd

ADJCLOSE_TOLERANCE = 1e-9


@dataclass(frozen=True)
class Violation:
    """One broken invariant: which row, which rule, in words."""

    index: int
    name: str
    message: str


def violations(df: pd.DataFrame) -> list[Violation]:
    """Return every invariant the series breaks, in row order."""
    out: list[Violation] = []
    if df.empty:
        return out
    dates = pd.to_datetime(df["date"]).to_numpy()
    for i in range(1, len(df)):
        if dates[i] <= dates[i - 1]:
            out.append(Violation(i, "date", f"date {dates[i]} is not after {dates[i - 1]}"))
    for name in ("open", "high", "low", "close", "adjclose"):
        col = df[name].to_numpy(dtype=float)
        for j in np.flatnonzero(~(col > 0)).tolist():
            out.append(Violation(int(j), "positive", f"{name} {col[j]} is not positive"))
    o, h, lo, c = (df[k].to_numpy(dtype=float) for k in ("open", "high", "low", "close"))
    for j in np.flatnonzero(lo > np.minimum(o, c)).tolist():
        out.append(Violation(int(j), "low", f"low {lo[j]} is above min(open, close)"))
    for j in np.flatnonzero(h < np.maximum(o, c)).tolist():
        out.append(Violation(int(j), "high", f"high {h[j]} is below max(open, close)"))
    adj = df["adjclose"].to_numpy(dtype=float)
    for j in np.flatnonzero(adj > c * (1 + ADJCLOSE_TOLERANCE)).tolist():
        out.append(Violation(int(j), "adjclose", f"adjclose {adj[j]} is above close {c[j]}"))
    out.sort(key=lambda v: v.index)
    return out


def check(df: pd.DataFrame) -> None:
    """Raise ``ValueError`` naming the first violation, if any."""
    vs = violations(df)
    if vs:
        v = vs[0]
        raise ValueError(f"bar {v.index}: {v.name}: {v.message}")


def stale(df: pd.DataFrame, now: dt.date, trading_days: int = 3) -> bool:
    """True when the last bar is older than ``trading_days`` weekdays before now."""
    if df.empty:
        return True
    last = pd.Timestamp(df["date"].iloc[-1]).date()
    weekdays = int(np.busday_count(last, now))
    return weekdays > trading_days


def mixed_sources(df: pd.DataFrame) -> bool:
    """True when more than one ``source`` appears in the series."""
    return df["source"].nunique() > 1
