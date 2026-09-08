import datetime as dt

import numpy as np
import pandas as pd
import pytest

from tt.strategies import time_series_momentum as tsm

U = ["A", "B", "H"]


def bars_from(start: dt.date = dt.date(2024, 1, 1), **series: list[float]):
    n = len(next(iter(series.values())))
    dates = pd.bdate_range(start, periods=n).date
    return {s: pd.DataFrame({"date": dates, "adjclose": v}) for s, v in series.items()}


def test_params_validation() -> None:
    good = {"lookback": 252, "rebalance_days": 21}
    assert tsm.Params.from_dict(good) == tsm.Params(252, 21)
    with pytest.raises(ValueError, match="unknown"):
        tsm.Params.from_dict({**good, "top_k": 1})
    with pytest.raises(ValueError, match="missing"):
        tsm.Params.from_dict({"lookback": 252})
    with pytest.raises(ValueError, match="lookback"):
        tsm.Params.from_dict({**good, "lookback": 0})
    with pytest.raises(ValueError, match="rebalance_days"):
        tsm.Params.from_dict({**good, "rebalance_days": 2.5})


def test_all_rising_all_falling_and_mixed() -> None:
    p = tsm.Params(lookback=2, rebalance_days=1)
    up = [10, 11, 12, 13, 14]
    down = [14, 13, 12, 11, 10]
    b = bars_from(A=up, B=down, H=[1] * 5)
    tr = tsm.replay(b, U, p, 1.0)
    assert tr["s_A"].tolist() == [0, 0, 1, 1, 1]
    assert (tr["s_B"] == 0).all()
    assert tr["w_H"].tolist() == [1.0, 1.0, 0.5, 0.5, 0.5]
    b = bars_from(A=up, B=up, H=[1] * 5)
    tr = tsm.replay(b, U, p, 1.0)
    assert tr["w_H"].tolist()[2:] == [0.0, 0.0, 0.0] and tr["w_A"].tolist()[2:] == [0.5] * 3


def test_exactly_zero_return_rests_in_the_haven() -> None:
    p = tsm.Params(lookback=1, rebalance_days=1)
    b = bars_from(A=[10, 10, 11, 11], H=[1] * 4)
    tr = tsm.replay(b, ["A", "H"], p, 1.0)
    assert tr["s_A"].tolist() == [0, 0, 1, 0]


def test_signal_changes_wait_for_a_rebalance_bar() -> None:
    p = tsm.Params(lookback=2, rebalance_days=3)
    # returns: t=2 +, t=3 -, t=4 -, t=5 +, t=6 +, t=7 -
    a = [10, 11, 12, 9, 8, 10, 11, 9]
    b = bars_from(A=a, H=[1] * 8)
    tr = tsm.replay(b, ["A", "H"], p, 1.0)
    assert tr["rebalance"].tolist() == [0, 0, 1, 0, 0, 1, 0, 0]
    # Enter at t=2; the negative returns at t=3,4 are off-schedule and stand;
    # t=5 is positive and keeps it; t=7's negative is off-schedule too.
    assert tr["s_A"].tolist() == [0, 0, 1, 1, 1, 1, 1, 1]
    p = tsm.Params(lookback=2, rebalance_days=1)
    tr = tsm.replay(b, ["A", "H"], p, 1.0)
    assert tr["s_A"].tolist() == [0, 0, 1, 0, 0, 1, 1, 0]


def test_insufficient_lookback_and_independent_haven() -> None:
    p = tsm.Params(lookback=10, rebalance_days=1)
    b = bars_from(A=[10, 11, 12], H=[5, 1, 9])
    tr = tsm.replay(b, ["A", "H"], p, 1.0)
    assert (tr["s_A"] == 0).all() and np.isnan(tr["r_A"]).all() and tr["w_H"].tolist() == [1.0] * 3
    # The haven's own moves never enter the decision.
    p = tsm.Params(lookback=1, rebalance_days=1)
    b = bars_from(A=[10, 11, 12], H=[5, 100, 1])
    tr = tsm.replay(b, ["A", "H"], p, 1.0)
    assert tr["s_A"].tolist() == [0, 1, 1]


def test_gross_leverage_and_signals_shape() -> None:
    p = tsm.Params(lookback=1, rebalance_days=1)
    b = bars_from(A=[10, 11], B=[5, 4], H=[1, 1])
    tr = tsm.replay(b, U, p, 0.8)
    assert tr["w_A"].iloc[1] == pytest.approx(0.4) and tr["w_B"].iloc[1] == 0 and tr["w_H"].iloc[1] == pytest.approx(0.4)
    for _, row in tr.iterrows():
        assert row["w_A"] + row["w_B"] + row["w_H"] == pytest.approx(0.8)
    sig = tsm.signals(b, U, p, 0.8)
    assert list(sig.columns) == ["date", "symbol", "target_weight"] and len(sig) == 6
    assert tsm.switches(tr, U) == 1
