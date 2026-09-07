"""Reproduce the GLD/GDX pairs study and emit a spec plus goldens.

    uv run python scripts/gld_gdx.py              # fetch from Yahoo
    uv run python scripts/gld_gdx.py --synthetic  # seeded pair, no network

The spec is promoted to specs/gld_gdx_pairs.yaml only when the out-of-sample
Sharpe clears the runtime's floor of 1.0; otherwise it lands in research/out/
as a rejected spec. Goldens are written either way: parity does not care
whether the strategy is any good.
"""

from __future__ import annotations

import argparse
import datetime as dt
import sys
from pathlib import Path

import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt.backtest import metrics
from tt.backtest.engine import CostModel, run_pairs
from tt.data import bars as barsio
from tt.data.synthetic import cointegrated_pair
from tt.data.yahoo import fetch
from tt.spec import REPO_ROOT, build_spec, write_golden, write_spec
from tt.stats.cointegration import engle_granger
from tt.stats.halflife import halflife_ar1
from tt.stats.kelly import half_kelly
from tt.strategies import pairs

NAME = "gld_gdx_pairs"
UNIVERSE = ["GLD", "GDX"]
TRAIN = (dt.date(2015, 1, 1), dt.date(2022, 12, 31))
COSTS = CostModel(commission_usd=0.0, slippage_bps=5.0)
LOOKBACK, ENTRY, EXIT = 20, 2.0, 0.5
MAX_LEG_USD = 2000.0


def _slice(df: pd.DataFrame, lo: dt.date, hi: dt.date) -> pd.DataFrame:
    d = pd.to_datetime(df["date"]).dt.date
    return df[(d >= lo) & (d <= hi)].reset_index(drop=True)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--synthetic", action="store_true", help="use the seeded pair, no network")
    ap.add_argument("--golden", default=str(REPO_ROOT / "golden" / "gld_gdx"))
    args = ap.parse_args()

    data_dir = REPO_ROOT / "research" / "data" / "bars"
    if args.synthetic:
        syn = cointegrated_pair(n=2200, seed=7, hedge=1.6)
        bars = {"GLD": syn["A"], "GDX": syn["B"]}
        source_note = "SYNTHETIC (seeded, tt.data.synthetic.cointegrated_pair(n=2200, seed=7))"
    else:
        bars = fetch(UNIVERSE, TRAIN[0])
        for s, df in bars.items():
            barsio.write_bars(df, data_dir / f"{s}.parquet")
        source_note = "Yahoo Finance via yfinance"
    last = min(pd.Timestamp(bars[s]["date"].iloc[-1]).date() for s in UNIVERSE)
    test = (dt.date(2023, 1, 1), last)

    # Train: cointegration, hedge ratio, half-life.
    train = pairs.align({s: _slice(bars[s], *TRAIN) for s in UNIVERSE}, UNIVERSE)
    eg = engle_granger(train["a"].to_numpy(), train["b"].to_numpy())
    hl = halflife_ar1(eg.residual)
    max_hold = int(min(60, max(5, round(3 * hl)))) if hl != float("inf") else 30
    params = pairs.Params(eg.hedge_ratio, LOOKBACK, ENTRY, EXIT, max_hold)
    print(f"train {TRAIN[0]}..{TRAIN[1]}: hedge {eg.hedge_ratio:.4f}, ADF p {eg.adf.pvalue:.4f}, "
          f"cointegrated@5% {eg.cointegrated()}, half-life {hl:.1f} bars, max_hold {max_hold}")

    # Sizing from train returns at full leverage, then half-Kelly capped at 0.9.
    tr_train = pairs.replay({s: _slice(bars[s], *TRAIN) for s in UNIVERSE}, UNIVERSE, params, 1.0)
    res_train = run_pairs(tr_train, train, UNIVERSE, COSTS)
    r = metrics.daily_returns(res_train.equity["equity"].to_numpy())
    gross = half_kelly(float(r.mean()), float(r.var(ddof=1)), cap=0.9) if r.var(ddof=1) > 0 else 0.9
    gross = max(gross, 0.1)
    print(f"train Sharpe {metrics.sharpe(r):.2f}; half-Kelly leverage {gross:.3f}")

    # Test: out of sample, warm start from the tail of train.
    warm = TRAIN[1] - dt.timedelta(days=LOOKBACK * 2)
    test_bars = {s: _slice(bars[s], warm, test[1]) for s in UNIVERSE}
    tr = pairs.replay(test_bars, UNIVERSE, params, gross)
    al = pairs.align(test_bars, UNIVERSE)
    keep = (pd.to_datetime(al["date"]).dt.date >= test[0]).to_numpy()
    res = run_pairs(tr[keep].reset_index(drop=True), al[keep].reset_index(drop=True), UNIVERSE, COSTS)
    eq = res.equity["equity"].to_numpy()
    m = metrics.summary(eq)
    print(f"test {test[0]}..{test[1]}: Sharpe {m['sharpe']:.3f}, max DD {m['max_drawdown']:.3%}, "
          f"DD duration {m['max_drawdown_duration']} bars, trades {len(res.trades)}")

    spec = build_spec(
        name=NAME, strategy=pairs.NAME, universe=UNIVERSE,
        params={"hedge_ratio": params.hedge_ratio, "lookback": LOOKBACK, "entry_z": ENTRY,
                "exit_z": EXIT, "max_hold_days": max_hold},
        gross_leverage=gross, max_notional_per_leg_usd=MAX_LEG_USD,
        train_window=TRAIN, test_window=test,
        oos_sharpe=float(m["sharpe"]), oos_max_drawdown=float(m["max_drawdown"]),
        commission_usd=COSTS.commission_usd, slippage_bps=COSTS.slippage_bps,
    )
    if m["sharpe"] >= 1.0 and not args.synthetic:
        out = REPO_ROOT / "specs" / f"{NAME}.yaml"
        write_spec(spec, out)
        print(f"PROMOTED: wrote {out}")
    else:
        out = REPO_ROOT / "research" / "out" / f"{NAME}.rejected.yaml"
        write_spec(spec, out)
        why = "synthetic data" if args.synthetic else f"OOS Sharpe {m['sharpe']:.2f} < 1.0"
        print(f"NOT PROMOTED ({why}): wrote {out}")

    # Goldens: the whole test-window replay, signals for every aligned date.
    golden_bars = {s: _slice(bars[s], warm, test[1]) for s in UNIVERSE}
    sig = pairs.signals(golden_bars, UNIVERSE, params, gross)
    note = (
        f"# golden/gld_gdx\n\nParity fixtures for `pairs_zscore` (docs/PLAN.md §4.3, §4.4).\n\n"
        f"Source: {source_note}.\nWritten by research/scripts/gld_gdx.py on "
        f"{dt.datetime.now(dt.UTC):%Y-%m-%d}.\n\n"
        "Regenerate with `make golden` and commit the result together with any\n"
        "change to docs/CONTRACTS.md or either implementation.\n"
    )
    files = write_golden(Path(args.golden), bars=golden_bars, spec=spec, signals=sig,
                         equity=res.equity, metrics=m, note=note)
    for f in files:
        print("wrote", f)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
