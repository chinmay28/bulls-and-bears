import datetime as dt
from pathlib import Path

import pandas as pd
import pyarrow.parquet as pq
import pytest

from tt.data import bars as barsio
from tt.data import checks, yahoo
from tt.data.synthetic import cointegrated_pair


def good() -> pd.DataFrame:
    fetched = pd.Timestamp("2024-01-05T12:30:00.123456Z")
    return pd.DataFrame(
        {
            "date": [dt.date(2024, 1, 2), dt.date(2024, 1, 3), dt.date(2024, 1, 4)],
            "open": [100.0, 101.5, 103.0],
            "high": [102.0, 104.0, 103.5],
            "low": [99.0, 101.0, 101.5],
            "close": [101.0, 103.0, 102.0],
            "adjclose": [100.5, 102.5, 102.0],
            "volume": [1000, 2000, 1500],
            "source": "yahoo",
            "fetched_at": fetched,
        }
    )


def test_parquet_round_trip_keeps_the_schema(tmp_path: Path) -> None:
    p = tmp_path / "SPY.parquet"
    barsio.write_bars(good(), p)
    schema = pq.read_schema(p)
    assert str(schema.field("date").type) == "date32[day]"
    assert str(schema.field("fetched_at").type) == "timestamp[us, tz=UTC]"
    assert str(schema.field("volume").type) == "int64"
    back = barsio.read_bars(p)
    assert list(back.columns) == barsio.COLUMNS
    assert back["date"].tolist() == good()["date"].tolist()
    assert back["adjclose"].tolist() == [100.5, 102.5, 102.0]


@pytest.mark.parametrize(
    ("mutate", "name"),
    [
        (lambda d: d.__setitem__("date", [dt.date(2024, 1, 2)] * 3), "date"),
        (lambda d: d.loc[1, "close"].__class__ and d.__setitem__("close", [101.0, -1.0, 102.0]), "positive"),
        (lambda d: d.__setitem__("low", [99.0, 104.5, 101.5]), "low"),
        (lambda d: d.__setitem__("high", [102.0, 102.0, 103.5]), "high"),
        (lambda d: d.__setitem__("adjclose", [100.5, 206.0, 102.0]), "adjclose"),
    ],
)
def test_each_invariant(mutate, name) -> None:
    df = good()
    mutate(df)
    vs = checks.violations(df)
    assert vs, "expected a violation"
    assert name in {v.name for v in vs}
    with pytest.raises(ValueError):
        checks.check(df)


def test_clean_series_passes() -> None:
    assert checks.violations(good()) == []
    checks.check(good())


def test_stale_counts_weekdays() -> None:
    df = good()  # last bar Thursday Jan 4
    assert not checks.stale(df, dt.date(2024, 1, 9))  # Tuesday: 3 weekdays later
    assert checks.stale(df, dt.date(2024, 1, 10))  # Wednesday: 4


def test_mixed_sources() -> None:
    df = good()
    assert not checks.mixed_sources(df)
    df.loc[0, "source"] = "robinhood"
    assert checks.mixed_sources(df)


def fake_download(symbols, **kwargs) -> pd.DataFrame:
    idx = pd.to_datetime(["2024-01-02", "2024-01-03"])
    frames = {}
    for s in symbols:
        frames[s] = pd.DataFrame(
            {"Open": [1.0, 2.0], "High": [2.0, 3.0], "Low": [0.5, 1.5], "Close": [1.5, 2.5],
             "Adj Close": [1.4, 2.4], "Volume": [10, 20]}, index=idx,
        )
    return pd.concat(frames, axis=1)


def test_fetch_reshapes_and_stamps(tmp_path: Path) -> None:
    out = yahoo.fetch(["AAA", "BBB"], dt.date(2024, 1, 1), download=fake_download,
                      now=lambda: pd.Timestamp("2024-01-05T00:00:00Z"))
    assert set(out) == {"AAA", "BBB"}
    df = out["AAA"]
    assert list(df.columns) == barsio.COLUMNS
    assert df["source"].tolist() == ["yahoo", "yahoo"]
    assert df["adjclose"].tolist() == [1.4, 2.4]
    assert df["date"].tolist() == [dt.date(2024, 1, 2), dt.date(2024, 1, 3)]


def test_fetch_retries_then_gives_up() -> None:
    calls = []

    def flaky(*a, **k):
        calls.append(1)
        raise ConnectionError("nope")

    slept = []
    with pytest.raises(RuntimeError, match="giving up"):
        yahoo.fetch(["AAA"], dt.date(2024, 1, 1), download=flaky, retries=3, sleep=slept.append)
    assert len(calls) == 3
    assert slept == [1.0, 2.0, 4.0]


def test_refresh_appends_the_tail(tmp_path: Path) -> None:
    p = tmp_path / "AAA.parquet"
    barsio.write_bars(good(), p)

    def later(symbols, **kwargs):
        assert kwargs["start"] == "2024-01-05"
        idx = pd.to_datetime(["2024-01-05"])
        return pd.concat({"AAA": pd.DataFrame(
            {"Open": [102.0], "High": [103.0], "Low": [101.0], "Close": [102.5],
             "Adj Close": [102.5], "Volume": [7]}, index=idx)}, axis=1)

    merged = yahoo.refresh("AAA", p, download=later, now=lambda: pd.Timestamp("2024-01-06T00:00:00Z"))
    assert len(merged) == 4
    assert barsio.read_bars(p)["date"].iloc[-1] == dt.date(2024, 1, 5)


def test_synthetic_pair_is_clean_and_deterministic() -> None:
    a = cointegrated_pair(n=50, seed=1)
    b = cointegrated_pair(n=50, seed=1)
    for s in ("A", "B"):
        checks.check(a[s])
        assert a[s]["close"].tolist() == b[s]["close"].tolist()
    assert cointegrated_pair(n=50, seed=2)["A"]["close"].iloc[-1] != a["A"]["close"].iloc[-1]
