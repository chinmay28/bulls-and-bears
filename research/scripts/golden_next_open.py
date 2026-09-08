"""The synthetic next-open parity fixture, ``golden/next_open/``.

    uv run python scripts/golden_next_open.py

Two symbols over twelve business days, built by hand to exercise everything
the next-open fill has to get right (docs/CONTRACTS.md, Execution): a gap up
and a gap down between a close and the next open, a 2-for-1 split in A's raw
prices that the adjustment factor must undo, target flips, a bar whose target
repeats the previous one (a no-op fill), and a final signal that is never
filled. The Python engine writes the equity curve; the Go engine's
``TestNextOpenGoldenParity`` reproduces it to 1e-6. No network, no
randomness: the numbers are the same on every machine.
"""

from __future__ import annotations

import datetime as dt
import sys
from pathlib import Path

import numpy as np
import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt.backtest.engine import CostModel, Fill, run
from tt.data import adjust
from tt.data import bars as barsio
from tt.data.checks import check
from tt.spec import REPO_ROOT

OUT = REPO_ROOT / "golden" / "next_open"
FETCHED = pd.Timestamp("2026-09-08T00:00:00Z")


def symbol_a() -> pd.DataFrame:
    """Raw prices halve at bar 6 (a 2-for-1 split); adjclose does not."""
    dates = pd.bdate_range(dt.date(2024, 1, 1), periods=12).date
    adj = [100.0, 102, 101, 108, 107, 104, 105, 110, 109, 103, 104, 106]
    adjopen = [99, 101, 104, 103, 107.5, 106, 104, 106, 112, 108, 103, 104]
    raw = [2.0] * 6 + [1.0] * 6  # close / adjclose before and after the split
    close = [a * r for a, r in zip(adj, raw, strict=True)]
    open_ = [o * r for o, r in zip(adjopen, raw, strict=True)]
    high = [max(o, c) * 1.01 for o, c in zip(open_, close, strict=True)]
    low = [min(o, c) * 0.99 for o, c in zip(open_, close, strict=True)]
    return frame(dates, open_, high, low, close, adj)


def symbol_b() -> pd.DataFrame:
    """A quieter series with no adjustment at all."""
    dates = pd.bdate_range(dt.date(2024, 1, 1), periods=12).date
    close = [50.0, 50.5, 50.2, 49, 49.5, 51, 51.5, 51, 52, 52.5, 52, 53]
    open_ = [50, 50.2, 50.6, 50.1, 48.8, 49.6, 51.2, 51.4, 51.1, 52.2, 52.4, 52.1]
    high = [max(o, c) * 1.005 for o, c in zip(open_, close, strict=True)]
    low = [min(o, c) * 0.995 for o, c in zip(open_, close, strict=True)]
    return frame(dates, open_, high, low, close, close)


def frame(
    dates: np.ndarray, open_: list[float], high: list[float], low: list[float],
    close: list[float], adj: list[float],
) -> pd.DataFrame:
    df = pd.DataFrame({
        "date": dates, "open": open_, "high": high, "low": low, "close": close, "adjclose": adj,
        "volume": [1000] * len(dates), "source": "synthetic", "fetched_at": FETCHED,
    })
    df = barsio.normalize(df)
    check(df)
    return df


def weights(n: int) -> pd.DataFrame:
    """Flips, a repeat, a flat stretch, and a last-bar signal nothing fills."""
    a = [0.6, 0.6, 0.0, -0.4, -0.4, 0.5, 0.5, 0.0, 0.0, 0.7, 0.3, 1.0]
    b = [0.4, 0.4, 0.9, 0.5, 0.5, 0.5, 0.5, 0.0, 0.0, 0.3, 0.7, 0.0]
    assert len(a) == len(b) == n
    return pd.DataFrame({"A": a, "B": b})


def main() -> int:
    bars = {"A": symbol_a(), "B": symbol_b()}
    universe = ["A", "B"]
    prices = pd.DataFrame({"date": bars["A"]["date"], "A": bars["A"]["adjclose"], "B": bars["B"]["adjclose"]})
    opens = adjust.aligned(bars, universe, "open")
    w = weights(len(prices))
    res = run(prices, w, CostModel(commission_usd=1.0, slippage_bps=10.0), fill=Fill.NEXT_OPEN, opens=opens)

    OUT.mkdir(parents=True, exist_ok=True)
    for s, df in bars.items():
        barsio.write_bars(df, OUT / f"bars_{s}.parquet")
    long = pd.concat(
        pd.DataFrame({"date": prices["date"], "symbol": s, "target_weight": w[s]}) for s in universe
    ).sort_values(["date", "symbol"])
    long.to_csv(OUT / "weights.csv", index=False, float_format="%.17g")
    res.equity.to_csv(OUT / "equity_curve.csv", index=False, float_format="%.17g")
    (OUT / "README.md").write_text(
        "# golden/next_open\n\nParity fixture for the `next_open` fill (docs/CONTRACTS.md, Execution):\n"
        "two hand-built symbols with a gap up, a gap down, a 2-for-1 split in A's raw\n"
        "prices, target flips, a repeated target and a final signal that is never\n"
        "filled. `weights.csv` is the input, `equity_curve.csv` the Python engine's\n"
        "output at 1 USD commission and 10 bps slippage; the Go engine must match it\n"
        f"to 1e-6.\n\nWritten by research/scripts/golden_next_open.py on {dt.datetime.now(dt.UTC):%Y-%m-%d}.\n"
    )
    print(f"wrote {OUT}: {len(res.equity)} bars, {len(res.trades)} trades")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
