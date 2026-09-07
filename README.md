<p align="center"><img src="art/bulls-and-bears-logo.png" width="320" alt="Bulls and Bears"></p>

# Bulls and Bears

**Small bets, honest books.** A personal algorithmic trading toolkit, built
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

It runs dry. Nothing is placed anywhere until a broker is connected from
Settings, and even then only in a Robinhood Agentic account funded with what
you are prepared to lose.

## The halt switch

There is one way to stop trading and one way to start it again. A halt is a
marker file in the data directory; the scheduler checks for it before every
run and does nothing while it is there. The drawdown kill switch in the risk
gate, `bnb halt` on the command line and the **Halt trading** button on the
phone all write the same file, with why and when. Nothing resumes on its own:
**Resume trading** on the phone or `bnb resume` removes it.

```sh
sudo -u bnb /opt/bnb/bnb halt -data /var/lib/bnb going away for a week
sudo -u bnb /opt/bnb/bnb status -data /var/lib/bnb
sudo -u bnb /opt/bnb/bnb resume -data /var/lib/bnb
```

An upgrade keeps the marker. A halt marker that cannot be read still halts.

## What is built so far

The shell: the binary, the web app, versioning, the installer and the halt
switch. The phases in the plan land in order from here, and each one is in
`docs/PLAN.md` with its definition of done:

- **Phase 0**, discovery of the Robinhood Trading MCP — `docs/DISCOVERY.md`.
- **Phase 1**, the Python research side — `research/`.
- **Phase 2**, the offline Go core: bars, specs, the strategy and its parity
  test, metrics, the paper broker, the risk gate, the journal, `bnb backtest`.
- **Phase 3**, live plumbing in dry-run: the MCP client, the scheduler,
  `bnb run --dry-run`, `bnb report`.
- **Phase 4**, live, small.

## Development

```sh
make build          # PWA into the Go embed directory, then the binary
make run            # build and start on :8877 with data in ./data
make test           # Go tests, with the race detector
make test-web       # the PWA's unit tests
make test-icongen   # the icon cutter's tests
make test-installer # install, upgrade, rollback and uninstall, in a sandbox (root)

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

Server flags:

| Flag        | Env        | Default  | Meaning                                              |
| ----------- | ---------- | -------- | ---------------------------------------------------- |
| `-addr`     | `BNB_ADDR` | `:8877`  | listen address                                       |
| `-data`     | `BNB_DATA` | `data`   | halt marker, journal, paper book, broker token       |
| `-pin`      | `BNB_PIN`  | _(empty)_ | optional PIN; empty = open                          |
| `-self-ref` | `BNB_REF`  | `main`   | git ref a self-update builds from                    |
| `-v`        |            | `false`  | verbose logging                                      |

Subcommands: `bnb halt [reason]`, `bnb resume`, `bnb status`, `bnb version`.
All take `-data`.

## API

| Method | Path            | What                                            |
| ------ | --------------- | ----------------------------------------------- |
| GET    | `/api/health`   | `{status, version}`; public, polled by the installer |
| GET    | `/api/session`  | whether a PIN is required and whether this browser has one |
| POST   | `/api/session`  | `{pin}` → session cookie                        |
| GET    | `/api/self`     | version, ref, mode, halt state                  |
| GET    | `/api/overview` | the dashboard in one round trip                 |
| GET    | `/api/halt`     | the halt marker, if any                         |
| POST   | `/api/halt`     | `{reason?}` writes it                           |
| DELETE | `/api/halt`     | removes it                                      |

## Layout

```
server/
  cmd/bnb/           entrypoint: flags, subcommands, graceful shutdown
  internal/halt/     the halt marker: the one switch that stops trading
  internal/api/      REST handlers, optional PIN gate
  internal/version/  the version number, and where YEAR.MONTH is declared
  internal/web/      serves the embedded PWA
apps/web/            the PWA: React, Vite, no UI framework
docs/                the plan, discovery findings, the strategy template
specs/               strategy specs and their JSON schema
scripts/             the installer, its test harness, and version.mjs
art/                 the logo; tools/icongen cuts the app icons from it
tools/icongen/       the icon cutter (its own Go module)
```
