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
tt/strategies/  pairs.py — pairs_zscore per docs/CONTRACTS.md
tt/spec.py      writes specs (validated against specs/strategy.schema.json) and goldens
scripts/        gld_gdx.py, the study that produces the first spec
```

## Running it

```sh
uv sync                                  # once
uv run pytest                            # no network in tests
uv run ruff check . && uv run mypy --strict tt

uv run python scripts/gld_gdx.py         # fetch GLD and GDX from Yahoo, backtest, emit
uv run python scripts/gld_gdx.py --synthetic   # the seeded pair, for parity without network
```

The script promotes `specs/gld_gdx_pairs.yaml` only when the out-of-sample
Sharpe clears 1.0 on real data; otherwise the spec goes to `research/out/` as
rejected, so the runtime never sees a strategy that did not earn it. The
goldens under `golden/gld_gdx/` are written either way, because parity between
the two implementations is worth checking whether or not the strategy is any
good. `make golden` runs the script; `make parity` runs the Go tests that read
its output.

## Caveats that matter

Yahoo Finance is unofficial and survivorship-biased: delisted tickers vanish
from it. That is acceptable for ETF pairs like GLD/GDX and must be flagged for
any stock basket. `yfinance` is pinned to an exact version and every fetch is
retried; `Adj Close` folds splits and dividends in and is what returns are
computed from.

The goldens committed at the moment come from `--synthetic`: the machine that
built this tree could not reach Yahoo. Run the script for real once, commit
what it writes, and the Go parity tests hold both implementations to the real
series from then on.
