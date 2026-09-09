# research

The Python half of Bulls and Bears: where a strategy is found, backtested
honestly, sized, and turned into a spec the Go runtime will run. It owns the
data fetch, the statistics, the backtest engine, the strategies' signal
functions and the golden files. It never talks to a broker, and nothing here
runs unattended.

```
tt/data/        bars.py (the §4.1 Parquet schema), yahoo.py, checks.py, synthetic.py,
                adjust.py (adjusted open/high/low from adjclose/close),
                intraday.py (bars inside a session, and what Yahoo will not serve of them)
tt/stats/       adf, engle_granger, johansen, halflife_ar1, kelly,
                session.py (what a hold between two times of day did, day by day)
tt/indicators/  rolling mean/sd/z-score/return, SMA, Donchian, realised vol, ATR, Wilder RSI —
                the calculations the strategies share, per docs/CONTRACTS.md "Indicators"
tt/backtest/    engine.py (same-close and next-open fills), metrics.py, walkforward.py
tt/strategies/  pairs.py, ratio.py, trend.py, momentum.py, time_series_momentum.py,
                donchian.py, risk_parity.py, rsi2.py — one strategy each, per docs/CONTRACTS.md
tt/studies/     a sweep two scripts share (dual_momentum.py: dual momentum and sector rotation)
tt/study.py     what every study script shares: flags, data, windows, the sweep record,
                the cost stress, the verdict
tt/spec.py      writes specs (validated against specs/strategy.schema.json) and goldens
scripts/        one study each, docs/strategies/ has the write-up of each:
                  gld_gdx.py (pairs), etf_gld_ratio.py, sma_trend.py, dual_momentum.py
                  time_series_momentum.py, donchian_breakout.py, risk_parity_trend.py,
                  rsi2_reversion.py, sector_rotation.py
                golden_next_open.py and golden_indicators.py write the engine and indicator
                parity fixtures from synthetic series (no network)
                intraday_window.py answers a question rather than emitting a spec: how often
                a hold between two times of day paid
```

docs/strategies/SOURCES.md is the catalogue of where every strategy was
borrowed from and what was surveyed and set aside.

## Running it

```sh
uv sync                                  # once
uv run pytest                            # no network in tests
uv run ruff check . && uv run mypy --strict tt

uv run python scripts/gld_gdx.py         # fetch GLD and GDX from Yahoo, backtest, emit
uv run python scripts/gld_gdx.py --synthetic   # the seeded pair, for parity without network

uv run python scripts/etf_gld_ratio.py         # SPY, QQQ, VTI, XLK against GLD, from Yahoo

uv run python scripts/intraday_window.py                        # VTI and QQQ, 10:00-15:00 ET, 60 days of 30m bars
uv run python scripts/intraday_window.py --interval 1h --days 730 --window 10:30-15:30
uv run python scripts/etf_gld_ratio.py --csv-dir DIR   # from Kaggle-format SYMBOL.csv files
```

Every script takes `--universe SPY,QQQ,GLD` (the haven last; a pairs study
takes exactly two), `--train-to`, and `--specs-dir`, `--bars-dir` and
`--out-dir` to put a promoted spec, the fetched bars and a rejected spec
somewhere other than the checkout; the app's Research tab uses these to run
a study on the trading machine with the output pointed at the runtime's own
directories. A study refuses a test window under 252 bars.

Each script promotes its spec (`specs/gld_gdx_pairs.yaml`, `specs/etf_gld_ratio.yaml`) only when the out-of-sample
Sharpe clears 1.0 on real data; otherwise the spec goes to `research/out/` as
rejected, so the runtime never sees a strategy that did not earn it. Every
study also leaves its whole sweep (`sweep.csv`), its out-of-sample numbers
(`oos.json`) and a slippage stress with the neighbouring parameter sets
(`robustness.json`) under `research/out/<study>/<stamp>/`, so a result can
be audited without re-running it. The spec records which fill the numbers
were earned with (`execution.fill_at`; the newer studies use `next_open`)
and which study produced it (`provenance.research_study`). The
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
