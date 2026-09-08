import datetime as dt

import numpy as np
import pandas as pd
import pytest

from tt.strategies import donchian


def ohlc(close: list[float], high: list[float] | None = None, low: list[float] | None = None,
         factor: float = 1.0, start: dt.date = dt.date(2024, 1, 1)) -> pd.DataFrame:
    """Bars whose adjusted prices are the arguments; ``factor`` is close/adjclose."""
    n = len(close)
    c = np.array(close, dtype=float)
    h = np.array(high if high is not None else close, dtype=float)
    lo = np.array(low if low is not None else close, dtype=float)
    return pd.DataFrame({
        "date": pd.bdate_range(start, periods=n).date,
        "open": c * factor, "high": h * factor, "low": lo * factor, "close": c * factor, "adjclose": c,
    })


def test_params_validation() -> None:
    good = {"entry_lookback": 55, "exit_lookback": 20}
    assert donchian.Params.from_dict(good) == donchian.Params(55, 20)
    with pytest.raises(ValueError, match="unknown"):
        donchian.Params.from_dict({**good, "band": 0})
    with pytest.raises(ValueError, match="missing"):
        donchian.Params.from_dict({"entry_lookback": 55})
    with pytest.raises(ValueError, match="entry_lookback"):
        donchian.Params.from_dict({**good, "entry_lookback": 1})
    with pytest.raises(ValueError, match="exit_lookback"):
        donchian.Params.from_dict({**good, "exit_lookback": 2.5})
    with pytest.raises(ValueError, match="exit_lookback < entry"):
        donchian.Params.from_dict({"entry_lookback": 20, "exit_lookback": 20})


def test_breakout_enters_above_prior_highs_and_exits_below_prior_lows() -> None:
    p = donchian.Params(entry_lookback=3, exit_lookback=2)
    #            t: 0   1   2   3    4    5   6   7
    close = [10, 11, 10, 12, 11.5, 11, 9, 9.5]
    high = [10.5, 11.5, 10.5, 12.5, 12, 11.5, 9.5, 10]
    low = [9.5, 10.5, 9.5, 11.5, 11, 10.5, 8.5, 9]
    b = {"A": ohlc(close, high, low), "H": ohlc([1.0] * 8)}
    tr = donchian.replay(b, ["A", "H"], p, 1.0)
    # upper undefined for t < 3; t=3: max(high 0..2) = 11.5, close 12 > 11.5 → enter.
    assert np.isnan(tr["upper_A"].iloc[2]) and tr["upper_A"].iloc[3] == 11.5
    # t=4: lower = min(low 2..3) = 9.5, close 11.5 stays; t=5: lower = min(11.5, 11) = 11,
    # close 11 is exactly on it → stays; t=6: lower = min(11, 10.5) = 10.5, close 9 → exit.
    assert tr["lower_A"].iloc[5] == 11 and tr["s_A"].iloc[5] == 1
    assert tr["s_A"].tolist() == [0, 0, 0, 1, 1, 1, 0, 0]
    # t=7: upper = max(high 4..6) = 12, close 9.5 → stays out.
    assert donchian.switches(tr, ["A", "H"]) == 2


def test_the_current_bar_is_not_in_its_own_channel() -> None:
    p = donchian.Params(entry_lookback=3, exit_lookback=2)
    # A new high on the bar itself: the channel is the prior three highs (10.5),
    # and 11 closes above it, so it enters. Were the bar included, 11 < 11.5.
    close = [10, 10, 10, 11]
    high = [10.5, 10.5, 10.5, 11.5]
    b = {"A": ohlc(close, high, [9.5, 9.5, 9.5, 10.5]), "H": ohlc([1.0] * 4)}
    tr = donchian.replay(b, ["A", "H"], p, 1.0)
    assert tr["s_A"].tolist() == [0, 0, 0, 1]


def test_a_split_cannot_fake_a_breakout_or_an_exit() -> None:
    p = donchian.Params(entry_lookback=3, exit_lookback=2)
    close = [10, 10, 10, 10, 10, 10]
    high = [10.2] * 6
    low = [9.8] * 6
    a = ohlc(close, high, low)
    # Raw prices double for the last three bars (a reverse split); adjusted ones do not.
    a.loc[3:, ["open", "high", "low", "close"]] *= 2
    b = {"A": a, "H": ohlc([1.0] * 6)}
    tr = donchian.replay(b, ["A", "H"], p, 1.0)
    assert (tr["s_A"] == 0).all() and tr["upper_A"].iloc[4] == pytest.approx(10.2)


def test_equality_never_transitions_and_weights_sum_to_gross() -> None:
    p = donchian.Params(entry_lookback=3, exit_lookback=2)
    close = [10, 10, 10, 10.5, 10.5]
    high = [10.5, 10.5, 10.5, 10.5, 10.5]
    b = {"A": ohlc(close, high, [9.5] * 5),
         "B": ohlc([5, 5, 5, 6, 6], [5.5, 5.5, 5.5, 6.5, 6.5], [4.5] * 5),
         "H": ohlc([1.0] * 5)}
    tr = donchian.replay(b, ["A", "B", "H"], p, 0.8)
    assert (tr["s_A"] == 0).all()  # 10.5 is on the channel, never above it
    assert tr["s_B"].tolist() == [0, 0, 0, 1, 1]
    for _, row in tr.iterrows():
        assert row["w_A"] + row["w_B"] + row["w_H"] == pytest.approx(0.8)
    assert tr["w_B"].iloc[-1] == pytest.approx(0.4) and tr["w_H"].iloc[-1] == pytest.approx(0.4)
    sig = donchian.signals(b, ["A", "B", "H"], p, 0.8)
    assert list(sig.columns) == ["date", "symbol", "target_weight"] and len(sig) == 15
