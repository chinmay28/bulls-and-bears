import datetime as dt

import numpy as np
import pandas as pd
import pytest

from tt.strategies import momentum, trend

U = ["A", "B", "H"]


def bars_from(start: dt.date = dt.date(2024, 1, 1), **series: list[float]):
    n = len(next(iter(series.values())))
    dates = pd.bdate_range(start, periods=n).date
    return {s: pd.DataFrame({"date": dates, "adjclose": v}) for s, v in series.items()}


# --- sma_trend ---


def test_trend_params_validation() -> None:
    good = {"lookback": 3, "band": 0.0}
    assert trend.Params.from_dict(good) == trend.Params(3, 0.0)
    with pytest.raises(ValueError, match="unknown"):
        trend.Params.from_dict({**good, "entry_z": 1})
    with pytest.raises(ValueError, match="missing"):
        trend.Params.from_dict({"lookback": 3})
    with pytest.raises(ValueError, match="lookback"):
        trend.Params.from_dict({**good, "lookback": 1})
    with pytest.raises(ValueError, match="band"):
        trend.Params.from_dict({**good, "band": 1.0})
    with pytest.raises(ValueError, match="band"):
        trend.Params.from_dict({**good, "band": -0.1})


def test_trend_moving_average_is_exact_on_constant_prices() -> None:
    sma = trend.moving_average(np.array([10.0, 10, 10, 10]), 3)
    assert np.isnan(sma[0]) and np.isnan(sma[1]) and sma[2] == 10.0 and sma[3] == 10.0
    assert trend.moving_average(np.array([1.0, 2, 3, 4]), 2).tolist()[1:] == [1.5, 2.5, 3.5]
    assert np.isnan(trend.moving_average(np.array([1.0]), 2)).all()


def test_trend_enters_strictly_above_the_average_and_exits_strictly_below() -> None:
    p = trend.Params(lookback=3, band=0.0)
    # sma undefined for two bars; 10 on a 10 average stays out; 12 above 10.67
    # enters; 12 above 11.33 stays; 9 below 11 exits; 9 on a 9 average stays
    # out; 13 above 10.33 enters again.
    a = [10, 10, 10, 12, 12, 9, 9, 9, 13]
    b = bars_from(A=a, B=[5] * 9, H=[1] * 9)
    tr = trend.replay(b, U, p, 1.0)
    assert tr["s_A"].tolist() == [0, 0, 0, 1, 1, 0, 0, 0, 1]
    # B never moves: never strictly above its own average, never held.
    assert (tr["s_B"] == 0).all()


def test_trend_band_needs_a_clear_cross() -> None:
    p = trend.Params(lookback=2, band=0.10)
    # sma of (10,10) = 10: 10.5 is inside the band (needs > 11) → stays out;
    # 13 above 1.1 × 11.75 enters; 11 is inside the exit band (needs < 10.8)
    # → stays; 8 below 0.9 × 9.5 exits.
    a = [10, 10, 10.5, 13, 11, 8]
    b = bars_from(A=a, H=[1] * 6)
    tr = trend.replay(b, ["A", "H"], p, 1.0)
    assert tr["s_A"].tolist() == [0, 0, 0, 1, 1, 0]


def test_trend_weights_and_signals_shape() -> None:
    p = trend.Params(lookback=3, band=0.0)
    b = bars_from(A=[10, 10, 10, 12], B=[5, 5, 5, 6], H=[1, 1, 1, 1])
    tr = trend.replay(b, U, p, 0.8)
    for _, row in tr.iterrows():
        assert row["w_A"] + row["w_B"] + row["w_H"] == pytest.approx(0.8)
    assert tr["w_H"].tolist()[:3] == [0.8, 0.8, 0.8]
    assert tr["w_A"].iloc[-1] == pytest.approx(0.4) and tr["w_B"].iloc[-1] == pytest.approx(0.4)
    assert tr["w_H"].iloc[-1] == 0
    sig = trend.signals(b, U, p, 0.8)
    assert list(sig.columns) == ["date", "symbol", "target_weight"] and len(sig) == 12
    assert trend.switches(tr, U) == 2


# --- dual_momentum ---


def test_momentum_params_validation() -> None:
    good = {"lookback": 2, "top_k": 1, "rebalance_days": 1}
    assert momentum.Params.from_dict(good) == momentum.Params(2, 1, 1)
    with pytest.raises(ValueError, match="unknown"):
        momentum.Params.from_dict({**good, "band": 0})
    with pytest.raises(ValueError, match="top_k"):
        momentum.Params.from_dict({**good, "top_k": 0})
    with pytest.raises(ValueError, match="rebalance_days"):
        momentum.Params.from_dict({**good, "rebalance_days": 0.5})
    with pytest.raises(ValueError, match="lookback"):
        momentum.Params.from_dict({**good, "lookback": 0})


def test_momentum_pick_ranks_and_filters_against_the_haven() -> None:
    r = {"A": 0.10, "B": 0.05, "C": 0.20, "H": 0.06}
    assert momentum.pick(["A", "B", "C"], r, "H", 2) == ["C", "A"]
    assert momentum.pick(["A", "B", "C"], r, "H", 5) == ["C", "A"]  # B trails the haven
    r["H"] = 0.5
    assert momentum.pick(["A", "B", "C"], r, "H", 2) == []
    # Ties keep universe order.
    assert momentum.pick(["A", "B"], {"A": 0.1, "B": 0.1, "H": 0.0}, "H", 1) == ["A"]


def test_momentum_holds_between_rebalances() -> None:
    p = momentum.Params(lookback=1, top_k=1, rebalance_days=2)
    # Returns from bar 1 on. A wins at bar 1 (rebalance), B wins at bar 2 (no
    # rebalance: A still held), rebalance at bar 3: B wins.
    b = bars_from(A=[1, 2, 2, 2], B=[1, 1, 2, 3], H=[1, 1, 1, 1])
    tr = momentum.replay(b, U, p, 1.0)
    assert tr["h_A"].tolist() == [0, 1, 1, 0]
    assert tr["h_B"].tolist() == [0, 0, 0, 1]
    assert tr["w_H"].tolist() == [1.0, 0.0, 0.0, 0.0]
    assert momentum.switches(tr, U) == 3


def test_momentum_goes_to_the_haven_when_nothing_beats_it() -> None:
    p = momentum.Params(lookback=1, top_k=2, rebalance_days=1)
    b = bars_from(A=[1, 1, 1], B=[1, 0.9, 0.8], H=[1, 1.1, 1.2])
    tr = momentum.replay(b, U, p, 1.0)
    assert (tr["w_H"] == 1.0).all()
    assert np.isnan(tr["r_A"].iloc[0]) and tr["r_H"].iloc[1] == pytest.approx(0.1)


def test_momentum_top_k_splits_the_book() -> None:
    p = momentum.Params(lookback=1, top_k=2, rebalance_days=1)
    b = bars_from(A=[1, 2], B=[1, 1.5], H=[1, 1])
    tr = momentum.replay(b, U, p, 1.0)
    assert tr["w_A"].iloc[1] == 0.5 and tr["w_B"].iloc[1] == 0.5 and tr["w_H"].iloc[1] == 0
    with pytest.raises(ValueError, match="top_k"):
        momentum.replay(b, U, momentum.Params(1, 3, 1), 1.0)
