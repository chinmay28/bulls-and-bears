"""The indicator parity fixtures, ``golden/indicators/``.

    uv run python scripts/golden_indicators.py

One seeded synthetic OHLC series (no network, no clock) and every indicator
in docs/CONTRACTS.md "Indicators" evaluated over it by the Python side. The
Go package ``internal/indicator`` reads ``series.csv`` and reproduces each
column of ``expected.csv`` to 1e-9. Regenerate and commit together with any
change to the contract or either implementation.
"""

from __future__ import annotations

import datetime as dt
import sys
from pathlib import Path

import numpy as np
import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt import indicators as ind
from tt.spec import REPO_ROOT

OUT = REPO_ROOT / "golden" / "indicators"
N = 300


def series() -> pd.DataFrame:
    """A geometric random walk with a flat stretch, so a zero sd is exercised."""
    rng = np.random.default_rng(11)
    r = rng.normal(0.0003, 0.012, N)
    r[120:130] = 0.0
    close = 100 * np.exp(np.cumsum(r))
    open_ = np.concatenate([[close[0]], close[:-1]]) * (1 + rng.normal(0, 0.002, N))
    wiggle = np.abs(rng.normal(0, 0.004, N)) * close
    high = np.maximum(open_, close) + wiggle
    low = np.minimum(open_, close) - wiggle
    dates = pd.bdate_range(dt.date(2020, 1, 1), periods=N).date
    return pd.DataFrame({"date": dates, "open": open_, "high": high, "low": low, "close": close})


def main() -> int:
    s = series()
    c = s["close"].to_numpy()
    h = s["high"].to_numpy()
    lo = s["low"].to_numpy()
    out = pd.DataFrame({
        "date": s["date"],
        "rolling_mean_20": ind.rolling_mean(c, 20),
        "rolling_sample_sd_20": ind.rolling_sample_sd(c, 20),
        "rolling_zscore_20": ind.rolling_zscore(c, 20),
        "rolling_return_21": ind.rolling_return(c, 21),
        "sma_50": ind.sma(c, 50),
        "donchian_upper_20": ind.donchian_upper(h, 20),
        "donchian_lower_10": ind.donchian_lower(lo, 10),
        "realised_vol_21": ind.realised_vol(c, 21),
        "true_range": ind.true_range(h, lo, c),
        "atr_14": ind.atr(h, lo, c, 14),
        "wilder_rsi_2": ind.wilder_rsi(c, 2),
        "wilder_rsi_14": ind.wilder_rsi(c, 14),
    })
    OUT.mkdir(parents=True, exist_ok=True)
    s.to_csv(OUT / "series.csv", index=False, float_format="%.17g")
    out.to_csv(OUT / "expected.csv", index=False, float_format="%.17g", na_rep="NaN")
    (OUT / "README.md").write_text(
        "# golden/indicators\n\nParity fixtures for docs/CONTRACTS.md \"Indicators\": `series.csv` is a\n"
        "seeded synthetic OHLC series, `expected.csv` every indicator's output from\n"
        "`research/tt/indicators`, `NaN` where the contract says undefined. The Go\n"
        "package `internal/indicator` must reproduce each column to 1e-9.\n\n"
        f"Written by research/scripts/golden_indicators.py on {dt.datetime.now(dt.UTC):%Y-%m-%d}.\n"
    )
    print(f"wrote {OUT}: {N} bars, {len(out.columns) - 1} indicators")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
