# Discovery

Phase 0 findings about the Robinhood Trading MCP, and every place the plan and
reality disagreed. Nothing here is verified yet: the questions below are open
until someone has completed the desktop OAuth flow and called `tools/list`.

## Status

Not started: `tools/list` has not been called and `docs/discovery/` is
empty. The tooling for it exists. On a machine with a browser:

```sh
bnb login -data ~/bnb-data                # OAuth in the browser; writes robinhood-token.json
bnb discover -data ~/bnb-data -out docs/discovery/tools_list.json
```

`login` follows the MCP authorization flow (the 401 challenge, the protected
resource metadata, the authorization server's metadata, dynamic client
registration, PKCE, a localhost redirect) and writes the token, 0600, into
the data directory. `discover` initializes against the server with it and
prints `tools/list`. Copy the token file to the trading machine's data
directory afterwards; the refresh is headless.

Once the JSON is in, fill the questions below in and record the sample
request/response pairs under `docs/discovery/samples/`.

## Open questions (from `docs/PLAN.md` §10)

1. Does the MCP provide historical bars? At what depth, and are they adjusted?
2. Quote timestamp semantics, and staleness during pre- and post-market.
3. Order types (market, limit, stop) and time-in-force supported.
4. How fills are surfaced: polling order status, or an events tool.
5. Rate limits and token lifetime; does refresh work headlessly on the homelab?
6. Fractional-share support for the pairs legs.

## Decisions

- **Historical bars: Yahoo via `yfinance` until (1) is answered.** The Go
  runtime reads Parquet the research side writes; nothing in the runtime
  depends on the MCP having history.

## Discrepancies with the plan

The plan describes a `tt` command-line toolkit. This repository builds the same
runtime as `bnb`, a single binary that also serves a phone-first web app, the
way HostMan does: the daemon, the paper broker, the risk gate and the journal
are the plan's; the app is how they are watched and stopped from a phone.
`tt backtest`, `tt run`, `tt report` become `bnb` subcommands and API routes as
the phases land.

The plan's §4.1 says of the bar files: **Python writes; Go reads.** The Go
runtime now also writes them, from `internal/marketdata/yahoo` through
`internal/bars/refill`, when the operator asks for a refresh from the app.
The reason is the one §0 gives for adapting a component: the phone is how
this system is actually operated, and a run that halts on stale bars was
otherwise only fixable at a terminal, which is exactly when the operator does
not have one. The direction of the contract is unchanged — both halves write
the same §4.1 schema, stamp `source: yahoo`, and read each other's files; the
Go side's parity is held by `internal/bars` round-trip tests.

Three things keep the second writer from being a second source of truth:

- The fetch is **on demand only**. Nothing in the daily cycle reaches the
  network for history; the scheduler still runs on whatever is on disk, and
  refuses stale bars exactly as before.
- A refill **never shortens a series**. A symbol whose fetch fails, or whose
  answer holds fewer bars than the file already has, is left untouched and
  reported. Research remains the way to *deepen* history; the app's refresh
  is for the tail.
- The **current day is dropped**. Yahoo serves a partial bar while the
  session is open, and the runner already builds today from quotes.

Specs may likewise be imported from the app (`POST /api/strategies`). This is
not a way around provenance: an upload goes through `spec.Check`, the same
schema, the same 1.0 out-of-sample Sharpe floor and the same TTL a run
applies, and a spec that would be refused is not written at all. Research
still has to have earned it — the file just no longer has to arrive by scp.

## Research data when Yahoo is unreachable

The ETF/GLD ratio study (`research/scripts/etf_gld_ratio.py`,
`docs/strategies/etf_gld_ratio.md`) was first run from a sandbox whose egress
policy refused every quote host — Yahoo, Stooq, FRED, Alpha Vantage, Tiingo,
Nasdaq, EODHD — and allowed anonymous clones of public GitHub repositories.
The plan's data fallback (§2) therefore had a fallback of its own: a GitHub
mirror of Kaggle's "Huge Stock Market Dataset"
(`masaok/kaggle-boris-huge-stock-market-dataset`), whose `ETFs/*.us.txt`
files carry split- and dividend-adjusted daily bars for GLD, SPY, QQQ, VTI
and XLK from 2005-02-25 to 2017-11-10. The script reads them with
`--csv-dir`, stamps `source: kaggle`, and never promotes a spec from them:
promotion needs Yahoo data, which `make golden-ratio` fetches on a machine
that has it.

What this cost: the study's out-of-sample window ends in November 2017, so
it says nothing about 2018–2026, the years in which gold's outperformance of
equities was strongest. The verdict it reaches (the contrarian ratio rule
does not clear the Sharpe floor) is over 2005–2017 only and is recorded as
such in the strategy document and in the rejected spec's provenance.
