"""The bar schema (docs/PLAN.md §4.1) as a pyarrow schema, and Parquet I/O.

One file per symbol. Written with exactly these types so the Go reader and
this module agree on the file byte for byte in what matters: ``date`` is a
``date32``, not a timestamp; ``fetched_at`` is a microsecond UTC timestamp.
"""

from __future__ import annotations

from pathlib import Path

import pandas as pd
import pyarrow as pa
import pyarrow.parquet as pq

COLUMNS = ["date", "open", "high", "low", "close", "adjclose", "volume", "source", "fetched_at"]

_FIELDS: dict[str, pa.DataType] = {
    "date": pa.date32(),
    "open": pa.float64(),
    "high": pa.float64(),
    "low": pa.float64(),
    "close": pa.float64(),
    "adjclose": pa.float64(),
    "volume": pa.int64(),
    "source": pa.string(),
    "fetched_at": pa.timestamp("us", tz="UTC"),
}
SCHEMA = pa.schema(_FIELDS)


def normalize(df: pd.DataFrame) -> pd.DataFrame:
    """Return a copy with the schema's columns, in order, oldest first.

    ``date`` becomes a ``datetime.date``; ``fetched_at`` a tz-aware UTC
    timestamp; ``volume`` an int64. Missing columns raise.
    """
    missing = [c for c in COLUMNS if c not in df.columns]
    if missing:
        raise ValueError(f"bars are missing columns {missing}")
    out = df[COLUMNS].copy()
    out["date"] = pd.to_datetime(out["date"]).dt.date
    out["fetched_at"] = pd.to_datetime(out["fetched_at"], utc=True)
    out["volume"] = out["volume"].astype("int64")
    for c in ("open", "high", "low", "close", "adjclose"):
        out[c] = out[c].astype("float64")
    out["source"] = out["source"].astype("string")
    return out.sort_values("date").reset_index(drop=True)


def write_bars(df: pd.DataFrame, path: Path) -> None:
    """Write bars to ``path`` in the schema, creating the directory."""
    path.parent.mkdir(parents=True, exist_ok=True)
    table = pa.Table.from_pandas(normalize(df), schema=SCHEMA, preserve_index=False)
    pq.write_table(table, path)


def read_bars(path: Path) -> pd.DataFrame:
    """Read a bars file back as a DataFrame in the schema's column order."""
    table = pq.read_table(path)
    df = table.to_pandas()
    return normalize(df)
