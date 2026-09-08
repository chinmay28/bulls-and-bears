# research

The Python half of Bulls and Bears: where a strategy is found, backtested
honestly, sized, and turned into a spec the Go runtime will run. It owns the
data fetch, the statistics, the backtest engine, the strategies' signal
functions and the golden files. It never talks to a broker, and nothing here
runs unattended.

```
tt/data/        bars.py (the §4.1 Parquet schema), yahoo.py, checks.py, synthetic.py
tt/stats/       adf, engle_granger, johansen, halflife_ar1, kelly
tt/backtest/    engine.py, metrics.py, walkforward.py
tt/strategies/  pairs.py — pairs_zscore; ratio.py — ratio_reversion (both per docs/CONTRACTS.md)
tt/spec.py      writes specs (validated against specs/strategy.schema.json) and goldens
scripts/        gld_gdx.py, the study that produces the first spec
                etf_gld_ratio.py, the ETF/GLD ratio study (docs/strategies/etf_gld_ratio.md)
```

## Running it

```sh
uv sync                                  # once
uv run pytest                            # no network in tests
uv run ruff check . && uv run mypy --strict tt

uv run python scripts/gld_gdx.py         # fetch GLD and GDX from Yahoo, backtest, emit
uv run python scripts/gld_gdx.py --synthetic   # the seeded pair, for parity without network

uv run python scripts/etf_gld_ratio.py         # SPY, QQQ, VTI, XLK against GLD, from Yahoo
uv run python scripts/etf_gld_ratio.py --csv-dir DIR   # from Kaggle-format SYMBOL.csv files
```

Both scripts take `--specs-dir`, `--bars-dir` and `--out-dir` to put a
promoted spec, the fetched bars and a rejected spec somewhere other than the
checkout; the app's Research tab uses these to run a study on the trading
machine with the output pointed at the runtime's own directories.

Each script promotes its spec (`specs/gld_gdx_pairs.yaml`, `specs/etf_gld_ratio.yaml`) only when the out-of-sample
Sharpe clears 1.0 on real data; otherwise the spec goes to `research/out/` as
rejected, so the runtime never sees a strategy that did not earn it. The
goldens under `golden/gld_gdx/` and `golden/etf_gld_ratio/` are written either
way, because parity between the two implementations is worth checking whether
or not the strategy is any good. `make golden` and `make golden-ratio` run the
scripts; `make parity` runs the Go tests that read their output.

## Caveats that matter

Yahoo Finance is unofficial and survivorship-biased: delisted tickers vanish
from it. That is acceptable for ETF pairs like GLD/GDX and must be flagged for
any stock basket. `yfinance` is pinned to an exact version and every fetch is
retried; `Adj Close` folds splits and dividends in and is what returns are
computed from.

The GLD/GDX goldens committed at the moment come from `--synthetic`, and the
ETF/GLD ratio goldens from `--csv-dir` over a Kaggle mirror that ends in
November 2017: the machines that built this tree could not reach Yahoo. Run
the scripts for real once, commit what they write, and the Go parity tests
hold both implementations to the real series from then on.
