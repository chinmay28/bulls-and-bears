import datetime as dt

import pandas as pd
import pytest

from tt.stats import session


def bars(rows: list[tuple[str, float]], tz: str = "America/New_York") -> pd.DataFrame:
    ts = pd.DatetimeIndex([pd.Timestamp(t) for t, _ in rows]).tz_localize(tz)
    return pd.DataFrame({"ts": ts, "open": [p for _, p in rows], "close": [p for _, p in rows]})


DAY = [
    ("2026-09-08 09:30", 100.0),
    ("2026-09-08 10:00", 101.0),
    ("2026-09-08 15:00", 102.0),
    ("2026-09-08 15:30", 103.0),
]


def test_the_hold_is_open_to_open_between_the_two_times():
    s = session.sessions(bars(DAY), dt.time(10, 0), dt.time(15, 0))
    assert list(s.frame.columns) == session.COLUMNS
    assert s.frame["entry"].tolist() == [101.0]
    assert s.frame["exit"].tolist() == [102.0]
    assert s.returns.iloc[0] == pytest.approx(102.0 / 101.0 - 1.0)
    assert s.dropped == []


def test_a_day_missing_either_leg_is_dropped_not_zeroed():
    half = [*DAY[:3], ("2026-09-09 10:00", 50.0)]  # 2026-09-09 closes early: no 15:00 print
    s = session.sessions(bars(half), dt.time(10, 0), dt.time(15, 0))
    assert s.frame["date"].tolist() == [dt.date(2026, 9, 8)]
    assert s.dropped == [dt.date(2026, 9, 9)]


def test_the_time_of_day_is_the_exchange_clock_across_a_dst_change():
    # 2026-11-01 is when New York leaves daylight time; 10:00 stays 10:00.
    rows = [("2026-10-30 10:00", 10.0), ("2026-10-30 15:00", 11.0),
            ("2026-11-02 10:00", 20.0), ("2026-11-02 15:00", 21.0)]
    s = session.sessions(bars(rows), dt.time(10, 0), dt.time(15, 0))
    assert s.frame["date"].tolist() == [dt.date(2026, 10, 30), dt.date(2026, 11, 2)]
    assert s.returns.tolist() == pytest.approx([11 / 10 - 1, 21 / 20 - 1])


@pytest.mark.parametrize(
    ("mutate", "message"),
    [
        (lambda d: d.drop(columns=["ts"]), "no 'ts' column"),
        (lambda d: d.assign(ts=pd.DatetimeIndex(d["ts"]).tz_localize(None)), "timezone-aware"),
        (lambda d: pd.concat([d, d.iloc[[1]]], ignore_index=True), "two 10:00 bars"),
    ],
)
def test_refusals(mutate, message):
    with pytest.raises(ValueError, match=message):
        session.sessions(mutate(bars(DAY)), dt.time(10, 0), dt.time(15, 0))


def test_entry_must_precede_the_exit():
    with pytest.raises(ValueError, match="not before"):
        session.sessions(bars(DAY), dt.time(15, 0), dt.time(10, 0))


def test_a_missing_price_column_is_named():
    with pytest.raises(ValueError, match="no 'vwap' column"):
        session.sessions(bars(DAY), dt.time(10, 0), dt.time(15, 0), price="vwap")


def test_net_charges_the_round_trip_once():
    r = pd.Series([0.0010, -0.0005])
    assert session.net(r, 2.0).tolist() == pytest.approx([0.0008, -0.0007])
    with pytest.raises(ValueError, match="negative"):
        session.net(r, -1.0)


def test_summary_counts_only_strictly_positive_days():
    s = session.summarize(pd.Series([0.01, -0.01, 0.0, 0.02]))
    assert (s.days, s.wins) == (4, 2)
    assert s.win_rate == 0.5
    assert s.days_per_year == pytest.approx(126.0)
    assert s.median == pytest.approx(0.005)


def test_summary_standard_error_shrinks_with_the_sample():
    few = session.summarize(pd.Series([0.01, -0.01] * 30))
    many = session.summarize(pd.Series([0.01, -0.01] * 300))
    assert few.win_rate == many.win_rate == 0.5
    assert few.win_rate_se == pytest.approx(0.5 / 60**0.5)
    assert many.win_rate_se < few.win_rate_se


def test_summary_of_nothing_is_not_a_zero_win_rate():
    s = session.summarize(pd.Series([], dtype=float))
    assert s.days == 0
    assert s.win_rate != s.win_rate  # NaN: no evidence, not a losing strategy


def test_the_t_stat_scales_with_the_sample():
    small = session.summarize(pd.Series([0.002, 0.001, -0.001, 0.003] * 5))
    big = session.summarize(pd.Series([0.002, 0.001, -0.001, 0.003] * 20))
    assert big.mean == pytest.approx(small.mean)
    # sqrt(n), up to the ddof=1 correction on the two sample deviations.
    assert big.t_stat == pytest.approx(small.t_stat * 2.0, rel=0.03)


def test_the_line_reports_basis_points():
    line = session.summarize(pd.Series([0.001, -0.001, 0.002])).line("VTI 10:00-15:00")
    assert "VTI 10:00-15:00" in line and "bp" in line and "days" in line
