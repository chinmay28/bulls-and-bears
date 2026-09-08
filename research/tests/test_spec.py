import datetime as dt
from pathlib import Path

import jsonschema
import pytest
import yaml

from tt.backtest import metrics
from tt.backtest.engine import CostModel, run_pairs
from tt.data.synthetic import cointegrated_pair
from tt.spec import build_spec, validate, write_golden, write_spec
from tt.strategies import pairs


def a_spec(**over):
    kw = dict(
        name="gld_gdx_pairs", strategy="pairs_zscore", universe=["GLD", "GDX"],
        params={"hedge_ratio": 1.631, "lookback": 20, "entry_z": 2.0, "exit_z": 0.5,
                "max_hold_days": 30},
        gross_leverage=0.9, max_notional_per_leg_usd=2000,
        train_window=(dt.date(2015, 1, 1), dt.date(2022, 12, 31)),
        test_window=(dt.date(2023, 1, 1), dt.date(2026, 8, 31)),
        oos_sharpe=1.32, oos_max_drawdown=-0.087, commission_usd=0, slippage_bps=5,
        generated_at=dt.datetime(2026, 9, 6, tzinfo=dt.UTC), research_git_sha="abc1234",
        fill_at="same_close_legacy",
    )
    kw.update(over)
    return build_spec(**kw)


def test_spec_validates_and_round_trips(tmp_path: Path) -> None:
    spec = a_spec()
    validate(spec)
    p = tmp_path / "s.yaml"
    write_spec(spec, p)
    back = yaml.safe_load(p.read_text())
    assert back == spec
    assert back["provenance"]["generated_at"] == "2026-09-06T00:00:00Z"


def test_every_registered_strategy_name_validates() -> None:
    validate(a_spec(strategy="pairs_zscore"))
    validate(a_spec(name="sma", strategy="sma_trend", universe=["SPY", "GLD"],
                    params={"lookback": 200, "band": 0.01}))
    validate(a_spec(name="dm", strategy="dual_momentum", universe=["SPY", "QQQ", "GLD"],
                    params={"lookback": 252, "top_k": 1, "rebalance_days": 21}))
    validate(a_spec(name="tsm", strategy="time_series_momentum", universe=["SPY", "GLD"],
                    params={"lookback": 252, "rebalance_days": 21}, fill_at="next_open",
                    research_study="time_series_momentum"))
    validate(a_spec(name="dc", strategy="donchian_breakout", universe=["SPY", "GLD"],
                    params={"entry_lookback": 55, "exit_lookback": 20}, fill_at="next_open"))
    validate(a_spec(name="rp", strategy="risk_parity_trend", universe=["SPY", "GLD"],
                    params={"trend_lookback": 200, "vol_lookback": 63, "rebalance_days": 21},
                    fill_at="next_open"))
    validate(a_spec(name="rsi2", strategy="rsi2_reversion", universe=["SPY", "GLD"],
                    params={"trend_lookback": 200, "rsi_entry": 5, "rsi_exit": 70, "max_hold_days": 5},
                    fill_at="next_open"))
    validate(a_spec(name="etf_gld_ratio", strategy="ratio_reversion",
                    universe=["SPY", "QQQ", "VTI", "XLK", "GLD"],
                    params={"lookback": 20, "entry_z": 2.0, "exit_z": 0.5, "max_hold_days": 8}))


def test_invalid_spec_is_refused_before_writing(tmp_path: Path) -> None:
    spec = a_spec(strategy="momentum_of_the_week")
    with pytest.raises(jsonschema.ValidationError):
        write_spec(spec, tmp_path / "s.yaml")
    assert not (tmp_path / "s.yaml").exists()
    with pytest.raises(jsonschema.ValidationError):
        validate(a_spec(oos_max_drawdown=0.1))


def test_write_golden_writes_the_fixture_set(tmp_path: Path) -> None:
    syn = cointegrated_pair(n=120, seed=3)
    bars = {"GLD": syn["A"], "GDX": syn["B"]}
    params = pairs.Params(1.6, 20, 2.0, 0.5, 30)
    tr = pairs.replay(bars, ["GLD", "GDX"], params, 0.9)
    al = pairs.align(bars, ["GLD", "GDX"])
    res = run_pairs(tr, al, ["GLD", "GDX"], CostModel(0, 5))
    files = write_golden(
        tmp_path / "g", bars=bars, spec=a_spec(),
        signals=pairs.signals(bars, ["GLD", "GDX"], params, 0.9),
        equity=res.equity, metrics=metrics.summary(res.equity["equity"].to_numpy()), note="x",
    )
    names = sorted(f.name for f in files)
    assert names == ["README.md", "bars_GDX.parquet", "bars_GLD.parquet", "equity_curve.csv",
                     "expected_signals.csv", "metrics.json", "spec.yaml"]
    head = (tmp_path / "g" / "expected_signals.csv").read_text().splitlines()[:3]
    assert head[0] == "date,symbol,target_weight"
    assert head[1].startswith("2015-01-02,GDX,")
