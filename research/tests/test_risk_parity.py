import datetime as dt

import numpy as np
import pandas as pd
import pytest

from tt.strategies import risk_parity as rp

U = ["A", "B", "H"]


def bars_from(start: dt.date = dt.date(2024, 1, 1), **series: list[float]):
    n = len(next(iter(series.values())))
    dates = pd.bdate_range(start, periods=n).date
    return {s: pd.DataFrame({"date": dates, "adjclose": v}) for s, v in series.items()}


def test_params_validation() -> None:
    good = {"trend_lookback": 200, "vol_lookback": 63, "rebalance_days": 21}
    assert rp.Params.from_dict(good) == rp.Params(200, 63, 21)
    with pytest.raises(ValueError, match="unknown"):
        rp.Params.from_dict({**good, "band": 0})
    with pytest.raises(ValueError, match="missing"):
        rp.Params.from_dict({"trend_lookback": 200})
    with pytest.raises(ValueError, match="trend_lookback"):
        rp.Params.from_dict({**good, "trend_lookback": 1})
    with pytest.raises(ValueError, match="vol_lookback"):
        rp.Params.from_dict({**good, "vol_lookback": 2.5})
    with pytest.raises(ValueError, match="rebalance_days"):
        rp.Params.from_dict({**good, "rebalance_days": 0})


def test_weights_at_pools_active_sleeves_by_inverse_vol() -> None:
    w = rp.weights_at(["A", "B", "C"], "H", ["A", "B"], {"A": 0.01, "B": 0.03}, 3, 0.9)
    # active budget 0.6; raw 100 and 33.3; A gets 3/4 of it, B 1/4; C's sleeve rests.
    assert w["A"] == pytest.approx(0.45) and w["B"] == pytest.approx(0.15) and w["C"] == 0
    assert w["H"] == pytest.approx(0.3)
    assert rp.weights_at(["A"], "H", [], {}, 1, 1.0) == {"A": 0.0, "H": 1.0}


def test_eligibility_needs_trend_and_positive_vol() -> None:
    p = rp.Params(trend_lookback=2, vol_lookback=2, rebalance_days=1)
    # A trends up with vol; B is flat (zero vol, on its average); C trends down.
    a = [10, 11, 12, 13, 14]
    b = [5, 5, 5, 5, 5]
    c = [14, 13, 12, 11, 10]
    bars = bars_from(A=a, B=b, C=c, H=[1] * 5)
    tr = rp.replay(bars, ["A", "B", "C", "H"], p, 1.0)
    assert tr["rebalance"].tolist() == [0, 0, 1, 1, 1]
    assert tr["e_A"].tolist() == [0, 0, 1, 1, 1]
    assert (tr["e_B"] == 0).all() and (tr["e_C"] == 0).all()
    # Only A is active: it gets its own third; two thirds rest in the haven.
    assert tr["w_A"].tolist()[2:] == pytest.approx([1 / 3] * 3)
    assert tr["w_H"].tolist() == pytest.approx([1.0, 1.0, 2 / 3, 2 / 3, 2 / 3])
    assert (tr["w_B"] == 0).all() and (tr["w_C"] == 0).all()


def test_weights_change_continuously_and_hold_between_rebalances() -> None:
    p = rp.Params(trend_lookback=2, vol_lookback=2, rebalance_days=2)
    a = [10, 11, 12.5, 13, 14.5, 15, 16.5]
    b = [10, 10.5, 11, 11.2, 11.5, 11.7, 12]
    bars = bars_from(A=a, B=b, H=[1] * 7)
    tr = rp.replay(bars, U, p, 1.0)
    assert tr["rebalance"].tolist() == [0, 0, 1, 0, 1, 0, 1]
    w = tr[["w_A", "w_B", "w_H"]].to_numpy()
    # Both eligible from t=2; the quieter B carries more of the book.
    assert w[2, 1] > w[2, 0] > 0 and w[2, 2] == 0
    # Off-schedule bars repeat the previous weights exactly.
    assert (w[3] == w[2]).all() and (w[5] == w[4]).all()
    # On-schedule bars re-weight to the new volatilities.
    assert not (w[4] == w[2]).all()
    for row in w:
        assert row.sum() == pytest.approx(1.0) and (row >= 0).all()


def test_all_ineligible_rests_in_the_haven_and_gross_scales() -> None:
    p = rp.Params(trend_lookback=3, vol_lookback=2, rebalance_days=1)
    bars = bars_from(A=[14, 13, 12, 11, 10], B=[9, 8, 7, 6, 5], H=[1] * 5)
    tr = rp.replay(bars, U, p, 0.8)
    assert (tr["w_A"] == 0).all() and (tr["w_B"] == 0).all()
    assert tr["w_H"].tolist() == pytest.approx([0.8] * 5)
    assert np.isnan(tr["sma_A"].iloc[1]) and not np.isnan(tr["sma_A"].iloc[2])
    sig = rp.signals(bars, U, p, 0.8)
    assert list(sig.columns) == ["date", "symbol", "target_weight"] and len(sig) == 15
    assert rp.switches(tr, U) == 0
