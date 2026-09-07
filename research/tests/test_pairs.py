import datetime as dt

import numpy as np
import pandas as pd
import pytest

from tt.strategies import pairs


def bars_from(a: list[float], b: list[float], start: dt.date = dt.date(2024, 1, 1)):
    dates = pd.bdate_range(start, periods=len(a)).date
    def frame(prices):
        return pd.DataFrame({"date": dates, "adjclose": prices})
    return {"A": frame(a), "B": frame(b)}


P = pairs.Params(hedge_ratio=1.0, lookback=3, entry_z=1.0, exit_z=0.5, max_hold_days=2)


def test_params_validation() -> None:
    with pytest.raises(ValueError, match="unknown"):
        pairs.Params.from_dict({"hedge_ratio": 1, "lookback": 3, "entry_z": 1, "exit_z": 0.5,
                                "max_hold_days": 2, "typo": 1})
    with pytest.raises(ValueError, match="lookback"):
        pairs.Params.from_dict({"hedge_ratio": 1, "lookback": 1, "entry_z": 1, "exit_z": 0.5,
                                "max_hold_days": 2})
    with pytest.raises(ValueError, match="exit_z"):
        pairs.Params.from_dict({"hedge_ratio": 1, "lookback": 3, "entry_z": 1, "exit_z": 1.5,
                                "max_hold_days": 2})


def test_z_is_undefined_until_the_window_fills() -> None:
    tr = pairs.replay(bars_from([1, 2, 3, 4], [0, 0, 0, 0]), ["A", "B"], P, 0.9)
    assert np.isnan(tr["z"].iloc[0]) and np.isnan(tr["z"].iloc[1])
    # spread 1,2,3 over lookback 3: mean 2, sample sd 1 → z = 1.
    assert tr["z"].iloc[2] == pytest.approx(1.0)
    assert (tr["state"].iloc[:2] == 0).all()


def test_flat_spread_has_undefined_z_and_flat_state() -> None:
    tr = pairs.replay(bars_from([5, 5, 5, 5], [1, 1, 1, 1]), ["A", "B"], P, 0.9)
    assert tr["z"].isna().all()
    assert (tr["state"] == 0).all()


def test_entry_exit_time_stop_and_no_same_bar_reentry() -> None:
    # spread: 0,0,0 (undefined sd → flat), then a drop to −9 (z << −1: enter long),
    # then held, held (time stop after max_hold 2), then a spike.
    a = [10, 10, 10, 1, 1, 1, 1, 30, 30, 30]
    b = [10] * 10
    tr = pairs.replay(bars_from(a, b), ["A", "B"], P, 0.9)
    states = tr["state"].tolist()
    # bar 3: z = (−9 − mean(0,0,−9)) / sd → −1.1547 ≤ −1 → long
    assert states[3] == 1
    # bar 4: z = (−9 − (−6)) / 5.196 = −0.577 < −0.5 → stays, held 1
    assert states[4] == 1
    # bar 5: z of (−9,−9,−9) undefined (sd 0) → forced flat
    assert states[5] == 0
    # bar 7: window (−9,−9,20): z = +1.1547 → short
    assert states[7] == -1


def test_weights_sum_to_gross_when_in_a_position() -> None:
    a = [10, 10, 10, 1, 1, 1, 1, 30, 30, 30]
    b = [10] * 10
    tr = pairs.replay(bars_from(a, b), ["A", "B"], P, 0.9)
    for _, row in tr.iterrows():
        if row["state"] != 0:
            assert abs(row["w_a"]) + abs(row["w_b"]) == pytest.approx(0.9)
            assert np.sign(row["w_a"]) == row["state"]
            assert np.sign(row["w_b"]) == -row["state"]
        else:
            assert row["w_a"] == 0 and row["w_b"] == 0


def test_time_stop() -> None:
    # A spread that stays far below its mean: enter, then time stop after 2 held bars.
    a = [10, 10, 10, 1, 0.9, 0.8, 0.7, 0.6]
    b = [10] * 8
    p = pairs.Params(1.0, 3, 1.0, 0.5, 2)
    tr = pairs.replay(bars_from(a, b), ["A", "B"], p, 1.0)
    st = tr["state"].tolist()
    assert st[3] == 1
    assert st[4] == 1 and st[5] == 1  # held 1, held 2
    assert st[6] == 0  # held ≥ max_hold: exit, no re-entry this bar


def test_alignment_inner_joins_on_date() -> None:
    d = bars_from([1, 2, 3], [1, 1, 1])
    d["B"] = d["B"].iloc[1:]  # B lacks the first date
    al = pairs.align(d, ["A", "B"])
    assert len(al) == 2


def test_signals_long_form_covers_every_date_and_symbol() -> None:
    sig = pairs.signals(bars_from([1, 2, 3, 4], [1, 1, 1, 1]), ["A", "B"], P, 0.9)
    assert list(sig.columns) == ["date", "symbol", "target_weight"]
    assert len(sig) == 8
