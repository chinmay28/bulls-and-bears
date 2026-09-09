"""What a hold between two times of day did, session by session.

Pure arithmetic over intraday bars: one row per session carrying the price
at each of two times and the simple return between them, plus the sessions
that had to be dropped for want of a bar. No I/O, no clock, no randomness.

The times are read on the exchange's own clock, so 10:00 is 10:00 in New
York on both sides of a daylight-saving change — which is what a Pacific
trader means by "seven in the morning" all year round. Costs are a
round-trip charge in basis points subtracted in return space: a market order
pays about half the spread on each side, and on a penny-wide ETF that is a
basis point or two in total.

This module does not know where bars come from and never adjusts them. It
does not have to: a hold that opens and closes inside one session spans no
dividend and no split, because both land between sessions.
"""

from __future__ import annotations

import datetime as dt
import math
from dataclasses import dataclass

import numpy as np
import pandas as pd

from tt.backtest.metrics import TRADING_DAYS

BPS = 1e-4

# The columns ``sessions`` returns, in order.
COLUMNS = ["date", "entry", "exit", "ret"]


@dataclass(frozen=True)
class Sessions:
    """The holds a pair of times produced, and the days that had no bar.

    ``dropped`` is what honesty costs: a half day that closes at 13:00 has no
    15:00 print, and a day Yahoo served short has no 10:00 one. Neither is a
    losing trade, so neither may sit in the frame as a zero.
    """

    frame: pd.DataFrame
    dropped: list[dt.date]

    @property
    def returns(self) -> pd.Series:
        """The simple return of each completed hold."""
        return self.frame["ret"]


def sessions(bars: pd.DataFrame, entry: dt.time, exit_at: dt.time, *, price: str = "open") -> Sessions:
    """One row per day that had a bar at both times: the two prices and the return.

    ``bars`` needs a timezone-aware ``ts`` column on the exchange's clock and
    the ``price`` column to trade at. ``price="open"`` is the honest choice
    for a market order at a stated time, because an intraday bar is stamped
    with the start of the period it covers: the open of the 10:00 bar is the
    10:00 print, while its close is the 10:29 one.
    """
    if price not in bars.columns:
        raise ValueError(f"sessions: bars have no {price!r} column")
    if "ts" not in bars.columns:
        raise ValueError("sessions: bars have no 'ts' column")
    if entry >= exit_at:
        raise ValueError(f"sessions: entry {entry} is not before exit {exit_at}")
    ts = pd.DatetimeIndex(pd.Series(bars["ts"]))
    if ts.tz is None:
        raise ValueError("sessions: ts must be timezone-aware; a naive stamp has no time of day")

    tidy = pd.DataFrame({"date": ts.date, "tod": ts.time, "px": bars[price].to_numpy(dtype=float)})
    legs = [_leg(tidy, want, name) for name, want in (("entry", entry), ("exit", exit_at))]
    frame = legs[0].merge(legs[1], on="date", how="inner").sort_values("date").reset_index(drop=True)
    frame["ret"] = frame["exit"] / frame["entry"] - 1.0
    kept = set(frame["date"])
    dropped = sorted(d for d in dict.fromkeys(tidy["date"]) if d not in kept)
    return Sessions(frame[COLUMNS], dropped)


def _leg(tidy: pd.DataFrame, want: dt.time, name: str) -> pd.DataFrame:
    """The one price per day stamped ``want``; two of them is a broken feed."""
    part = tidy[tidy["tod"] == want]
    dup = part["date"].duplicated()
    if bool(dup.any()):
        raise ValueError(f"sessions: {part.loc[dup, 'date'].iloc[0]} has two {want:%H:%M} bars")
    return part[["date", "px"]].rename(columns={"px": name}).reset_index(drop=True)


def net(returns: pd.Series, cost_bps: float) -> pd.Series:
    """The same returns after a round-trip cost, charged in return space."""
    if cost_bps < 0:
        raise ValueError(f"net: cost_bps {cost_bps} is negative")
    return returns - cost_bps * BPS


@dataclass(frozen=True)
class Summary:
    """How often the hold paid, and how sure of that a sample this size lets you be."""

    days: int
    wins: int
    win_rate: float
    win_rate_se: float
    mean: float
    median: float
    stdev: float
    t_stat: float

    @property
    def days_per_year(self) -> float:
        """Winning days in a 252-day year at this rate."""
        return self.win_rate * TRADING_DAYS

    def line(self, label: str) -> str:
        """One line of numbers for a printed table."""
        return (
            f"{label:<22} {self.days:>5} days  win {self.win_rate:6.1%} +/- {self.win_rate_se:.1%}"
            f"  ({self.days_per_year:5.1f}/252)  mean {self.mean * 1e4:+7.2f} bp"
            f"  median {self.median * 1e4:+7.2f} bp  sd {self.stdev * 1e4:6.1f} bp  t {self.t_stat:+5.2f}"
        )


def summarize(returns: pd.Series) -> Summary:
    """Count the winners and say how thin the evidence is.

    A day is a win when its return is strictly positive; a flat day pays
    nothing and is not one. ``win_rate_se`` is the binomial standard error,
    the first number to read: sixty days cannot tell 50% from 56%.
    """
    r = np.asarray(returns, dtype=float)
    n = len(r)
    if n == 0:
        return Summary(0, 0, math.nan, math.nan, math.nan, math.nan, math.nan, math.nan)
    wins = int((r > 0).sum())
    p = wins / n
    se = math.sqrt(p * (1 - p) / n)
    mean, median = float(r.mean()), float(np.median(r))
    sd = float(r.std(ddof=1)) if n > 1 else math.nan
    t = mean / (sd / math.sqrt(n)) if n > 1 and sd > 0 else math.nan
    return Summary(n, wins, p, se, mean, median, sd, t)
