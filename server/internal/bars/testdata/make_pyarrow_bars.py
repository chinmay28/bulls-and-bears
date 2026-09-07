# Makes pyarrow_bars.parquet the way the research side will: pandas.to_parquet
# through pyarrow. Same three bars as good() in bars_test.go, except the last
# is from robinhood so MixedSources has something to find. Regenerate with
#   uv run --python 3.11 --with pyarrow --with pandas python make_pyarrow_bars.py
import pandas as pd, pyarrow.parquet as pq, datetime as dt
import os
out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "pyarrow_bars.parquet")
fa = dt.datetime(2024, 1, 5, 12, 30, 0, 123456, tzinfo=dt.timezone.utc)
rows = [
  # Column order deliberately differs from the schema in docs/PLAN.md §4.1.
  dict(source="yahoo", close=101.0, open=100.0, high=102.0, low=99.0, adjclose=100.5, volume=1000, date=dt.date(2024,1,2), fetched_at=fa),
  dict(source="yahoo", close=103.0, open=101.5, high=104.0, low=101.0, adjclose=102.5, volume=2000, date=dt.date(2024,1,3), fetched_at=fa),
  dict(source="robinhood", close=102.0, open=103.0, high=103.5, low=101.5, adjclose=102.0, volume=1500, date=dt.date(2024,1,4), fetched_at=fa),
]
df = pd.DataFrame(rows)
df["fetched_at"] = df["fetched_at"].astype("datetime64[us, UTC]")
df["source"] = df["source"].astype("string")
df.index = pd.Index([10, 11, 12])  # non-default index: pandas writes __index_level_0__
df.to_parquet(out, engine="pyarrow", index=True)
print(pq.read_schema(out))
print(pq.read_table(out).to_pandas())
