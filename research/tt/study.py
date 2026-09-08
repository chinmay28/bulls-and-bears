"""What every study script shares: its flags, its data, its windows, its verdict.

A study is a script under ``research/scripts`` that fetches bars, chooses
parameters on a training window, judges them once out of sample, and emits a
spec the runtime may run plus the goldens that hold the Go implementation to
this one. The parts that are the same for every study live here so that a
new study is only its sweep.
"""

from __future__ import annotations

import argparse
import datetime as dt
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import pandas as pd

from tt.backtest import metrics
from tt.backtest.engine import CostModel, Fill, Result, run
from tt.data import adjust
from tt.data import bars as barsio
from tt.data.checks import check
from tt.data.yahoo import fetch
from tt.spec import REPO_ROOT, build_spec, write_golden, write_spec

# The test window has to be long enough for its Sharpe to mean something. A
# year of daily returns is the least the runtime's floor should be cleared on;
# a handful of them annualize to any number at all.
MIN_TEST_BARS = 252

COSTS = CostModel(commission_usd=0.0, slippage_bps=5.0)
MAX_LEG_USD = 2000.0


def parse_args(
    description: str, *, default_universe: list[str], default_train_to: dt.date
) -> argparse.Namespace:
    """The flags every study takes."""
    ap = argparse.ArgumentParser(description=description)
    ap.add_argument("--csv-dir", type=Path, help="Kaggle-format SYMBOL.csv files instead of Yahoo")
    ap.add_argument("--universe", type=symbols, default=default_universe,
                    help="comma-separated symbols, the haven last (default %(default)s)")
    ap.add_argument("--train-to", type=dt.date.fromisoformat, default=default_train_to,
                    help="last day of the training window (default %(default)s)")
    ap.add_argument("--golden", type=Path, help="parity fixtures directory (default golden/<name>)")
    # Where the outputs go. The defaults are the checkout's; the app points
    # them at the runtime's own directories when it runs this.
    ap.add_argument("--specs-dir", type=Path, default=REPO_ROOT / "specs",
                    help="where a promoted spec is written")
    ap.add_argument("--bars-dir", type=Path, default=REPO_ROOT / "research" / "data" / "bars",
                    help="where the fetched bars are written as Parquet")
    ap.add_argument("--out-dir", type=Path, default=REPO_ROOT / "research" / "out",
                    help="where a rejected spec is written")
    return ap.parse_args()


def symbols(text: str) -> list[str]:
    """Parse ``SPY,QQQ,GLD``; upper-cased, no repeats, at least two."""
    out = [s.strip().upper() for s in text.replace(" ", ",").split(",") if s.strip()]
    if len(out) < 2:
        raise argparse.ArgumentTypeError("a universe needs at least two symbols")
    if len(set(out)) != len(out):
        raise argparse.ArgumentTypeError("a universe cannot repeat a symbol")
    return out


def spec_name(base: str, universe: list[str], default_universe: list[str]) -> str:
    """``base`` for the study's own universe; ``base_spy_gld`` for another."""
    if universe == default_universe:
        return base
    return base + "_" + "_".join(s.lower().replace(".", "_") for s in universe)


def load_csv_dir(directory: Path, universe: list[str], fetched_at: pd.Timestamp) -> dict[str, pd.DataFrame]:
    """Read ``SYMBOL.csv`` files in the Kaggle "Huge Stock Market Dataset" shape.

    Columns ``Date,Open,High,Low,Close,Volume[,OpenInt]``; the prices there are
    already adjusted for splits and dividends, so ``adjclose`` is ``Close``.
    """
    out: dict[str, pd.DataFrame] = {}
    for s in universe:
        raw = pd.read_csv(directory / f"{s}.csv", parse_dates=["Date"])
        df = pd.DataFrame(
            {
                "date": raw["Date"].dt.date,
                "open": raw["Open"].astype(float),
                "high": raw["High"].astype(float),
                "low": raw["Low"].astype(float),
                "close": raw["Close"].astype(float),
                "adjclose": raw["Close"].astype(float),
                "volume": raw["Volume"].fillna(0).astype("int64"),
                "source": "kaggle",
                "fetched_at": fetched_at,
            }
        )
        df = barsio.normalize(df)
        check(df)
        out[s] = df
    return out


@dataclass(frozen=True)
class Data:
    """The bars a study runs on, and where they came from."""

    bars: dict[str, pd.DataFrame]
    source_note: str
    # real says the bars came from Yahoo: only then may a spec be promoted.
    real: bool


def load(args: argparse.Namespace, universe: list[str], start: dt.date) -> Data:
    """Fetch from Yahoo, or read the CSV directory, and write the bars out."""
    if args.csv_dir:
        bars = load_csv_dir(args.csv_dir, universe, pd.Timestamp.now(tz="UTC"))
        data = Data(bars, f"Kaggle 'Huge Stock Market Dataset' CSVs from {args.csv_dir.name}/ (adjusted)", False)
    else:
        data = Data(fetch(universe, start), "Yahoo Finance via yfinance", True)
    for s, df in data.bars.items():
        barsio.write_bars(df, args.bars_dir / f"{s}.parquet")
    return data


@dataclass(frozen=True)
class Windows:
    """Train and test, and how many aligned bars the test holds."""

    train: tuple[dt.date, dt.date]
    test: tuple[dt.date, dt.date]
    test_bars: int


def windows(aligned_dates: pd.Series, train_to: dt.date) -> Windows | None:
    """Split the aligned dates at ``train_to``; None, with the reason printed, when unusable."""
    d = pd.to_datetime(aligned_dates).dt.date
    first, last = d.iloc[0], d.iloc[-1]
    if not first < train_to < last:
        print(f"train_to {train_to} must fall inside the data {first}..{last}", file=sys.stderr)
        return None
    test_from = train_to + dt.timedelta(days=1)
    test_bars = int((d >= test_from).sum())
    if test_bars < MIN_TEST_BARS:
        print(f"REFUSED: the test window {test_from}..{last} holds {test_bars} bars; at least "
              f"{MIN_TEST_BARS} (about a year) are needed before an out-of-sample Sharpe means "
              f"anything. Move --train-to earlier.", file=sys.stderr)
        return None
    return Windows((first, train_to), (test_from, last), test_bars)


def slice_window(df: pd.DataFrame, lo: dt.date, hi: dt.date) -> pd.DataFrame:
    """The bars within [lo, hi]."""
    d = pd.to_datetime(df["date"]).dt.date
    return df[(d >= lo) & (d <= hi)].reset_index(drop=True)


def opens_for(prices: pd.DataFrame, bars: dict[str, pd.DataFrame], universe: list[str]) -> pd.DataFrame:
    """The adjusted opens on exactly the dates of an aligned ``prices`` frame."""
    o = adjust.aligned(bars, universe, "open")
    out = prices[["date"]].merge(o, on="date", how="left")
    if out[universe].isna().any().any():
        raise ValueError("opens_for: a bar has no open on an aligned date")
    return out.reset_index(drop=True)


def backtest_weights(
    prices: pd.DataFrame,
    weights: pd.DataFrame,
    start: dt.date,
    *,
    fill: Fill,
    opens: pd.DataFrame | None = None,
    costs: CostModel = COSTS,
) -> Result:
    """Open the book at ``start`` and run the engine over aligned prices and weights.

    ``opens`` (from ``opens_for``) is needed for ``Fill.NEXT_OPEN``.
    """
    keep = (pd.to_datetime(prices["date"]).dt.date >= start).to_numpy()
    o = opens[keep].reset_index(drop=True) if opens is not None else None
    return run(prices[keep].reset_index(drop=True), weights[keep].reset_index(drop=True), costs,
               fill=fill, opens=o)


def hold(
    prices: pd.DataFrame, universe: list[str], symbol: str, start: dt.date, *,
    fill: Fill = Fill.SAME_CLOSE, opens: pd.DataFrame | None = None,
) -> float:
    """Buy-and-hold one symbol through the same engine: its Sharpe."""
    w = pd.DataFrame({s: [1.0 if s == symbol else 0.0] * len(prices) for s in universe})
    eq = backtest_weights(prices, w, start, fill=fill, opens=opens).equity["equity"].to_numpy()
    return metrics.sharpe(metrics.daily_returns(eq))


def summary(res: Result) -> dict[str, float | int]:
    """Sharpe, drawdown and duration of a result's equity curve."""
    return metrics.summary(res.equity["equity"].to_numpy())


def emit(
    *,
    args: argparse.Namespace,
    name: str,
    strategy: str,
    universe: list[str],
    params: dict[str, float],
    gross: float,
    win: Windows,
    oos: Result,
    signals: pd.DataFrame,
    data: Data,
    script: str,
    fill: Fill,
    study: str,
) -> int:
    """Write the spec where it belongs and the goldens; return the exit code.

    ``fill`` is the execution model ``oos`` was run with and ``study`` the
    name the app knows this script by; both go into the spec.
    """
    m = summary(oos)
    spec = build_spec(
        name=name, strategy=strategy, universe=universe, params=params,
        gross_leverage=gross, max_notional_per_leg_usd=MAX_LEG_USD,
        train_window=win.train, test_window=win.test,
        oos_sharpe=float(m["sharpe"]), oos_max_drawdown=float(m["max_drawdown"]),
        commission_usd=COSTS.commission_usd, slippage_bps=COSTS.slippage_bps,
        fill_at=fill.value, research_study=study,
    )
    if m["sharpe"] >= 1.0 and data.real:
        out = args.specs_dir / f"{name}.yaml"
        write_spec(spec, out)
        print(f"PROMOTED: wrote {out}")
    else:
        out = args.out_dir / f"{name}.rejected.yaml"
        write_spec(spec, out)
        why = f"OOS Sharpe {m['sharpe']:.2f} < 1.0" if m["sharpe"] < 1.0 else "not Yahoo data"
        print(f"NOT PROMOTED ({why}): wrote {out}")

    golden = args.golden or REPO_ROOT / "golden" / name
    note = (
        f"# golden/{name}\n\nParity fixtures for `{strategy}` (docs/PLAN.md §4.3, §4.4).\n\n"
        f"Source: {data.source_note}.\nWritten by research/scripts/{script} on "
        f"{dt.datetime.now(dt.UTC):%Y-%m-%d}.\n\n"
        "Regenerate with the script (or the app's Research tab) and commit the result\n"
        "together with any change to docs/CONTRACTS.md or either implementation.\n"
    )
    files = write_golden(Path(golden), bars=data.bars, spec=spec, signals=signals,
                         equity=oos.equity, metrics=m, note=note)
    for f in files:
        print("wrote", f)
    return 0


def describe(label: str, res: Result, extra: dict[str, Any] | None = None) -> str:
    """One line of numbers for a window."""
    m = summary(res)
    eq = res.equity["equity"].to_numpy()
    years = len(eq) / metrics.TRADING_DAYS
    cagr = (eq[-1] / eq[0]) ** (1 / years) - 1 if years > 0 else float("nan")
    text = (f"{label}: Sharpe {m['sharpe']:.3f}, CAGR {cagr:.1%}, max DD {m['max_drawdown']:.1%}, "
            f"DD duration {m['max_drawdown_duration']} bars")
    for k, v in (extra or {}).items():
        text += f", {k} {v}"
    return text
