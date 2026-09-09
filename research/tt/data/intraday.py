"""Intraday bars from Yahoo Finance through ``yfinance``.

The daily fetcher in ``tt.data.yahoo`` is the one strategies run on. This one
exists for questions about what happens *inside* a session, and it comes with
harder limits than the daily feed:

- Yahoo serves a short intraday history and refuses older requests outright:
  about 30 days at one minute, 60 at anything finer than an hour, and 730 at
  the hour. ``MAX_LOOKBACK_DAYS`` records it and ``check_period`` fails closed
  rather than letting a study quietly test on whatever came back.
- Intraday prices are never split- or dividend-adjusted. That is fine for a
  hold inside one session — both land between sessions — and wrong for
  anything that spans a night, which is why these bars are not written to
  ``data/bars/`` and never reach a spec.
- A bar is stamped with the **start** of the period it covers, so the open of
  the 10:00 bar is the 10:00 print. Times that are not on the interval's grid
  are unreachable, not roundable: ``check_grid`` says so.

Timestamps come back tz-aware and are converted to the exchange's own clock,
so a time of day means the same thing on both sides of a daylight-saving
change. Tests never touch the network: they hand ``fetch`` a recorded frame
through ``download``.
"""

from __future__ import annotations

import datetime as dt
import time
from collections.abc import Callable
from typing import Any

import pandas as pd

SOURCE = "yahoo"
EXCHANGE_TZ = "America/New_York"
SESSION_OPEN = dt.time(9, 30)

COLUMNS = ["ts", "open", "high", "low", "close", "volume", "source", "fetched_at"]

# What Yahoo will serve for each interval, in calendar days back from today.
MAX_LOOKBACK_DAYS = {
    "1m": 30, "2m": 60, "5m": 60, "15m": 60, "30m": 60, "90m": 60, "60m": 730, "1h": 730,
}

Downloader = Callable[..., pd.DataFrame]


def _yf_download(*args: Any, **kwargs: Any) -> pd.DataFrame:
    import yfinance as yf

    frame: pd.DataFrame = yf.download(*args, **kwargs)
    return frame


def interval_minutes(interval: str) -> int:
    """Minutes in an interval string (``30m``, ``1h``); raises on an unknown one."""
    if interval not in MAX_LOOKBACK_DAYS:
        raise ValueError(f"intraday: unknown interval {interval!r}; one of {sorted(MAX_LOOKBACK_DAYS)}")
    n, unit = int(interval[:-1]), interval[-1]
    return n * 60 if unit == "h" else n


def check_period(interval: str, days: int) -> None:
    """Refuse a lookback Yahoo will not serve at this interval."""
    interval_minutes(interval)  # rejects an unknown interval first
    if days < 1:
        raise ValueError(f"intraday: days {days} is not positive")
    limit = MAX_LOOKBACK_DAYS[interval]
    if days > limit:
        raise ValueError(
            f"intraday: Yahoo serves at most {limit} days at {interval}, not {days}. Ask for a "
            f"coarser interval (1h reaches 730 days) or a shorter window."
        )


def check_grid(interval: str, when: dt.time) -> None:
    """Refuse a time of day no bar starts at, naming the two that bracket it."""
    step = interval_minutes(interval)
    open_min = SESSION_OPEN.hour * 60 + SESSION_OPEN.minute
    want = when.hour * 60 + when.minute
    if want < open_min or (want - open_min) % step:
        before = open_min + max((want - open_min) // step, 0) * step
        raise ValueError(
            f"intraday: no {interval} bar starts at {when:%H:%M}; bars run from "
            f"{SESSION_OPEN:%H:%M} every {step} minutes, so {_hhmm(before)} and "
            f"{_hhmm(before + step)} are the neighbours."
        )


def _hhmm(minutes: int) -> str:
    return f"{minutes // 60:02d}:{minutes % 60:02d}"


def verify(df: pd.DataFrame) -> None:
    """The §4.1 invariants that survive the loss of a date and an adjusted close."""
    if df.empty:
        raise ValueError("intraday: no bars")
    ts = pd.DatetimeIndex(pd.Series(df["ts"]))
    if ts.tz is None:
        raise ValueError("intraday: ts must be timezone-aware")
    if not ts.is_monotonic_increasing or ts.has_duplicates:
        raise ValueError("intraday: ts is not strictly increasing")
    for name in ("open", "high", "low", "close"):
        col = df[name].to_numpy(dtype=float)
        if not (col > 0).all():
            raise ValueError(f"intraday: {name} is not positive on every bar")
    o, h, lo, c = (df[k].to_numpy(dtype=float) for k in ("open", "high", "low", "close"))
    if (lo > o.clip(max=c)).any():
        raise ValueError("intraday: low is above min(open, close) on some bar")
    if (h < o.clip(min=c)).any():
        raise ValueError("intraday: high is below max(open, close) on some bar")


def to_bars(raw: pd.DataFrame, fetched_at: pd.Timestamp, *, tz: str = EXCHANGE_TZ) -> pd.DataFrame:
    """Reshape one symbol's yfinance intraday frame into ``COLUMNS``, on the exchange clock."""
    df = raw.copy()
    if isinstance(df.columns, pd.MultiIndex):
        df.columns = df.columns.get_level_values(0)
    df = df.rename(columns={"Open": "open", "High": "high", "Low": "low",
                            "Close": "close", "Volume": "volume"})
    df = df.dropna(subset=["open", "high", "low", "close"])
    index = pd.DatetimeIndex(df.index)
    if index.tz is None:
        raise ValueError("intraday: yahoo returned naive timestamps; the time of day is unusable")
    out = pd.DataFrame(
        {
            "ts": index.tz_convert(tz),
            "open": df["open"].astype(float).to_numpy(),
            "high": df["high"].astype(float).to_numpy(),
            "low": df["low"].astype(float).to_numpy(),
            "close": df["close"].astype(float).to_numpy(),
            "volume": df["volume"].fillna(0).astype("int64").to_numpy(),
            "source": SOURCE,
            "fetched_at": fetched_at,
        }
    )
    out = out.drop_duplicates(subset="ts", keep="last").sort_values("ts").reset_index(drop=True)
    verify(out)
    return out[COLUMNS]


def fetch(
    symbols: list[str],
    *,
    interval: str = "30m",
    days: int = 60,
    tz: str = EXCHANGE_TZ,
    download: Downloader = _yf_download,
    retries: int = 4,
    backoff: float = 2.0,
    sleep: Callable[[float], None] = time.sleep,
    now: Callable[[], pd.Timestamp] = lambda: pd.Timestamp.now(tz="UTC"),
) -> dict[str, pd.DataFrame]:
    """Fetch ``days`` of intraday bars for each symbol, retrying with exponential backoff."""
    check_period(interval, days)
    last: Exception | None = None
    for attempt in range(retries):
        try:
            raw = download(symbols, period=f"{days}d", interval=interval, auto_adjust=False,
                      actions=False, prepost=False, progress=False, group_by="ticker", threads=False)
            break
        except Exception as e:  # yfinance raises a zoo of types
            last = e
            sleep(backoff**attempt)
    else:
        raise RuntimeError(f"intraday: giving up after {retries} attempts: {last}")
    if raw.empty:
        raise RuntimeError(f"intraday: no {interval} data for {symbols}")
    fetched_at = now()
    out: dict[str, pd.DataFrame] = {}
    for s in symbols:
        part = raw[s] if isinstance(raw.columns, pd.MultiIndex) else raw
        if not isinstance(part, pd.DataFrame):
            raise RuntimeError(f"intraday: unexpected shape for {s}")
        out[s] = to_bars(part, fetched_at, tz=tz)
    return out

