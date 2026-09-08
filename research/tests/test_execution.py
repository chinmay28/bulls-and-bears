"""The next-open fill and the adjusted OHLC helper (docs/CONTRACTS.md, Execution)."""

import datetime as dt

import numpy as np
import pandas as pd
import pytest

from tt.backtest.engine import CostModel, Fill, run
from tt.data import adjust


def frames(closes: dict[str, list[float]], opens: dict[str, list[float]], weights: dict[str, list[float]]):
    n = len(next(iter(closes.values())))
    dates = pd.bdate_range(dt.date(2024, 1, 1), periods=n).date
    return (
        pd.DataFrame({"date": dates, **closes}),
        pd.DataFrame({"date": dates, **opens}),
        pd.DataFrame(weights),
    )


def test_next_open_does_not_trade_same_bar_and_executes_previous_target() -> None:
    closes, opens, w = frames({"A": [100.0, 110.0, 120.0]}, {"A": [99.0, 105.0, 118.0]}, {"A": [1.0, 1.0, 1.0]})
    res = run(closes, w, CostModel(), fill=Fill.NEXT_OPEN, opens=opens)
    eq = res.equity["equity"].tolist()
    # Bar 0: nothing pending → all cash, marked 10,000 at the close.
    assert eq[0] == 10_000.0
    # Bar 1: fill bar 0's target at the open 105: 95.238 shares; marked at 110.
    assert eq[1] == pytest.approx(10_000 / 105 * 110)
    # Bar 2: already fully invested; the open fill is a no-op; marked at 120.
    assert eq[2] == pytest.approx(10_000 / 105 * 120)
    assert len(res.trades) == 1
    assert res.trades.iloc[0]["price"] == 105.0 and str(res.trades.iloc[0]["date"]) == "2024-01-02"


def test_first_bar_has_no_trade_and_final_signal_is_not_executed() -> None:
    closes, opens, w = frames({"A": [100.0, 100.0, 100.0]}, {"A": [100.0, 100.0, 100.0]}, {"A": [0.0, 0.0, 1.0]})
    res = run(closes, w, CostModel(), fill=Fill.NEXT_OPEN, opens=opens)
    assert res.trades.empty
    assert res.equity["equity"].tolist() == [10_000.0] * 3


def test_cost_is_charged_on_the_execution_bar_at_the_fill_price() -> None:
    closes, opens, w = frames({"A": [100.0, 100.0]}, {"A": [100.0, 200.0]}, {"A": [0.5, 0.5]})
    res = run(closes, w, CostModel(commission_usd=1.0, slippage_bps=10.0), fill=Fill.NEXT_OPEN, opens=opens)
    eq = res.equity["equity"].tolist()
    assert eq[0] == 10_000.0
    # Fill at the open of bar 1 (200): 25 shares = 5,000 traded; slippage 5 +
    # commission 1. Marked at the close of 100: 25 × 100 + 5,000 − 6 = 7,494.
    assert eq[1] == pytest.approx(7_494.0)


def test_target_shares_use_equity_at_the_open() -> None:
    closes, opens, w = frames({"A": [100.0, 100.0, 100.0]}, {"A": [100.0, 100.0, 80.0]}, {"A": [1.0, 0.5, 0.5]})
    res = run(closes, w, CostModel(), fill=Fill.NEXT_OPEN, opens=opens)
    # Bar 1 open: buy 100 shares at 100. Bar 2 open at 80: equity 8,000,
    # target 0.5 → 50 shares; sell 50 at 80 → cash 4,000; close 100 → 9,000.
    assert res.equity["equity"].tolist()[2] == pytest.approx(9_000.0)
    assert res.trades["qty"].tolist() == [100.0, -50.0]


def test_next_open_needs_opens_and_matching_rows() -> None:
    closes, opens, w = frames({"A": [100.0, 100.0]}, {"A": [100.0, 100.0]}, {"A": [1.0, 1.0]})
    with pytest.raises(ValueError, match="opens"):
        run(closes, w, CostModel(), fill=Fill.NEXT_OPEN)
    with pytest.raises(ValueError, match="rows"):
        run(closes, w, CostModel(), fill=Fill.NEXT_OPEN, opens=opens.iloc[:1])


def test_legacy_mode_is_the_default_and_unchanged() -> None:
    closes, opens, w = frames({"A": [100.0, 110.0]}, {"A": [1.0, 1.0]}, {"A": [1.0, 1.0]})
    legacy = run(closes, w, CostModel())
    explicit = run(closes, w, CostModel(), fill=Fill.SAME_CLOSE, opens=opens)
    assert legacy.equity["equity"].tolist() == explicit.equity["equity"].tolist() == [10_000.0, 11_000.0]


def bars_with_split() -> pd.DataFrame:
    # A 2-for-1 split between bars 1 and 2: raw prices halve, adjclose does not.
    return pd.DataFrame({
        "date": pd.bdate_range("2024-01-01", periods=4).date,
        "open": [200.0, 204.0, 101.0, 103.0],
        "high": [210.0, 208.0, 105.0, 106.0],
        "low": [198.0, 200.0, 100.0, 101.0],
        "close": [204.0, 206.0, 104.0, 105.0],
        "adjclose": [102.0, 103.0, 104.0, 105.0],
    })


def test_adjusted_open_handles_split() -> None:
    df = bars_with_split()
    assert adjust.factor(df).tolist() == [0.5, 0.5, 1.0, 1.0]
    assert adjust.adj_open(df).tolist() == [100.0, 102.0, 101.0, 103.0]
    assert adjust.adj_high(df).tolist() == [105.0, 104.0, 105.0, 106.0]
    assert adjust.adj_low(df).tolist() == [99.0, 100.0, 100.0, 101.0]
    bad = df.assign(close=[204.0, 0.0, 104.0, 105.0])
    with pytest.raises(ValueError, match="not positive"):
        adjust.factor(bad)


def test_aligned_adjusted_column_inner_joins_on_date() -> None:
    a = bars_with_split()
    b = bars_with_split().iloc[1:].reset_index(drop=True)
    out = adjust.aligned({"A": a, "B": b}, ["A", "B"], "low")
    assert len(out) == 3 and list(out.columns) == ["date", "A", "B"]
    assert np.allclose(out["A"], [100.0, 100.0, 101.0]) and np.allclose(out["B"], out["A"])
