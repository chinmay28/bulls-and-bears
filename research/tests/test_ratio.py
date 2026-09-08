import datetime as dt
import math

import numpy as np
import pandas as pd
import pytest

from tt.strategies import ratio

U = ["A", "B", "H"]  # two risk assets and the haven, last


def bars_from(start: dt.date = dt.date(2024, 1, 1), **series: list[float]):
    n = len(next(iter(series.values())))
    dates = pd.bdate_range(start, periods=n).date
    return {s: pd.DataFrame({"date": dates, "adjclose": v}) for s, v in series.items()}


P = ratio.Params(lookback=3, entry_z=1.0, exit_z=0.0, max_hold_days=2)


def test_params_validation() -> None:
    good = {"lookback": 3, "entry_z": 1.0, "exit_z": 0.0, "max_hold_days": 2}
    assert ratio.Params.from_dict(good) == P
    with pytest.raises(ValueError, match="unknown"):
        ratio.Params.from_dict({**good, "hedge_ratio": 1})
    with pytest.raises(ValueError, match="missing"):
        ratio.Params.from_dict({k: v for k, v in good.items() if k != "exit_z"})
    with pytest.raises(ValueError, match="lookback"):
        ratio.Params.from_dict({**good, "lookback": 1})
    with pytest.raises(ValueError, match="lookback"):
        ratio.Params.from_dict({**good, "lookback": 2.5})
    with pytest.raises(ValueError, match="entry_z"):
        ratio.Params.from_dict({**good, "entry_z": 0})
    with pytest.raises(ValueError, match="exit_z"):
        ratio.Params.from_dict({**good, "exit_z": -1.0})
    with pytest.raises(ValueError, match="max_hold_days"):
        ratio.Params.from_dict({**good, "max_hold_days": 0})


def test_universe_split() -> None:
    assert ratio.split(["SPY", "QQQ", "GLD"]) == (["SPY", "QQQ"], "GLD")
    with pytest.raises(ValueError, match="at least"):
        ratio.split(["GLD"])
    with pytest.raises(ValueError, match="repeats"):
        ratio.split(["SPY", "SPY", "GLD"])


def test_z_is_undefined_until_the_window_fills_and_when_flat() -> None:
    # A/H ratio: e^0, e^1, e^2, e^3 → log ratio 0,1,2,3. B/H flat.
    b = bars_from(A=[1, math.e, math.e**2, math.e**3], B=[2, 2, 2, 2], H=[1, 1, 1, 1])
    z = ratio.zscores(ratio.align(b, U), U, 3)
    assert np.isnan(z["A"].iloc[0]) and np.isnan(z["A"].iloc[1])
    # window (0,1,2): mean 1, sample sd 1 → z = 1
    assert z["A"].iloc[2] == pytest.approx(1.0)
    assert z["A"].iloc[3] == pytest.approx(1.0)
    assert z["B"].isna().all()  # sd 0 → undefined


def test_sleeve_enters_when_cheap_exits_at_the_mean_and_time_stops() -> None:
    # Log ratio A/H: 0,0,0 (flat: undefined), then a drop to -9 (z = -1.1547 ≤ -1: enter),
    # then held while z stays below 0, time stop after max_hold 2 held bars.
    a = [1, 1, 1, math.exp(-9), math.exp(-9.1), math.exp(-9.2), math.exp(-9.3), math.exp(-9.3)]
    b = bars_from(A=a, B=[5] * 8, H=[1] * 8)
    tr = ratio.replay(b, U, P, 1.0)
    st = tr["s_A"].tolist()
    assert st[:3] == [0, 0, 0]
    assert st[3] == 1  # entered
    assert st[4] == 1 and st[5] == 1  # held 1, held 2 (z still below the mean)
    assert st[6] == 0  # held >= max_hold: exit, and no re-entry on the same bar
    # B's ratio never moves: its sleeve stays in the haven throughout.
    assert (tr["s_B"] == 0).all()


def test_exit_when_ratio_reverts_above_exit_z() -> None:
    p = ratio.Params(lookback=3, entry_z=1.0, exit_z=0.5, max_hold_days=99)
    # ratio drops (enter), then spikes back well above its mean (exit).
    a = [1, 1, 1, math.exp(-9), math.exp(-9), math.exp(20), math.exp(20)]
    b = bars_from(A=a, B=[5] * 7, H=[1] * 7)
    tr = ratio.replay(b, U, p, 1.0)
    st = tr["s_A"].tolist()
    assert st[3] == 1
    assert st[4] == 1  # z of (0,-9,-9) window = -0.577: above -entry but below exit → hold
    assert st[5] == 0  # z >> exit_z → exit


def test_weights_split_gross_across_sleeves_and_haven_takes_the_rest() -> None:
    a = [1, 1, 1, math.exp(-9), math.exp(-9.1), math.exp(-9.2), math.exp(-9.3), math.exp(-9.3)]
    b = bars_from(A=a, B=[5] * 8, H=[1] * 8)
    tr = ratio.replay(b, U, P, 0.9)
    for _, row in tr.iterrows():
        total = row["w_A"] + row["w_B"] + row["w_H"]
        assert total == pytest.approx(0.9)
        assert row["w_A"] >= 0 and row["w_B"] >= 0 and row["w_H"] >= 0
        if row["s_A"] == 1:
            assert row["w_A"] == pytest.approx(0.45)
            assert row["w_H"] == pytest.approx(0.45)
        else:
            assert row["w_A"] == 0 and row["w_H"] == pytest.approx(0.9)


def test_alignment_inner_joins_on_date() -> None:
    b = bars_from(A=[1, 2, 3], B=[1, 1, 1], H=[1, 1, 1])
    b["H"] = b["H"].iloc[1:]
    assert len(ratio.align(b, U)) == 2
    assert list(ratio.align(b, U).columns) == ["date", "A", "B", "H"]


def test_signals_long_form_and_switch_count() -> None:
    a = [1, 1, 1, math.exp(-9), math.exp(-9.1), math.exp(-9.2), math.exp(-9.3), math.exp(-9.3)]
    b = bars_from(A=a, B=[5] * 8, H=[1] * 8)
    sig = ratio.signals(b, U, P, 1.0)
    assert list(sig.columns) == ["date", "symbol", "target_weight"]
    assert len(sig) == 8 * 3
    tr = ratio.replay(b, U, P, 1.0)
    assert ratio.switches(tr, U) == 2  # one entry, one exit, on A only
    w = ratio.weights_frame(tr, U)
    assert list(w.columns) == U
