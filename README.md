<p align="center"><img src="art/bulls-and-bears-logo.png" width="320" alt="Bulls and Bears"></p>

# Bulls and Bears

**The agentic trading toolkit.** A personal algorithmic trading system, built
around Ernie Chan's loop: borrow a simple strategy, backtest it honestly, size
it at half-Kelly, paper-trade it against the backtest, then go live small. The
research lives in Python; the thing that runs unattended is a single Go
binary, `bnb`, that serves a phone-first web app so the whole operation can be
watched — and stopped — from a phone.

The plan it follows is [docs/PLAN.md](docs/PLAN.md). The LLM is a build tool
here and a research assistant; it is never in the trading decision path.

## Install

On the machine you want it to run on — a Raspberry Pi on your LAN or Tailscale
network is the intended home:

```sh
curl -fsSL https://raw.githubusercontent.com/chinmay28/bulls-and-bears/main/scripts/quickstart.sh | sudo bash
```

Then open `http://<that machine>:8877` on your phone and add it to the home
screen. Re-run the same command to upgrade: the build happens first, the paper
book is snapshotted, the new binary is health-checked after it starts, and a
failed upgrade rolls back to the previous binary and book.

```sh
# a PIN for the web UI, a different port, a specific version
curl -fsSL .../quickstart.sh | sudo BNB_PIN=1234 BNB_PORT=9000 BNB_REF=v2026.9.40 bash

# remove the service (the data directory is kept)
curl -fsSL .../quickstart.sh | sudo bash -s -- --uninstall
```

It runs dry. Orders fill on paper against the newest bar on disk, and nothing
reaches a broker until one is connected — and even then only a Robinhood
Agentic account funded with what you are prepared to lose.

## How it runs

Once a trading day, ten minutes before the NYSE close, the daily cycle runs:

1. The **specs** in `specs/` are loaded. A spec is a YAML file the research
   side wrote, carrying its strategy, parameters, sizing and provenance — the
   backtest window, the out-of-sample Sharpe, a TTL. The runtime refuses any
   spec without provenance, with a Sharpe under 1.0, past its TTL, or naming
   a strategy this binary does not have. Refused specs are listed on the
   phone with the reason.
2. The **bars** for every symbol are read from Parquet. Stale bars (more than
   three trading days old) or a symbol that mixes sources end the run before
   any order goes out.
3. **Quotes** are fetched and journalled. Until the Robinhood adapter exists,
   they come from the newest bar on disk.
4. Each armed strategy — a pure function of the bars — returns target
   weights. The difference between targets and the book's positions becomes
   intents.
5. Every intent goes through the **risk gate**: stale quote, per-leg notional,
   daily loss limit (closes still allowed), orders per day, gross leverage
   (openers scaled down, never dropped silently), and in live mode the
   confirmation threshold. The drawdown kill switch and the three-daily-hits
   rule halt the whole run and write the halt marker.
6. Allowed intents go to the **broker** — the paper book, or the real one —
   with the order line written to the journal the instant before, and the
   fills after.

Everything is written to an append-only JSONL **journal**, one file per run,
before the thing it describes is done. A checker enforces the invariant the
whole system rests on: no order is ever submitted without an allowed risk
decision carrying the same intent id. The phone shows every run, event by
event, and says so.

## The halt switch

There is one way to stop trading and one way to start it again. A halt is a
marker file in the data directory; the scheduler checks for it before every
run and does nothing while it is there. The drawdown kill switch, `bnb halt`
on the command line and the **Halt trading** button on the phone all write the
same file, with why and when. Nothing resumes on its own: **Resume trading**
on the phone or `bnb resume` removes it.

```sh
sudo -u bnb /opt/bnb/bnb halt -data /var/lib/bnb going away for a week
sudo -u bnb /opt/bnb/bnb status -data /var/lib/bnb
sudo -u bnb /opt/bnb/bnb resume -data /var/lib/bnb
```

An upgrade keeps the marker. A halt marker that cannot be read still halts.

## The two halves agree

The signal is implemented twice — Python for research, Go for the runtime —
and so are the metrics and the backtest engine. [docs/CONTRACTS.md](docs/CONTRACTS.md)
defines each once, in prose, and `golden/` holds the research side's output:
bars, every expected signal, the equity curve, the metrics. The Go parity
tests hold the runtime to them: every signal within `1e-9`, every equity
point and metric within `1e-6`. `make golden` regenerates them; `make parity`
checks; `make backtest-compare` prints both engines' numbers side by side.

The goldens committed today come from a seeded synthetic pair, because the
machine that built this tree could not reach Yahoo. Run
`research/scripts/gld_gdx.py` once with network and commit what it writes.

## The phone

Five tabs, in HostMan's shape:

- **Overview** — equity, today's change, and the three guardrails as meters
  against their limits; today's run and the next; every spec, armed or
  refused; **Halt trading**.
- **Strategies** — each spec with its provenance, how long its TTL has left,
  and its signal now: the z-score against the entry and exit bands. **Run
  backtest** replays it over the bars on the machine and says whether the
  number matches the spec's. **Import a spec** takes the YAML research wrote
  and installs it, if the runtime would run it; a spec's own screen removes
  it again.
- **Research** — the studies under `research/scripts`, run on the machine
  from the phone: the ETF/GLD ratio reversion, Faber's moving-average
  trend, Antonacci's dual momentum, and Chan's pairs trade, each on the
  symbols typed into its card. **Set up the environment** finds or
  downloads `uv` and installs the Python side into the data directory;
  **Run study** fetches from Yahoo, sweeps on the training window, judges
  once on at least a year out of sample and installs the spec only if it
  clears the floor — the same gate the script applies from a terminal. The
  log streams to the phone as it runs; a job can be cancelled; every log is
  kept.
- **Book** — equity over time, positions marked to market, working orders,
  fills with the price they got.
- **Settings** — version, the halt state, the bars per symbol and whether
  they are stale with **Refresh bars from Yahoo**, recent runs, the broker
  connection.

A run that trades nothing says why on its own screen: every spec it saw is
journalled with its reason, and the two failures an operator can fix — no
armed spec, and bars too old to trade on — link to the screen that fixes
them.

## Commands

| Command | What |
| --- | --- |
| `bnb` | serve the API and the app; run the daily cycle before each close |
| `bnb run [-run-id DATE]` | run one cycle now and exit; the same thing the phone's **Run now** does |
| `bnb backtest -spec FILE -bars DIR [-from -to -csv]` | replay a spec over bars and print the numbers |
| `bnb halt [reason]` / `bnb resume` / `bnb status` | the halt marker |
| `bnb login` | OAuth against the Robinhood MCP from a desktop browser; writes the token file |
| `bnb discover [-out FILE]` | `initialize` and `tools/list` against the MCP (Phase 0) |
| `bnb version` | the build |

Server flags:

| Flag | Env | Default | Meaning |
| --- | --- | --- | --- |
| `-addr` | `BNB_ADDR` | `:8877` | listen address |
| `-data` | `BNB_DATA` | `data` | halt marker, journal, paper book, token |
| `-specs` | `BNB_SPECS` | `specs` | strategy specs |
| `-research` | `BNB_RESEARCH` | `research/` beside `-specs` | the research tree the Research tab runs studies from |
| `-bars` | `BNB_BARS` | `<data>/bars` | daily bars, `<SYMBOL>.parquet` |
| `-risk` | `BNB_RISK` | _(defaults)_ | `risk.yaml`; see `risk.example.yaml` |
| `-pin` | `BNB_PIN` | _(empty)_ | optional PIN; empty = open |
| `-starting-cash` | | `10000` | what the paper book opens with, first time only |
| `-run-offset` | | `10m` | how long before the close the run fires |
| `-allow-mixed-sources` | | `false` | accept bars that mix sources |
| `-self-ref` | `BNB_REF` | `main` | git ref a self-update builds from |
| `-v` | | `false` | verbose logging |

## API

| Method | Path | What |
| --- | --- | --- |
| GET | `/api/health` | `{status, version}`; public |
| GET / POST | `/api/session` | PIN session |
| GET | `/api/self` | version, mode, halt state |
| GET | `/api/overview` | the dashboard in one round trip |
| GET / POST / DELETE | `/api/halt` | read, write, remove the halt marker |
| GET | `/api/strategies`, `/api/strategies/{name}` | specs with status, provenance, signal |
| POST | `/api/strategies` | import a spec: same schema, Sharpe floor and TTL a run applies |
| DELETE | `/api/strategies/{name}` | remove a spec; its bars stay |
| POST | `/api/strategies/{name}/backtest` | replay over the bars on disk |
| GET | `/api/bars` | bars per symbol and staleness |
| POST | `/api/bars/refresh` | refill bars from Yahoo; never shortens a series |
| GET | `/api/book` | the paper book: positions, orders, fills, history |
| GET | `/api/research` | the research environment, the studies, the current and recent jobs |
| POST | `/api/research/setup` | find or install `uv` and sync the Python environment |
| POST | `/api/research/runs` | run a study (`{"study", "trainTo"}`), output pointed at this machine's specs and bars |
| GET | `/api/research/jobs/{id}?from=N` | a job and its log from a byte offset |
| DELETE | `/api/research/jobs/{id}` | cancel a running job |
| GET | `/api/runs`, `/api/runs/{id}`, `/api/runs/{id}/jsonl` | journals |
| POST | `/api/run` | today's cycle, now |

## Development

```sh
make build          # PWA into the Go embed directory, then the binary
make run            # build and start on :8877 with data in ./data
make test           # Go tests, with the race detector
make test-web       # the PWA's unit tests
make test-research  # pytest, ruff and mypy on the Python side (needs uv)
make test-icongen   # the icon cutter's tests
make test-installer # install, upgrade, rollback and uninstall, in a sandbox (root)
make golden         # regenerate the parity fixtures (GOLDEN_FLAGS=--synthetic offline)
make parity         # the Go tests that read them
make backtest-compare

cd apps/web && npm run dev   # Vite dev server, proxying /api to :8877

make version        # print the version this tree would build as
make bump-version   # move the version line to this month, UTC
make icons          # cut the app icons from art/bulls-and-bears-logo.png
```

### Versioning

The version is `vYEAR.MONTH.PATCH` — a calendar version, where the patch number
is the repository's commit count, so `v2026.9.42` is the 42nd commit on the
2026.9 line. There is no semantic major/minor: the leading numbers say *when* a
release line opened, not what it promises about compatibility.

`Year` and `Month` are constants in `server/internal/version/version.go`. A
line opens with a branch: the first commit on a branch runs `make bump-version`,
which sets them to the current month in UTC (CLAUDE.md asks Claude to do this
without being told). They are deliberately not read from the build clock, so
rebuilding an old tree still reports what it originally shipped.

The patch number only exists at build time, so `scripts/version.mjs` works it
out once and both halves of the build take it from there: the linker stamps it
into the binary, Vite inlines it into the bundle. The app header shows what the
PWA was built as, Settings and `/api/health` show what the binary was, and they
agree because a build makes both together. A version ending in `.0` is an
unstamped build rather than a release, which includes a build from a shallow
clone — the installer clones with `--filter=blob:none` for that reason.

## Layout

```
server/
  cmd/bnb/              entrypoint: serve, run, backtest, login, discover, halt
  internal/bars/        the Parquet bar and its invariants; refill/ brings a directory up to date
  internal/spec/        spec loading, schema validation, TTL and Sharpe refusals
  internal/strategy/    the Strategy interface and registry; pairs/ is pairs_zscore
  internal/backtest/    the replay engine that matches the Python one
  internal/metrics/     Sharpe, max drawdown, drawdown duration
  internal/marketdata/  Quote and the data interfaces; replay/ serves quotes from bars,
                        yahoo/ fetches daily history on demand
  internal/broker/      the Broker interface; paper/ is the SQLite paper book
  internal/risk/        risk.yaml, the rule gate, the kill switch
  internal/journal/     append-only JSONL, and the invariant checker
  internal/runner/      the daily cycle
  internal/sched/       the NYSE calendar and the daily loop
  internal/halt/        the halt marker
  internal/mcp/         the streamable-HTTP MCP client; oauth/ gets and keeps the token
  internal/api/         REST handlers, optional PIN gate
  internal/version/     the version number, and where YEAR.MONTH is declared
  internal/web/         serves the embedded PWA
apps/web/               the PWA: React, Vite, no UI framework
research/               the Python side: data, statistics, backtests, specs, goldens
specs/                  strategy specs and their JSON schema
golden/                 the cross-language parity fixtures
docs/                   the plan, the contracts, discovery findings, the strategy template
scripts/                the installer, its test harness, and version.mjs
art/                    the logo; tools/icongen cuts the app icons from it
```
