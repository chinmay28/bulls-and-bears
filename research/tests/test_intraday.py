import datetime as dt

import pandas as pd
import pytest

from tt.data import intraday


def yf_frame(tz: str = "UTC") -> pd.DataFrame:
    index = pd.DatetimeIndex(
        ["2026-09-08 13:30", "2026-09-08 14:00", "2026-09-08 19:00"]
    ).tz_localize(tz)  # 09:30, 10:00 and 15:00 in New York
    return pd.DataFrame(
        {
            "Open": [100.0, 101.0, 102.0],
            "High": [100.5, 101.5, 102.5],
            "Low": [99.5, 100.5, 101.5],
            "Close": [100.2, 101.2, 102.2],
            "Volume": [1000, 2000, 1500],
        },
        index=index,
    )


FETCHED = pd.Timestamp("2026-09-09T00:00:00Z")


def test_to_bars_puts_the_stamps_on_the_exchange_clock():
    out = intraday.to_bars(yf_frame(), FETCHED)
    assert list(out.columns) == intraday.COLUMNS
    assert [t.strftime("%H:%M") for t in out["ts"]] == ["09:30", "10:00", "15:00"]
    assert out["source"].tolist() == ["yahoo"] * 3
    assert out["volume"].dtype == "int64"


def test_naive_stamps_are_refused_because_the_time_of_day_is_unusable():
    raw = yf_frame()
    raw.index = pd.DatetimeIndex(raw.index).tz_localize(None)
    with pytest.raises(ValueError, match="naive timestamps"):
        intraday.to_bars(raw, FETCHED)


def test_a_repeated_stamp_keeps_one_bar():
    raw = pd.concat([yf_frame(), yf_frame().iloc[[1]]])
    assert len(intraday.to_bars(raw, FETCHED)) == 3


@pytest.mark.parametrize(
    ("column", "value", "message"),
    [("Low", [99.5, 200.0, 101.5], "low is above"),
     ("High", [100.5, 100.9, 102.5], "high is below"),
     ("Open", [100.0, -1.0, 102.0], "open is not positive")],
)
def test_verify_rejects_a_broken_bar(column, value, message):
    raw = yf_frame()
    raw[column] = value
    with pytest.raises(ValueError, match=message):
        intraday.to_bars(raw, FETCHED)


@pytest.mark.parametrize(("interval", "minutes"), [("1m", 1), ("30m", 30), ("90m", 90), ("1h", 60), ("60m", 60)])
def test_interval_minutes(interval, minutes):
    assert intraday.interval_minutes(interval) == minutes


def test_an_unknown_interval_lists_the_known_ones():
    with pytest.raises(ValueError, match="unknown interval"):
        intraday.interval_minutes("7m")


@pytest.mark.parametrize(
    ("interval", "days", "ok"),
    [("30m", 60, True), ("30m", 61, False), ("1h", 730, True), ("1h", 731, False), ("1m", 30, True)],
)
def test_check_period_holds_to_yahoos_limits(interval, days, ok):
    if ok:
        intraday.check_period(interval, days)
    else:
        with pytest.raises(ValueError, match="serves at most"):
            intraday.check_period(interval, days)


@pytest.mark.parametrize(
    ("interval", "when", "ok"),
    [("30m", dt.time(10, 0), True), ("30m", dt.time(15, 0), True), ("30m", dt.time(10, 15), False),
     ("1h", dt.time(10, 30), True), ("1h", dt.time(10, 0), False), ("30m", dt.time(9, 0), False)],
)
def test_check_grid_only_allows_times_a_bar_starts_at(interval, when, ok):
    if ok:
        intraday.check_grid(interval, when)
    else:
        with pytest.raises(ValueError, match=r"no .* bar starts at"):
            intraday.check_grid(interval, when)


def test_check_grid_names_the_neighbouring_bars():
    with pytest.raises(ValueError, match="09:30 and 10:30 are the neighbours"):
        intraday.check_grid("1h", dt.time(10, 0))


def test_fetch_never_asks_for_more_than_yahoo_serves():
    def refuse(*args, **kwargs):
        raise AssertionError("fetch should have refused before the network")

    with pytest.raises(ValueError, match="serves at most"):
        intraday.fetch(["VTI"], interval="30m", days=730, download=refuse)


def test_fetch_splits_a_grouped_frame_by_symbol():
    grouped = pd.concat({"VTI": yf_frame(), "QQQ": yf_frame() * 2}, axis=1)
    got = intraday.fetch(["VTI", "QQQ"], interval="30m", days=5,
                         download=lambda *a, **k: grouped, now=lambda: FETCHED)
    assert set(got) == {"VTI", "QQQ"}
    assert got["QQQ"]["open"].iloc[0] == pytest.approx(200.0)


def test_fetch_retries_then_gives_up_without_sleeping_for_real():
    waits: list[float] = []
    calls = {"n": 0}

    def flaky(*args, **kwargs):
        calls["n"] += 1
        if calls["n"] < 3:
            raise RuntimeError("yahoo hiccup")
        return yf_frame()

    got = intraday.fetch(["VTI"], interval="30m", days=5, download=flaky,
                         sleep=waits.append, now=lambda: FETCHED)
    assert calls["n"] == 3 and waits == [1.0, 2.0]
    assert len(got["VTI"]) == 3

    def broken(*args, **kwargs):
        raise RuntimeError("still down")

    with pytest.raises(RuntimeError, match="giving up after 4 attempts"):
        intraday.fetch(["VTI"], interval="30m", days=5, download=broken, sleep=waits.append)


def test_an_empty_answer_is_an_error_not_an_empty_study():
    with pytest.raises(RuntimeError, match="no 30m data"):
        intraday.fetch(["VTI"], interval="30m", days=5, download=lambda *a, **k: pd.DataFrame())
