"""The ETF/GLD ratio study: sweep on train, judge out of sample, emit a spec and goldens.

    uv run python scripts/etf_gld_ratio.py                 # fetch from Yahoo
    uv run python scripts/etf_gld_ratio.py --csv-dir DIR   # Kaggle-format CSVs, no network

The idea under test (docs/strategies/etf_gld_ratio.md): hold a broad equity
ETF while its price ratio to GLD is stretched below the ratio's own trailing
mean, and hold GLD otherwise. Parameters are chosen on the training window
alone, from a fixed grid, preferring configurations that trade about one to
two times a week across the four sleeves; the test window is touched once.

The spec is promoted to specs/etf_gld_ratio.yaml only when the out-of-sample
Sharpe clears the runtime's floor of 1.0 on data fetched from Yahoo;
otherwise it lands in research/out/ as a rejected spec. Goldens are written
either way: parity does not care whether the strategy is any good.
"""

from __future__ import annotations

import argparse
import datetime as dt
import itertools
import sys
from dataclasses import dataclass
from pathlib import Path

import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt.backtest import metrics
from tt.backtest.engine import CostModel, Result, run
from tt.data import bars as barsio
from tt.data.checks import check
from tt.data.yahoo import fetch
from tt.spec import REPO_ROOT, build_spec, write_golden, write_spec
from tt.stats.kelly import half_kelly
from tt.strategies import ratio

NAME = "etf_gld_ratio"
UNIVERSE = ["SPY", "QQQ", "VTI", "XLK", "GLD"]
START = dt.date(2005, 1, 1)  # GLD listed 2004-11; VTI, XLK, QQQ, SPY are older
COSTS = CostModel(commission_usd=0.0, slippage_bps=5.0)
MAX_LEG_USD = 2000.0
NO_TIME_STOP = 9999

# The grid is fixed before the data is looked at. Lookbacks span a fortnight
# to half a year; the bands are in z units; the time stop is either off or
# about three half-lives of a deviation from an L-bar mean (~0.4 L).
LOOKBACKS = [10, 20, 40, 60, 90, 120]
ENTRIES = [0.5, 1.0, 1.5, 2.0]
EXITS = [-0.5, 0.0, 0.5, 1.0]
# One to two trades a week: each sleeve switch is a sell and a buy, so 40..120
# switches a year across the four sleeves is 80..240 orders, 1.5..4.5 a week.
BAND = (40.0, 120.0)
# The test window has to be long enough for its Sharpe to mean something. A
# year of daily returns is the least the runtime's floor should be cleared
# on; four days of them annualize to any number at all.
MIN_TEST_BARS = 252


@dataclass(frozen=True)
class Trial:
    """One configuration's showing on a window."""

    params: ratio.Params
    sharpe: float
    cagr: float
    max_drawdown: float
    switches_per_year: float
    result: Result


def _slice(df: pd.DataFrame, lo: dt.date, hi: dt.date) -> pd.DataFrame:
    d = pd.to_datetime(df["date"]).dt.date
    return df[(d >= lo) & (d <= hi)].reset_index(drop=True)


def load_csv_dir(directory: Path, fetched_at: pd.Timestamp) -> dict[str, pd.DataFrame]:
    """Read ``SYMBOL.csv`` files in the Kaggle "Huge Stock Market Dataset" shape.

    Columns ``Date,Open,High,Low,Close,Volume[,OpenInt]``; the prices there are
    already adjusted for splits and dividends, so ``adjclose`` is ``Close``.
    """
    out: dict[str, pd.DataFrame] = {}
    for s in UNIVERSE:
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


def backtest(
    bars: dict[str, pd.DataFrame], params: ratio.Params, gross: float, start: dt.date
) -> Trial:
    """Replay over everything given, open the book at ``start``."""
    tr = ratio.replay(bars, UNIVERSE, params, gross)
    al = ratio.align(bars, UNIVERSE)
    keep = (pd.to_datetime(al["date"]).dt.date >= start).to_numpy()
    prices = al[keep].reset_index(drop=True)
    weights = ratio.weights_frame(tr, UNIVERSE)[keep].reset_index(drop=True)
    res = run(prices, weights, COSTS)
    eq = res.equity["equity"].to_numpy()
    years = len(eq) / metrics.TRADING_DAYS
    return Trial(
        params=params,
        sharpe=metrics.sharpe(metrics.daily_returns(eq)),
        cagr=float((eq[-1] / eq[0]) ** (1 / years) - 1) if years > 0 else float("nan"),
        max_drawdown=metrics.max_drawdown(eq),
        switches_per_year=ratio.switches(tr[keep].reset_index(drop=True), UNIVERSE) / years,
        result=res,
    )


def hold(bars: dict[str, pd.DataFrame], symbol: str, start: dt.date) -> tuple[float, float]:
    """Buy-and-hold one symbol through the same engine: Sharpe and max drawdown."""
    al = ratio.align(bars, UNIVERSE)
    keep = (pd.to_datetime(al["date"]).dt.date >= start).to_numpy()
    prices = al[keep].reset_index(drop=True)
    w = pd.DataFrame({s: [1.0 if s == symbol else 0.0] * len(prices) for s in UNIVERSE})
    eq = run(prices, w, COSTS).equity["equity"].to_numpy()
    return metrics.sharpe(metrics.daily_returns(eq)), metrics.max_drawdown(eq)


def grid() -> list[ratio.Params]:
    out: list[ratio.Params] = []
    for lb, ez, xz, stop in itertools.product(LOOKBACKS, ENTRIES, EXITS, (False, True)):
        if not xz > -ez:
            continue
        hold_days = max(5, round(0.4 * lb)) if stop else NO_TIME_STOP
        out.append(ratio.Params(lb, ez, xz, hold_days))
    return out


def choose(trials: list[Trial]) -> tuple[Trial, str]:
    """The best train Sharpe inside the trade-frequency band, else the best overall."""
    lo, hi = BAND
    in_band = [t for t in trials if lo <= t.switches_per_year <= hi]
    if in_band:
        return max(in_band, key=lambda t: t.sharpe), f"best train Sharpe with {lo:.0f}..{hi:.0f} switches/yr"
    return max(trials, key=lambda t: t.sharpe), "best train Sharpe (nothing in the frequency band)"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--csv-dir", type=Path, help="Kaggle-format SYMBOL.csv files instead of Yahoo")
    ap.add_argument("--train-to", type=dt.date.fromisoformat, default=dt.date(2013, 12, 31),
                    help="last day of the training window (default 2013-12-31)")
    ap.add_argument("--golden", default=str(REPO_ROOT / "golden" / NAME))
    # Where the outputs go. The defaults are the checkout's; the app points
    # them at the runtime's own directories when it runs this.
    ap.add_argument("--specs-dir", type=Path, default=REPO_ROOT / "specs",
                    help="where a promoted spec is written")
    ap.add_argument("--bars-dir", type=Path, default=REPO_ROOT / "research" / "data" / "bars",
                    help="where the fetched bars are written as Parquet")
    ap.add_argument("--out-dir", type=Path, default=REPO_ROOT / "research" / "out",
                    help="where a rejected spec is written")
    args = ap.parse_args()

    data_dir = args.bars_dir
    if args.csv_dir:
        bars = load_csv_dir(args.csv_dir, pd.Timestamp.now(tz="UTC"))
        source_note = f"Kaggle 'Huge Stock Market Dataset' CSVs from {args.csv_dir.name}/ (adjusted; ends 2017-11)"
        real = False
    else:
        bars = fetch(UNIVERSE, START)
        source_note = "Yahoo Finance via yfinance"
        real = True
    for s, df in bars.items():
        barsio.write_bars(df, data_dir / f"{s}.parquet")
    aligned = ratio.align(bars, UNIVERSE)
    first = pd.Timestamp(aligned["date"].iloc[0]).date()
    last = pd.Timestamp(aligned["date"].iloc[-1]).date()
    train = (first, args.train_to)
    test = (args.train_to + dt.timedelta(days=1), last)
    if not first < train[1] < last:
        print(f"train_to {train[1]} must fall inside the data {first}..{last}", file=sys.stderr)
        return 2
    test_bars = int((pd.to_datetime(aligned["date"]).dt.date >= test[0]).sum())
    if test_bars < MIN_TEST_BARS:
        print(f"REFUSED: the test window {test[0]}..{test[1]} holds {test_bars} bars; at least "
              f"{MIN_TEST_BARS} (about a year) are needed before an out-of-sample Sharpe means "
              f"anything. Move --train-to earlier.", file=sys.stderr)
        return 2
    print(f"data: {source_note}; {len(aligned)} aligned bars {first}..{last}")
    print(f"train {train[0]}..{train[1]}, test {test[0]}..{test[1]}, costs {COSTS}")

    # Train: the fixed grid, judged on the training window alone.
    train_bars = {s: _slice(df, *train) for s, df in bars.items()}
    trials = [backtest(train_bars, p, 1.0, train[0]) for p in grid()]
    best, why = choose(trials)
    ranked = sorted(trials, key=lambda t: t.sharpe, reverse=True)
    print(f"\ntrain: {len(trials)} configurations; top 8 by Sharpe:")
    for t in ranked[:8]:
        p = t.params
        print(f"  L={p.lookback:3d} entry={p.entry_z:.1f} exit={p.exit_z:+.1f} stop={p.max_hold_days:4d}: "
              f"Sharpe {t.sharpe:.2f} CAGR {t.cagr:.1%} maxDD {t.max_drawdown:.1%} "
              f"switches/yr {t.switches_per_year:.0f}")
    print("train buy-and-hold Sharpe: " + ", ".join(f"{s} {hold(train_bars, s, train[0])[0]:.2f}" for s in UNIVERSE))
    print(f"share of configurations with train Sharpe >= 1.0: {sum(t.sharpe >= 1 for t in trials) / len(trials):.1%}")
    p = best.params
    print(f"\nchosen ({why}): L={p.lookback} entry={p.entry_z} exit={p.exit_z} max_hold={p.max_hold_days}; "
          f"train Sharpe {best.sharpe:.2f}, {best.switches_per_year:.0f} switches/yr")

    # Sizing: half-Kelly from the training returns, capped at 1.0 because the
    # book is long-only and unlevered by construction.
    r = metrics.daily_returns(best.result.equity["equity"].to_numpy())
    var = float(r.var(ddof=1))
    gross = half_kelly(float(r.mean()), var, cap=1.0) if var > 0 else 1.0
    gross = max(gross, 0.1)
    print(f"half-Kelly leverage {gross:.3f} (full Kelly {float(r.mean()) / var if var > 0 else float('nan'):.2f})")

    # Test: replay over the whole history so the sleeves are warm; the book
    # opens on the first test bar. This is the one look at the test window.
    oos = backtest(bars, p, gross, test[0])
    print(f"\ntest {test[0]}..{test[1]}: Sharpe {oos.sharpe:.3f}, CAGR {oos.cagr:.1%}, "
          f"max DD {oos.max_drawdown:.1%}, {oos.switches_per_year:.0f} switches/yr, "
          f"{len(oos.result.trades)} orders")
    print("test buy-and-hold Sharpe: " + ", ".join(f"{s} {hold(bars, s, test[0])[0]:.2f}" for s in UNIVERSE))

    spec = build_spec(
        name=NAME, strategy=ratio.NAME, universe=UNIVERSE,
        params={"lookback": p.lookback, "entry_z": p.entry_z, "exit_z": p.exit_z,
                "max_hold_days": p.max_hold_days},
        gross_leverage=gross, max_notional_per_leg_usd=MAX_LEG_USD,
        train_window=train, test_window=test,
        oos_sharpe=float(oos.sharpe), oos_max_drawdown=float(oos.max_drawdown),
        commission_usd=COSTS.commission_usd, slippage_bps=COSTS.slippage_bps,
    )
    if oos.sharpe >= 1.0 and real:
        out = args.specs_dir / f"{NAME}.yaml"
        write_spec(spec, out)
        print(f"PROMOTED: wrote {out}")
    else:
        out = args.out_dir / f"{NAME}.rejected.yaml"
        write_spec(spec, out)
        why_not = f"OOS Sharpe {oos.sharpe:.2f} < 1.0" if oos.sharpe < 1.0 else "not Yahoo data"
        print(f"NOT PROMOTED ({why_not}): wrote {out}")

    # Goldens: every aligned date's signals over the whole history; the equity
    # curve from the test window on.
    sig = ratio.signals(bars, UNIVERSE, p, gross)
    m = metrics.summary(oos.result.equity["equity"].to_numpy())
    note = (
        f"# golden/{NAME}\n\nParity fixtures for `{ratio.NAME}` (docs/PLAN.md §4.3, §4.4).\n\n"
        f"Source: {source_note}.\nWritten by research/scripts/{NAME}.py on "
        f"{dt.datetime.now(dt.UTC):%Y-%m-%d}.\n\n"
        "Regenerate with `make golden-ratio` and commit the result together with any\n"
        "change to docs/CONTRACTS.md or either implementation.\n"
    )
    files = write_golden(Path(args.golden), bars=bars, spec=spec, signals=sig,
                         equity=oos.result.equity, metrics=m, note=note)
    for f in files:
        print("wrote", f)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
