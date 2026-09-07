import datetime as dt
import math

import numpy as np
import pandas as pd
import pytest

from tt.backtest import metrics
from tt.backtest.engine import CostModel, run


def test_daily_returns() -> None:
    assert metrics.daily_returns(np.array([100, 110, 99])).tolist() == pytest.approx([0.1, -0.1])
    assert metrics.daily_returns(np.array([100])).size == 0


def test_sharpe_by_hand() -> None:
    # Same arithmetic as the Go test: mean 0.005, sample sd 0.0129099 → 6.14817.
    assert metrics.sharpe(np.array([0.01, -0.01, 0.02, 0.0])) == pytest.approx(6.148170, abs=1e-5)
    assert math.isnan(metrics.sharpe(np.array([0.1])))
    assert math.isnan(metrics.sharpe(np.array([0.01, 0.01, 0.01])))


@pytest.mark.parametrize(
    ("equity", "dd", "dur"),
    [
        ([], 0.0, 0),
        ([1, 1, 1], 0.0, 0),
        ([1, 2, 3], 0.0, 0),
        ([100, 90, 80, 95, 101], -0.2, 3),
        ([100, 95, 90, 110, 100, 95, 93.5], -0.15, 3),
        ([100, 90, 100, 90], -0.1, 1),
    ],
)
def test_drawdown(equity, dd, dur) -> None:
    e = np.array(equity, dtype=float)
    assert metrics.max_drawdown(e) == pytest.approx(dd)
    assert metrics.max_drawdown_duration(e) == dur


def test_engine_rebalances_at_close_with_costs() -> None:
    dates = pd.bdate_range(dt.date(2024, 1, 1), periods=3).date
    prices = pd.DataFrame({"date": dates, "A": [100.0, 110.0, 110.0], "B": [50.0, 50.0, 40.0]})
    weights = pd.DataFrame({"A": [0.5, 0.5, 0.0], "B": [-0.5, -0.5, 0.0]})
    res = run(prices, weights, CostModel(commission_usd=1.0, slippage_bps=10.0))
    eq = res.equity["equity"].tolist()
    # Bar 0: equity 10,000; buy 50 A (5,000), short 100 B (5,000): traded 10,000
    # → slippage 10, commission 2 → 9,988.
    assert eq[0] == pytest.approx(9988.0)
    # Bar 1: A up 10% (+500), B flat: marked 10,488; rebalance to 50% each:
    # target A = 47.6727 sh, B = −104.88 sh; traded = 2.327×110 + 4.88×50 = 256+244 = 500.
    # cost = 0.5 + 2 → 10,485.5
    assert eq[1] == pytest.approx(10485.5, abs=0.01)
    # Bar 2: B falls to 40: short gains 104.88×10 = 1,048.8 → 11,534.3; go flat:
    # traded 47.67×110 + 104.88×40 = 5,244+4,195 = 9,439 → 9.44 + 2 → 11,522.86
    assert eq[2] == pytest.approx(11522.86, abs=0.05)
    assert len(res.trades) == 6
