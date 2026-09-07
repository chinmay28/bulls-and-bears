"""Daily bars from Yahoo Finance through ``yfinance``.

Unofficial, unreliable, survivorship-biased — and free. Every fetch is
retried with backoff, every bar is stamped ``source="yahoo"``, and the result
is checked before it is written. Tests never touch the network: they hand
``fetch`` a recorded frame through ``download``.
"""

from __future__ import annotations

import datetime as dt
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

import pandas as pd

from tt.data import bars as barsio
from tt.data.checks import check

SOURCE = "yahoo"

Downloader = Callable[..., pd.DataFrame]


def _yf_download(*args: Any, **kwargs: Any) -> pd.DataFrame:
    import yfinance as yf

    frame: pd.DataFrame = yf.download(*args, **kwargs)
    return frame


def _to_bars(raw: pd.DataFrame, symbol: str, fetched_at: pd.Timestamp) -> pd.DataFrame:
    """Reshape one symbol's yfinance frame into the §4.1 columns."""
    df = raw.copy()
    if isinstance(df.columns, pd.MultiIndex):
        df.columns = df.columns.get_level_values(0)
    df = df.rename(
        columns={
            "Open": "open",
            "High": "high",
            "Low": "low",
            "Close": "close",
            "Adj Close": "adjclose",
            "Volume": "volume",
        }
    )
    df = df.dropna(subset=["open", "high", "low", "close", "adjclose"])
    df["date"] = pd.to_datetime(df.index).date
    df["source"] = SOURCE
    df["fetched_at"] = fetched_at
    df["volume"] = df["volume"].fillna(0).astype("int64")
    out = barsio.normalize(df)
    check(out)
    return out


def fetch(
    symbols: list[str],
    start: dt.date,
    end: dt.date | None = None,
    *,
    download: Downloader = _yf_download,
    retries: int = 4,
    backoff: float = 2.0,
    sleep: Callable[[float], None] = time.sleep,
    now: Callable[[], pd.Timestamp] = lambda: pd.Timestamp.now(tz="UTC"),
) -> dict[str, pd.DataFrame]:
    """Fetch daily bars for each symbol, retrying with exponential backoff."""
    last: Exception | None = None
    for attempt in range(retries):
        try:
            raw = download(
                symbols,
                start=start.isoformat(),
                end=None if end is None else (end + dt.timedelta(days=1)).isoformat(),
                auto_adjust=False,
                actions=False,
                progress=False,
                group_by="ticker",
                threads=False,
            )
            break
        except Exception as e:  # yfinance raises a zoo of types
            last = e
            sleep(backoff**attempt)
    else:
        raise RuntimeError(f"yahoo: giving up after {retries} attempts: {last}")
    if raw.empty:
        raise RuntimeError(f"yahoo: no data for {symbols} from {start}")
    fetched_at = now()
    out: dict[str, pd.DataFrame] = {}
    for s in symbols:
        part = raw[s] if isinstance(raw.columns, pd.MultiIndex) else raw
        if not isinstance(part, pd.DataFrame):
            raise RuntimeError(f"yahoo: unexpected shape for {s}")
        out[s] = _to_bars(part, s, fetched_at)
    return out


def refresh(symbol: str, path: Path, **kwargs: Any) -> pd.DataFrame:
    """Append the bars ``path`` is missing since its last date and rewrite it.

    A file that does not exist yet is fetched from 2010 onward.
    """
    if path.exists():
        have = barsio.read_bars(path)
        start = pd.Timestamp(have["date"].iloc[-1]).date() + dt.timedelta(days=1)
    else:
        have = pd.DataFrame(columns=barsio.COLUMNS)
        start = dt.date(2010, 1, 1)
    if start > dt.date.today():
        return have
    try:
        new = fetch([symbol], start, **kwargs)[symbol]
    except RuntimeError as e:
        if "no data" in str(e) and not have.empty:
            return have
        raise
    merged = pd.concat([have, new], ignore_index=True) if not have.empty else new
    merged = barsio.normalize(merged.drop_duplicates(subset="date", keep="last"))
    check(merged)
    barsio.write_bars(merged, path)
    return merged
