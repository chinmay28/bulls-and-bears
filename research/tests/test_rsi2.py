import datetime as dt

import numpy as np
import pandas as pd
import pytest

from tt.strategies import rsi2


def bars_from(start: dt.date = dt.date(2024, 1, 1), **series: list[float]):
    n = len(next(iter(series.values())))
    dates = pd.bdate_range(start, periods=n).date
    return {s: pd.DataFrame({"date": dates, "adjclose": v}) for s, v in series.items()}


def test_params_validation() -> None:
    good = {"trend_lookback": 200, "rsi_entry": 5, "rsi_exit": 70, "max_hold_days": 5}
    assert rsi2.Params.from_dict(good) == rsi2.Params(200, 5.0, 70.0, 5)
    with pytest.raises(ValueError, match="unknown"):
        rsi2.Params.from_dict({**good, "rsi_period": 2})
    with pytest.raises(ValueError, match="missing"):
        rsi2.Params.from_dict({k: v for k, v in good.items() if k != "rsi_exit"})
    with pytest.raises(ValueError, match="trend_lookback"):
        rsi2.Params.from_dict({**good, "trend_lookback": 1})
    with pytest.raises(ValueError, match="rsi_entry"):
        rsi2.Params.from_dict({**good, "rsi_entry": 70})
    with pytest.raises(ValueError, match="rsi_entry"):
        rsi2.Params.from_dict({**good, "rsi_exit": 101})
    with pytest.raises(ValueError, match="rsi_entry"):
        rsi2.Params.from_dict({**good, "rsi_entry": -1})
    with pytest.raises(ValueError, match="max_hold_days"):
        rsi2.Params.from_dict({**good, "max_hold_days": 0})


def test_buys_a_pullback_in_an_uptrend_and_sells_the_bounce() -> None:
    p = rsi2.Params(trend_lookback=6, rsi_entry=30, rsi_exit=60, max_hold_days=10)
    # Up, then two down days that keep the close above the 6-bar average, then a bounce.
    a = [10, 11, 12, 13, 14, 15, 14.2, 13.9, 14.8, 15.5]
    b = bars_from(A=a, H=[1] * 10)
    tr = rsi2.replay(b, ["A", "H"], p, 1.0)
    r = tr["rsi_A"].to_numpy()
    # t=6: -0.8 after +1s → G 0.5, L 0.4 → 55.6; t=7: -0.3 → G 0.25, L 0.35 → 41.7.
    # Not under 30: no entry.
    assert (tr["s_A"] == 0).all() and r[7] == pytest.approx(100 - 100 / (1 + 0.25 / 0.35))
    p = rsi2.Params(trend_lookback=6, rsi_entry=45, rsi_exit=60, max_hold_days=10)
    tr = rsi2.replay(b, ["A", "H"], p, 1.0)
    assert a[7] > tr["sma_A"].iloc[7] and tr["s_A"].iloc[7] == 1
    # t=8: +0.9 → RSI 76.7, over 60 → sold at that close.
    assert r[8] > 60 and tr["s_A"].iloc[8] == 0
    assert tr["s_A"].tolist() == [0, 0, 0, 0, 0, 0, 0, 1, 0, 0]
    assert rsi2.switches(tr, ["A", "H"]) == 2 and rsi2.holding_periods(tr, ["A", "H"]) == [1]


def test_time_stop_and_trend_break_exit_and_no_entry_below_trend() -> None:
    p = rsi2.Params(trend_lookback=6, rsi_entry=45, rsi_exit=99, max_hold_days=2)
    # A slow drift up after the entry keeps the close above the average and
    # the RSI under 99: only the time stop can end the holding.
    a = [10, 11, 12, 13, 14, 15, 14.2, 13.9, 14.3, 14.6, 14.9, 15.2]
    b = bars_from(A=a, H=[1] * 12)
    tr = rsi2.replay(b, ["A", "H"], p, 1.0)
    # Entered at t=7 (held 0); held through t=8 (1) and t=9 (2); out at t=10
    # when held reaches the stop. Then the RSI is high: no re-entry.
    assert tr["s_A"].tolist() == [0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 0, 0]
    assert rsi2.holding_periods(tr, ["A", "H"]) == [3]
    # A trend break exits before the time stop: the close drops under the average.
    p = rsi2.Params(trend_lookback=6, rsi_entry=45, rsi_exit=99, max_hold_days=10)
    a = [10, 11, 12, 13, 14, 15, 14.2, 13.9, 12.0, 12.5]
    b = bars_from(A=a, H=[1] * 10)
    tr = rsi2.replay(b, ["A", "H"], p, 1.0)
    assert tr["s_A"].iloc[7] == 1 and a[8] < tr["sma_A"].iloc[8] and tr["s_A"].iloc[8] == 0
    # Below the average with a low RSI: still no entry.
    assert tr["rsi_A"].iloc[8] == pytest.approx(10.0) and tr["s_A"].iloc[9] == 0


def test_thresholds_are_strict_and_undefined_inputs_rest() -> None:
    p = rsi2.Params(trend_lookback=2, rsi_entry=50, rsi_exit=50, max_hold_days=5)
    # +1 then -1 gives an RSI of exactly 50: not under 50, so no entry.
    with pytest.raises(ValueError):
        rsi2.Params.from_dict({"trend_lookback": 2, "rsi_entry": 50, "rsi_exit": 50, "max_hold_days": 5})
    p = rsi2.Params(trend_lookback=2, rsi_entry=50, rsi_exit=60, max_hold_days=5)
    b = bars_from(A=[10, 11, 10, 11.5], H=[1] * 4)
    tr = rsi2.replay(b, ["A", "H"], p, 1.0)
    assert tr["rsi_A"].iloc[2] == 50.0 and tr["s_A"].iloc[2] == 0
    assert np.isnan(tr["rsi_A"].iloc[1]) and tr["s_A"].iloc[1] == 0


def test_weights_and_signals_shape() -> None:
    p = rsi2.Params(trend_lookback=6, rsi_entry=45, rsi_exit=60, max_hold_days=5)
    a = [10, 11, 12, 13, 14, 15, 14.2, 13.9]
    b = bars_from(A=a, B=[5] * 8, H=[1] * 8)
    tr = rsi2.replay(b, ["A", "B", "H"], p, 0.8)
    for _, row in tr.iterrows():
        assert row["w_A"] + row["w_B"] + row["w_H"] == pytest.approx(0.8)
    assert tr["w_A"].iloc[-1] == pytest.approx(0.4) and tr["w_B"].iloc[-1] == 0
    assert tr["w_H"].iloc[-1] == pytest.approx(0.4)
    sig = rsi2.signals(b, ["A", "B", "H"], p, 0.8)
    assert list(sig.columns) == ["date", "symbol", "target_weight"] and len(sig) == 24
