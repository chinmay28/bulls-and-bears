# Discovery

Phase 0 findings about the Robinhood Trading MCP, and every place the plan and
reality disagreed. Nothing here is verified yet: the questions below are open
until someone has completed the desktop OAuth flow and called `tools/list`.

## Status

Not started. `tools/list` has not been called; `docs/discovery/` is empty.

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
