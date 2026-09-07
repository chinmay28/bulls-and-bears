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
