# 0. Purpose and framing

This document is a build plan for a personal algorithmic trading toolkit. It is written to be handed to a Claude Code session as the authoritative spec. When in doubt, follow the principles in §1; when the plan and reality conflict (e.g. the Robinhood MCP exposes different tools than assumed), record the discrepancy in `docs/DISCOVERY.md` and adapt the affected component, not the architecture.

The methodology comes from Ernie Chan, *Quantitative Trading: How to Build Your Own Algorithmic Trading Business* (Wiley, 2008). Its core loop is:

1. Source a simple, plausible strategy — borrow, don't invent.
2. Backtest it honestly: eliminate look-ahead, data-snooping, and survivorship bias; model transaction costs.
3. Size positions with Kelly, then trade at **half-Kelly** because the edge is overestimated.
4. Paper-trade, compare live to backtest, and treat the gap as a bug report.
5. Go live small. Reduce size in drawdowns, never scale up into them.

Everything below is infrastructure for that loop. The LLM is **not** in the trading decision path. Claude Code is a build tool and a research assistant; the runtime is deterministic.

# 1. Principles (non-negotiable)

- **Polyglot by fitness.** Python where the quant ecosystem is decisive (research, stats, backtesting, tear sheets). Go where a small, boring, single-binary daemon must run unattended (MCP client, paper broker, risk gate, scheduler).
- **Small components with explicit contracts.** Every component has an interface, a unit test suite, and a one-paragraph README stating what it owns and what it does not. Prefer 40-line packages with 200 lines of tests.
- **Strategy is a pure function.** `Targets(bars, positions) -> target_positions`. No I/O, no clock, no randomness. This is what makes backtest, dry-run, and live share one code path.
- **Dry-run is a broker implementation, not a code branch.** `--dry-run` swaps `RobinhoodBroker` for `PaperBroker`. Nothing upstream changes.
- **Provenance everywhere.** Every bar carries its `source`. Every strategy spec carries its backtest window, out-of-sample metrics, and git SHA. Every order decision is logged before it is sent.
- **Guardrails are code, not prompts.** Risk limits are enforced in the Go `RiskGate`, which runs identically in dry-run and live. Robinhood's disclosures are explicit that an agent authorized to trade without confirmation will do so; the toolkit never relies on the agent "being careful".
- **Fail closed.** Missing data, stale data, a spec older than its TTL, a schema mismatch, an unreachable broker — all halt the run and log why. Never trade on a guess.

# 2. Platform facts and constraints (verify in Phase 0)

From Robinhood's Agentic Trading documentation (`robinhood.com/us/en/support/articles/agentic-trading-overview/`) and launch coverage, as of this writing:

- The Robinhood Trading MCP endpoint is `https://agent.robinhood.com/mcp/trading` (streamable HTTP, OAuth). Authentication and Agentic-account onboarding complete only on a **desktop** browser; the OAuth redirect lands on `localhost`.
- The agent gets **read access** to all accounts (positions, balances, order history, watchlists, scans) but can **place trades only in the dedicated Agentic account**. Fund that account only with the amount you want at risk.
- **There is no paper-trading / sandbox mode.** Every order through the MCP is real. Paper trading must be implemented on our side (`PaperBroker`) against live quotes.
- Equities launched first; options and crypto support have been rolling out. Assume equities/ETFs only for v1.
- **Unknown until verified:** whether the MCP exposes historical OHLCV bars or only current quotes; exact tool names, argument schemas, order-status/fill semantics, and rate limits. Phase 0 exists to pin these down.

Data fallback: **Yahoo Finance via `yfinance`** (unofficial API; pin the version, cache aggressively, treat as unreliable). Its `Adj Close` folds in splits and dividends — use it for returns. Yahoo data is survivorship-biased (delisted tickers vanish), which is acceptable for ETF strategies and must be flagged for any stock-basket strategy.

# 3. Repository layout

```
trading-toolkit/
├── README.md                  # what this is, how to run each half
├── docs/
│   ├── PLAN.md                # this document
│   ├── DISCOVERY.md           # Phase 0 findings: MCP tools, schemas, quirks
│   └── STRATEGY_TEMPLATE.md   # hypothesis / universe / signal / sizing / exit / invalidation
├── data/                      # gitignored; Parquet bars, one file per symbol
│   └── bars/{SYMBOL}.parquet
├── specs/                     # strategy specs emitted by research, consumed by runtime
│   ├── strategy.schema.json
│   └── gld_gdx_pairs.yaml
├── golden/                    # cross-language parity fixtures
│   └── gld_gdx/{bars.parquet, expected_signals.csv}
├── research/                  # Python: uv-managed project
│   ├── pyproject.toml
│   ├── tt/                    # package
│   │   ├── data/  (yahoo.py, cache.py, checks.py)
│   │   ├── stats/ (stationarity.py, cointegration.py, halflife.py, kelly.py)
│   │   ├── backtest/ (engine.py, costs.py, metrics.py, walkforward.py)
│   │   ├── strategies/ (pairs.py)
│   │   └── spec.py            # writes specs/*.yaml + golden signals
│   ├── notebooks/
│   └── tests/
└── runtime/                   # Go module: github.com/chinmay28/trading-toolkit/runtime
    ├── cmd/tt/                # single binary: tt backtest | tt run [--dry-run] | tt discover
    └── internal/
        ├── bars/              # Parquet reader, Bar type, sanity checks
        ├── spec/              # spec loader + validation + TTL
        ├── strategy/          # Strategy interface + pairs implementation + parity test
        ├── broker/            # Broker interface; paper/ and robinhood/ implementations
        ├── marketdata/        # MarketData interface; robinhood/ implementation
        ├── mcp/               # minimal MCP client: OAuth, JSON-RPC, tools/list, tools/call
        ├── risk/              # RiskGate
        ├── metrics/           # Sharpe, max DD, DD duration (must match Python to tolerance)
        ├── journal/           # append-only JSONL event log
        └── sched/             # daily-at-close loop with market calendar
```

# 4. Shared contracts

## 4.1 Bars (Parquet)

One file per symbol at `data/bars/{SYMBOL}.parquet`. Python writes; Go reads (`parquet-go`). Schema:

| column     | type            | notes                                        |
|------------|-----------------|----------------------------------------------|
| `date`     | date32          | trading day, exchange-local                  |
| `open`     | float64         | raw                                          |
| `high`     | float64         | raw                                          |
| `low`      | float64         | raw                                          |
| `close`    | float64         | raw                                          |
| `adjclose` | float64         | split+dividend adjusted; use for returns     |
| `volume`   | int64           |                                              |
| `source`   | string          | `yahoo` \| `robinhood` \| other              |
| `fetched_at` | timestamp[us] | UTC                                          |

Invariants enforced on load (both languages): strictly increasing `date`; all prices > 0; `low <= min(open, close) <= max(open, close) <= high`; `adjclose <= close` unless a flagged exception; warn if last bar is older than 3 trading days; **error** if a single symbol mixes sources within a backtest window unless `--allow-mixed-sources` is set.

## 4.2 Strategy spec (YAML, validated by `specs/strategy.schema.json`)

```yaml
name: gld_gdx_pairs
version: 1
strategy: pairs_zscore            # must match a registered Go + Python implementation
universe: [GLD, GDX]
params:
  hedge_ratio: 1.631              # from cointegrating regression on training window
  lookback: 20                    # bars for spread mean/std
  entry_z: 2.0
  exit_z: 0.5
  max_hold_days: 30               # ~3x half-life; hard time stop
sizing:
  gross_leverage: 0.9             # half-Kelly, capped
  max_notional_per_leg_usd: 2000
provenance:
  train_window: {from: 2015-01-01, to: 2022-12-31}
  test_window:  {from: 2023-01-01, to: 2026-08-31}
  oos_sharpe: 1.32
  oos_max_drawdown: -0.087
  cost_model: {commission_usd: 0, slippage_bps: 5}
  research_git_sha: "abc1234"
  generated_at: 2026-09-06T00:00:00Z
  ttl_days: 90                    # runtime refuses to run a stale spec
```

Rule: the runtime **refuses** any spec missing `provenance`, with `oos_sharpe < 1.0`, or older than `ttl_days`.

## 4.3 Signal parity (golden files)

The signal is implemented twice — Python for backtest, Go for live. Divergence is a silent, expensive bug. Therefore, for each strategy:

- Python writes `golden/{name}/bars.parquet` (a fixed window) and `expected_signals.csv` (`date, symbol, target_weight`).
- A Go test loads both and asserts `strategy.Targets` matches every row within `1e-9` on weights (or a documented tolerance if float order-of-operations differs).
- CI fails on drift. Regenerate goldens only via an explicit `make golden` and commit both together.

## 4.4 Metrics parity

`Sharpe` (annualized, daily returns, rf = 0), `MaxDrawdown`, `MaxDrawdownDuration` are implemented in both languages and cross-checked against a golden equity curve to `1e-6`. Definitions follow Chan Example 3.4 and 3.5.

## 4.5 Journal (JSONL, append-only)

One line per event, `ts` in UTC, `run_id`, `mode: dry-run|live`. Event kinds: `quote`, `targets`, `risk_decision`, `order_submitted`, `fill`, `error`, `halt`. Every `order_submitted` must be preceded by a `risk_decision` with `allowed: true` referencing the same `intent_id`.

# 5. Go runtime interfaces

```go
package bars
type Bar struct {
    Date                 time.Time
    Open, High, Low      float64
    Close, AdjClose      float64
    Volume               int64
    Source               string
}

package marketdata
type Quote struct { Symbol string; Bid, Ask, Last float64; At time.Time }
type MarketData interface {
    Quotes(ctx context.Context, symbols []string) ([]Quote, error)
}
type HistoricalData interface { // may be unimplemented for Robinhood
    Bars(ctx context.Context, symbol string, from, to time.Time) ([]bars.Bar, error)
}

package strategy
type Position struct { Symbol string; Qty float64; AvgCost float64 }
type Strategy interface {
    // Pure. Returns target weights (fraction of equity) per symbol.
    Targets(hist map[string][]bars.Bar, pos []Position) (map[string]float64, error)
    Universe() []string
}

package broker
type Side int
type Order struct { IntentID, Symbol string; Side Side; Qty, Limit float64 }
type Fill  struct { OrderID, IntentID, Symbol string; Qty, Price float64; At time.Time }
type Broker interface {
    Equity(ctx context.Context) (float64, error)
    BuyingPower(ctx context.Context) (float64, error)
    Positions(ctx context.Context) ([]strategy.Position, error)
    Place(ctx context.Context, o Order) (orderID string, err error)
    Fills(ctx context.Context, since time.Time) ([]Fill, error)
    Cancel(ctx context.Context, orderID string) error
}

package risk
type Decision struct { IntentID string; Allowed bool; Reason string }
type Gate interface {
    Check(ctx context.Context, intents []broker.Order, state State) []Decision
}
```

`PaperBroker` implements `broker.Broker`: fills market/limit orders against the latest `Quote` at bid/ask plus a configurable slippage model (bps + optional volume-impact term), charges the cost model from the spec, persists equity/positions/fills to SQLite so restarts don't reset the paper book.

`RobinhoodBroker` and `RobinhoodMarketData` wrap `mcp.Client`. Tool names and argument shapes are filled in from `docs/DISCOVERY.md` after Phase 0.

# 6. Risk gate rules (v1)

Applied in dry-run and live, identically. All thresholds live in a `risk.yaml`, not in code.

| rule                          | default                      | action                                        |
|-------------------------------|------------------------------|-----------------------------------------------|
| max notional per symbol       | from spec                    | reject intent                                 |
| max gross leverage            | spec `gross_leverage`        | scale all intents down proportionally         |
| max orders per day            | 10                           | reject beyond                                 |
| daily loss limit              | 3% of start-of-day equity    | no new opening orders; closes allowed         |
| drawdown kill switch          | 10% from equity high-water   | halt; require manual `tt resume`              |
| 3 consecutive daily-limit hits| —                            | halt for remainder of week                    |
| stale quote                   | > 15 min old                 | reject intent                                 |
| stale spec                    | > `ttl_days`                 | refuse to start                               |
| live/paper divergence         | paper vs live equity > 2%    | warn; > 5% halt                               |
| confirmation threshold (live) | > $500 single order          | require interactive confirm unless `--yes`    |

# 7. Phased plan

Each phase ends with a **definition of done** (DoD). Do not start the next phase until the DoD is green in CI.

## Phase 0 — Discovery (≈1 day)

1. From Claude Code: `claude mcp add robinhood-trading --transport http https://agent.robinhood.com/mcp/trading`, authenticate on desktop, complete Agentic-account onboarding. Do **not** fund beyond the minimum yet.
2. Call `tools/list`. Save the full JSON to `docs/discovery/tools_list.json`.
3. For each read-only tool, call it once with benign arguments and save request/response pairs to `docs/discovery/samples/`.
4. Write `docs/DISCOVERY.md` answering: Are historical bars available? What granularity? What quote fields (bid/ask/last/timestamp)? What order types and TIF? How are order status and fills reported? Any rate limits or auth-refresh quirks?
5. Decide and record: `HistoricalData` primary source = Robinhood or Yahoo.

**DoD:** `docs/DISCOVERY.md` committed; `tt discover` (Go) can complete OAuth, call `tools/list`, and print it.

## Phase 1 — Python research foundation

1. `research/` as a `uv` project: pandas, pyarrow, yfinance (pinned), statsmodels, vectorbt, quantstats, pytest, pyyaml, jsonschema.
2. `tt/data/yahoo.py`: fetch daily bars for a symbol list; write Parquet per §4.1 with `source=yahoo`; incremental tail refresh; retry/backoff. Tests use recorded fixtures — no network in tests.
3. `tt/data/checks.py`: the §4.1 invariants as pure functions with tests.
4. `tt/stats/`: `adf`, `engle_granger`, `johansen` (thin wrappers with clear return types), `halflife_ar1`, `kelly_leverage(mean, var)`, `half_kelly`. Property/regression tests against known series.
5. `tt/backtest/`: a small daily-bar engine (or vectorbt wrapper) that consumes a `Strategy` (Python protocol mirroring §5), a cost model, and emits an equity curve. `metrics.py` per §4.4. `walkforward.py` for rolling train/test windows.
6. `tt/strategies/pairs.py`: `pairs_zscore` per the spec params. Pure function.
7. Notebook `notebooks/01_gld_gdx.ipynb`: reproduce Chan Example 3.6 / 7.2 / 7.5 — cointegration test, hedge ratio, half-life, z-score strategy, walk-forward, tear sheet. Record whether **net-of-cost OOS Sharpe ≥ 1.0**. If not, iterate on strategy choice here, not on infrastructure.
8. `tt/spec.py`: emit `specs/gld_gdx_pairs.yaml` with full provenance and `golden/gld_gdx/*`.

Optional research aid: install `agiprolabs/claude-trading-skills` as a Claude Code plugin for this phase only (its `mean-reversion`, `cointegration-analysis`, `walk-forward-validation`, `kelly-criterion`, and `risk-management` skills are useful checklists; its code is not to be vendored).

**DoD:** `uv run pytest` green; `data/bars/GLD.parquet` and `GDX.parquet` exist; a spec with `oos_sharpe` populated and golden files committed; `docs/STRATEGY_TEMPLATE.md` filled in for `gld_gdx_pairs`.

## Phase 2 — Go core (offline)

1. `internal/bars`: Parquet reader + invariants + tests on golden bars.
2. `internal/spec`: load, validate against `strategy.schema.json`, enforce TTL and `oos_sharpe` floor.
3. `internal/strategy/pairs`: `pairs_zscore` implementation + **parity test** against `golden/gld_gdx/expected_signals.csv`.
4. `internal/metrics`: Sharpe/MaxDD/DD-duration + parity test against golden equity curve.
5. `internal/broker/paper`: `PaperBroker` with SQLite state, slippage model, cost model; table-driven tests for fills, partial fills, insufficient buying power, restart persistence.
6. `internal/risk`: `RiskGate` with every §6 rule table-tested, including the "closes allowed after daily limit" nuance.
7. `internal/journal`: JSONL writer + reader + invariant checker (`order_submitted` must follow an allowed `risk_decision`).
8. `cmd/tt backtest --spec specs/gld_gdx_pairs.yaml --from --to`: replays Parquet bars through Strategy → RiskGate → PaperBroker and prints metrics. **Must match the Python backtest within tolerance** on the same window and cost model; document any residual difference.

**DoD:** `go test ./...` green including parity tests; `tt backtest` reproduces Python OOS Sharpe within ±0.05.

## Phase 3 — Live plumbing, dry-run

1. `internal/mcp`: minimal streamable-HTTP MCP client — OAuth (token cache on disk, refresh), `initialize`, `tools/list`, `tools/call`, retries with backoff, strict JSON decoding into per-tool structs generated from `docs/discovery/`. Tests against recorded fixtures; one opt-in integration test behind `-tags=live`.
2. `internal/marketdata/robinhood`, `internal/broker/robinhood`: thin adapters over `mcp.Client`. Broker adapter is written now but only exercised by integration tests until Phase 4.
3. `internal/sched`: market-calendar-aware daily loop (NYSE holidays; run at T-10 min before close, configurable). Idempotent per trading day via `run_id = date`.
4. `cmd/tt run --dry-run --spec ...`: each cycle — pull quotes via Robinhood MCP → append to bars in memory → `Strategy.Targets` → diff vs `PaperBroker.Positions` → intents → `RiskGate` → `PaperBroker.Place` → journal everything. Also fetch historical tail from the primary `HistoricalData` and refresh Parquet.
5. `cmd/tt report`: reads journal + paper SQLite; prints equity, DD, fills, slippage vs model, and **paper-vs-backtest divergence** for the elapsed window.
6. Deploy on the homelab (systemd unit, Tailscale-only access), run **2–4 weeks**.

**DoD:** dry-run has completed ≥ 10 trading days with zero `error` events unexplained in `docs/DISCOVERY.md`; `tt report` shows paper equity within the backtest's expected range for the same dates; every order in the journal has a matching allowed risk decision.

## Phase 4 — Live, small

1. Fund the Agentic account with a small amount (a few hundred dollars). Set `max_notional_per_leg_usd` accordingly; fractional shares are acceptable for pairs legs.
2. `tt run --live --spec ...` with `--yes` **off** for the first week (interactive confirm above threshold), then on.
3. Keep `tt run --dry-run` running in parallel on the same signals. `tt report` compares live fills to paper fills: slippage, fees, timing. This gap is the primary monitoring signal.
4. Scale only after ≥ 1 month of live results consistent with paper, and only up to half-Kelly.

**DoD:** one month live; documented slippage model updated from real fills; `risk.yaml` thresholds reviewed.

# 8. Testing and CI

- Python: `pytest`, `ruff`, `mypy --strict` on `tt/`. No network in unit tests; fixtures recorded.
- Go: `go test -race ./...`, `golangci-lint`, table-driven tests everywhere; `-tags=live` integration tests run manually only.
- Cross-language: `make golden` regenerates goldens (Python), `make parity` runs the Go parity tests. CI runs `parity` on every push.
- A `make backtest-compare` target runs both backtesters on the same window and diffs metrics.

# 9. Operational and safety notes

- Secrets (OAuth tokens) live in the OS keyring or a `0600` file outside the repo. Never in env files committed to git.
- The Agentic account is the only account the runtime can trade in by Robinhood's design; still, the runtime asserts the account ID it is trading matches the configured one and halts otherwise.
- `tt halt` writes a halt marker the scheduler honors; `tt resume` clears it. The drawdown kill switch writes the same marker.
- Notifications: Robinhood sends per-trade notifications; additionally, `journal` emits `halt` and `error` events to a webhook (ntfy/Slack) so problems are seen within minutes.
- Nothing here is investment advice. Expect the first strategy to fail its Sharpe floor; the plan's value is that it fails cheaply, in Phase 1, before any capital is at risk.

# 10. Open questions to resolve in Phase 0

1. Does the MCP provide historical bars? If yes, at what depth and are they adjusted?
2. Quote timestamp semantics and staleness during pre/post market.
3. Order types (market, limit, stop) and time-in-force supported through the MCP.
4. How fills are surfaced: polling order status vs. an events tool.
5. Rate limits and token lifetime; does refresh work headlessly on the homelab?
6. Fractional-share support for the pairs legs.

# Appendix A — Suggested first tasks for the Claude Code session

1. Create the repo skeleton from §3 with READMEs per component stating ownership and non-goals.
2. Write `specs/strategy.schema.json` from §4.2 and validate the example spec.
3. Implement `research/tt/data/yahoo.py` + `checks.py` with fixture-based tests; fetch GLD and GDX.
4. Implement `research/tt/stats/halflife.py` and `cointegration.py` with tests on synthetic AR(1) and cointegrated series.
5. Run Phase 0 discovery interactively and write `docs/DISCOVERY.md`.
