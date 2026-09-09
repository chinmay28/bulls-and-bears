# Discovery

Phase 0 findings about the Robinhood Trading MCP, and every place the plan and
reality disagreed. The §10 questions below are answered as far as read-only
calls against the live server can answer them ("The Robinhood MCP, read off
the live server"); what is left needs the desktop OAuth flow and a
`tools/list` from `bnb` itself.

## Status

Partly answered, and not by `bnb`. The questions below were answered on
2026-09-09 from a Claude Code session with the Robinhood MCP attached
directly, by calling the read-only tools against the live account. That is
not the same as `bnb discover`: it says what the *server* exposes, not that
the Go client can reach it. `docs/discovery/tools_list.json` is still empty
and the OAuth flow below is still the way to fill it.

What was learned, and what it costs the XLK/SATA rotation, is in "The
agentic account, as it stands" below. The short version: the account this
agent may trade holds $394.98, all of it crypto, with $0.00 buying power and
options not enabled, so that strategy cannot place a single one of its
orders today.

Still not started for the Go client: `tools/list` has not been called from
`bnb` and `docs/discovery/` is empty. The tooling for it exists. On a machine with a browser:

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

Answered below in "The Robinhood MCP, read off the live server", except
where marked.

1. Does the MCP provide historical bars? At what depth, and are they adjusted?
2. Quote timestamp semantics, and staleness during pre- and post-market.
3. Order types (market, limit, stop) and time-in-force supported.
4. How fills are surfaced: polling order status, or an events tool.
5. Rate limits and token lifetime; does refresh work headlessly on the
   homelab? — **still open**, and not answerable from a read-only session.
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

## The Robinhood MCP, read off the live server

Answers to §10 as far as read-only calls can give them, 2026-09-09.

**Accounts.** `get_accounts` returns every account with an `agentic_allowed`
flag. Exactly one is true — the account the agent may trade — and it is not
the default account. The runtime's §9 assertion (trade only the configured
account, halt otherwise) has a field to check it against.

**Historical bars: still no.** Nothing in the tool list returns daily OHLCV
for an equity. `get_equity_historicals` exists but was not exercised; the
§2 decision stands unchanged — Yahoo is the history, the MCP is the quote.

**Quotes.** `get_equity_quotes` gives `bid_price`, `ask_price`,
`last_trade_price`, a separate `last_non_reg_trade_price` for extended
hours, and per-field venue timestamps, plus the official prior-session close
with its own `source`. The timestamps are per field, so the staleness rule
in §6 has something real to measure. Outside regular hours the regular-hours
last trade goes stale while bid/ask keep updating, which is exactly the case
the rule exists for.

**Options: available, and richer than assumed.** §2 guessed equities only.
In fact `get_option_chains` gives expirations and the chain's `min_ticks`
(0.01 below a 3.00 cutoff, 0.05 at and above — a limit price off the tick is
rejected), `get_option_instruments` gives per-contract ids, strikes and
`tradability`, and **`get_option_quotes` carries `delta`, gamma, theta, vega,
implied volatility, open interest and volume** alongside bid/ask/mark. A
delta-targeted rule can therefore be written without a pricing model of our
own. Chains also publish `sellout_time_to_expiration` (1800s on XLK): the
broker force-closes a short contract near expiry, which is an exit the
runtime does not control and must expect.

**Order types.** Equities: market, limit, stop_market, stop_limit; `gfd` or
`gtc`; a `market_hours` selector for regular, extended and overnight
sessions, where only limit orders execute outside regular hours. Options:
limit, market, stop_limit, stop_market, single-leg at option level 2 and
multi-leg at level 3. Both take a `ref_id` idempotency key, which is what
`internal/mcp`'s "never retry an order" rule can be relaxed against later.

**Fills.** Polled, not pushed: `get_equity_orders` and `get_option_orders`
filter by state (`filled`, `partially_filled`, `rejected`, `cancelled`, …)
and by `created_at_gte`, and carry a `placed_agent` field that separates the
agent's own orders from the operator's. There is no events tool, so the
runner polls.

**Review before place.** `review_equity_order` and `review_option_order`
simulate an order and return pre-trade alerts (buying power, PDT, halts).
They are the natural place to put the §6 confirmation threshold.

**Not answered:** rate limits, token lifetime, and whether refresh works
headlessly — none of which a read-only session can establish. Fractional
shares are documented as market-order-and-regular-hours only.

## The agentic account, as it stands

The only `agentic_allowed` account is a limited-margin individual account
holding **$394.98, all of it crypto**: `equity_value` 0, `options_value` 0,
`cash` 0, `buying_power` **0.00**, no equity positions, no option positions.
Its `option_level` is empty.

Three consequences for `xlk_sata_rotation`, all of them hard:

1. **It cannot buy the lot.** 100 XLK is about $18,800 against $0.00 of
   buying power.
2. **It cannot write the covered call.** `place_option_order` rejects an
   account whose `option_level` is empty; the whole recovery leg is
   unreachable until options are enabled on *that* account. Three other
   accounts have option level 2 or 3, and none of them is tradable by this
   agent.
3. **It cannot park in SATA**, having no cash to park.

The other accounts are readable and not tradable, by Robinhood's design,
so none of this is worked around in code. Funding and options approval are
operator actions taken in the Robinhood app, and until they happen the
strategy is a dry-run strategy whatever mode it is asked to run in.

Noted for when it is funded: the account is `limited_margin`, and the rules
say not to use margin. The engine enforces that itself — every buy is capped
at settled cash — rather than trusting the account type.

## A strategy the weights interface cannot express

`xlk_sata_rotation` (docs/strategies/xlk_sata_rotation.md) is the first
strategy that does not fit `strategy.Strategy`, and the plan's §0 says to
record the discrepancy and adapt the component rather than the architecture.

`Targets(hist, pos) -> map[symbol]weight` is a function of daily bars
answering in fractions of equity. This strategy needs to say "exactly 100
shares, never 99"; it trades an instrument that is not in any bar file; it
decides at two intraday clock times rather than once against a bar; and its
decision depends on state no bar carries — the entry price of the open lot,
the premium already collected against it, and whether the midday review has
been passed. None of that survives a weight vector.

So `internal/strategy/xlksata` keeps the principle and drops the signature.
`Decide(Snapshot) -> Plan` is still pure — no I/O, no clock, no randomness,
same function in a test, a dry run and live — and the impurity is pushed to
the edges the way it already is elsewhere: the caller reads the book, and
the `Apply*` functions fold fills back into state. It is not in the
`strategy` registry, because a spec naming it would be a spec the runner
would try to ask for weights.

What this costs: no parity test, because there is no Python implementation
and no golden file; and no backtest, because `internal/backtest` replays
daily bars through `Targets`. Neither is written off — §4.3 applies the day
a study exists — but both are absent today, which is the largest reason the
strategy is not armed.

## What is not built yet for the rotation

The engine decides; `internal/robinhood` and `internal/rotation` now carry
its decisions to the account. `rotation.Cycle` is one turn: settle whatever
the last cycle placed, read the whole book into a snapshot, decide, write the
state, place at most one order. Its ordering is the part worth knowing —
settlement runs first because a fill is the only thing that can set the lot's
entry price, and the state is written *before* the order goes out, so a crash
between the two leaves a book that says an order may be outstanding rather
than one that has forgotten an order already at the exchange.

Two consequences of the wire format are worth recording because both would be
silent bugs. An option order's `average_price` is per *contract*, so a 0.85
credit comes back as 85 and is divided by the multiplier before it reaches
the strategy. And an option *position* carries an id but no strike, so every
open contract costs an extra `get_option_instruments` lookup to price the
assignment test against.

`rotation.Loop` fires it, and `bnb rotate` is the command. The loop is
separate from `sched.Loop` rather than a second fire time on it because the
two answer different questions: that one asks "when does today close?" and
runs once against it, this one asks "what is the market doing now?" several
times a day. Both read the same calendar, so a holiday and an early close are
honoured identically — and the close is exclusive, since 13:00:00 on a half
day is already shut. The phases are 07:12 and 12:07 Pacific, held in Eastern
because both US zones change over on the same dates and the session bounds
are already in Eastern.

`bnb rotate` defaults to reviewing every order and placing none; `-live` is
what places them, and `-account` has no default, because a command that
places real orders should not guess which account in.

`rotation.Gate` applies §6 and `rotation.Cycler` journals every cycle, so
`journal.Check`'s invariant — an `order_submitted` only ever after an allowed
`risk_decision` for the same intent — holds on this path and is tested
against a real cycle. The intent id is the same string in three places: the
gate's decision, the journal line, and the broker's `ref_id`.

It is a second gate rather than a use of `risk.RuleGate`, because that one
prices intents through `broker.Order` — a symbol, a side and a share count —
which cannot express a contract. Pushing an option leg through it would not
fail, it would produce a confident wrong number, valuing a covered call as
if it were a hundred shares of premium. So the equity-level rules, which are
arithmetic on the account and mean the same thing here, are delegated to
`risk.RuleGate` unchanged, and only the per-intent rules are re-stated. Both
read the same `risk.Config`, so the thresholds still live in one `risk.yaml`.

Two judgements are recorded in that gate rather than left implicit:

- The daily loss limit stops opening orders and allows closes. **A written
  call counts as risk-reducing**, despite carrying the position effect
  "open": it collects premium against shares already held, and it is the
  only way a recovery makes progress. Blocking it would trap a losing lot
  with no route out, which is the opposite of what the rule is for.
- **A sweep into SATA counts as opening**, so it is refused after a limit
  day and the proceeds sit in cash until the next session. That is the
  literal rule, and idle cash is the safe side of it.

The gate's day-scoped counters live in the book (`rotation.Marks`), because a
daemon that forgot how many orders it had sent today, or where the equity
high-water mark was, would hand the gate a clean slate on every restart —
which is exactly when the gate matters most.

### The book is an append-only ledger

`<data>/rotation.jsonl` holds one timestamped JSON object per line, appended
and never rewritten, the way `internal/journal` keeps a run. `bnb rotate
-history` prints it.

A line is a whole snapshot of the book rather than an event to fold, and the
reason is that the *why* of every change is already in the journal — the
quote, the decision, the order. An event log here would say the same things
twice and move the burden of correctness into a replay. What this file has to
survive is a crash, and a snapshot per line survives it in the way that
matters: the final line may be torn, and the line before it is a complete and
consistent book, one cycle stale. A fold over a torn event log loses the same
cycle with more machinery. So `Load` reads backwards and takes the last whole
line; a torn final line is skipped, and a line that parses but does not make
sense is an error, never a flat book, because forgetting an open lot is how a
second one gets opened on top of it.

Because nothing is rewritten, the file is also the history: every state the
book has ever been in, timestamped. That is the answer to "how did this lot
get here", which a mutable snapshot could not give.

### One rotation per data directory

`rotation.Acquire` takes an advisory `flock` on `<data>/rotation.lock` for as
long as a process may act, and `bnb rotate` holds it. This is not
housekeeping. The book is the only thing stopping a second lot being opened
on top of the first, and it works only if one process at a time reads it,
decides, and appends. Two rotations over one directory — a daemon and a
scheduled run, or two copies of the daemon after a botched restart — would
each read a flat book, each decide to buy, and each buy, because neither's
order is visible to the other until it has already gone out. The lock is held
by the open descriptor, so a crash leaves nothing stale to clear.

This is what makes a scheduled run safe to put beside a daemon: the second
one to start refuses, loudly, naming the process that has it.

### Running it from a schedule instead of a daemon

`bnb rotate -cron` prints a schedule. Two things had to be solved before a
scheduled firing was as good as a running daemon.

**Cron is UTC and does not follow US daylight saving**, so no fixed
expression holds 07:12 and 12:07 Pacific in both halves of the year: 14:12
UTC is 07:12 PDT but 06:12 PST, eighteen minutes before the opening bell.
What can be held is the *shape* of the rules — a morning entry and a review
four hours and fifty-five minutes later, both inside the session under either
offset. The window for keeping the full hold in session year-round is only
14:30 to 15:05 UTC; **14:45 and 19:40 UTC** sit in the middle of it:

| | PDT (≈Mar–Nov) | PST (≈Nov–Mar) |
|---|---|---|
| 14:45 UTC entry | 07:45 PT, open + 75m | 06:45 PT, open + 15m |
| 19:40 UTC review | 12:40 PT, close − 20m | 11:40 PT, close − 80m |

So the wall-clock time drifts an hour with the seasons and the hold does not.
A holiday firing exits without trading, because the calendar is checked at
run time rather than encoded in the cron. On the three or so early-close days
a year the review falls after the 13:00 ET close and is skipped; the lot
stays in `held` and is reviewed the next session.

**A single cycle cannot finish a sequence.** The strategy's steps come in
pairs on purpose — sell SATA, then buy XLK once the cash is there; buy the
call back, then sell the shares out from under it — so a firing that ran one
cycle would leave the second half until the next one. `-once` therefore runs
up to `-max-cycles` cycles (default 4) in one invocation, waiting `-settle`
between them for a fill to land and stopping as soon as a cycle has nothing
left to do. The first cycle runs the named phase and the rest run as
management, which is what carries a sequence to its end. That costs a few
seconds and makes an hourly management cron perfectly adequate, which is the
tightest cron the schedule allows anyway.

One guard came out of this. A named `-phase` used to skip the market-hours
check, so `-once -phase entry` would happily run at midnight; an equity order
placed into a shut market queues for the next open, where the quote it was
decided on is hours stale. `Times.InSession` is now checked whatever the
phase, and `-force` is the only way past it.

One consequence worth knowing before a live run: a 100-share XLK lot is about
$18,800, far over §6's $500 confirmation threshold, so `bnb rotate -live`
**refuses every entry** unless it is also given `-yes`. The command warns
about this at startup rather than at 07:12.

Still missing:

- **No `broker.Broker` implementation.** The rotation deliberately does not
  go through that interface: `Order` has no contract id, no `position_effect`
  and no multiplier, and widening it for one strategy would push options into
  every other strategy's path. `internal/rotation` talks to
  `internal/robinhood` directly, and the paper broker is therefore not
  available to it — which is why the honest dry run here is
  `Executor.DryRun`, reviewing every order against the live account and
  placing none.

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

## Research from the app

The plan (§1, §3) keeps research in Python on a workstation and the runtime
in Go on the homelab, joined by the spec files research emits. That is still
the division of labour; what changed is where the Python runs. The Research
tab (`server/internal/research`, `POST /api/research/runs`) runs the studies
under `research/scripts` on the trading machine itself: `uv sync --frozen`
against the checkout the installer already keeps at `/opt/bnb/src`, then the
script, with `--specs-dir`, `--bars-dir` and `--out-dir` pointed at the
runtime's own directories. The reason is the one every other adaptation here
gives: the phone is how this system is operated, and "run the study again
now that Yahoo has this month's bars" was otherwise a laptop job.

What keeps this inside the plan's principles:

- **The gate is the script's.** A study promotes a spec only when its own
  out-of-sample Sharpe clears 1.0 on real data, exactly as from a terminal;
  the runtime then judges the file as it judges any other. The app cannot
  arm a spec the research would have rejected.
- **Nothing in the trading path changes.** The runner is a subprocess with a
  log; the daily cycle does not know it exists. Research is one job at a
  time, so two syncs of one environment cannot race and a study cannot pile
  onto a trading run.
- **The service stays read-only outside its data directory.** uv's
  environment, cache and any Python it has to download, the rejected specs,
  the goldens and the logs all live under `<data>/research`; the research
  tree is only read (`PYTHONDONTWRITEBYTECODE` keeps Python from trying).
  Specs moved into the data directory for the same reason (below).
- **uv is installed, not assumed.** The installer puts `uv` on the machine;
  where it could not, the runner downloads the release archive for its
  platform, checks it against the published sha256, and keeps it in
  `<data>/research/bin`. Trust in the publisher is the same the installer
  already extends to Go and Node.

### Specs live in the data directory

The installer used to point `-specs` into the checkout, which the unit's
`ProtectSystem=strict` makes read-only: a spec imported from the phone had
nowhere to land. `-specs` is now `<data>/specs`, seeded from the checkout's
`specs/*.yaml` on install and upgrade without overwriting what is already
there — the operator's imports and the specs studies promote survive an
upgrade, and the checkout's specs still arrive.

## Re-validation on a schedule, and the paper-to-live stage

Asked whether the studies should run every day and arm or disarm specs on
their own, the answer was mostly no, and the shape that was built follows
from why. A study picks parameters on a training window and takes one look
at the test window; run daily on a moving window, that one look becomes
thousands of overlapping looks and the armed spec is whichever day's draw
cleared 1.0 — the data-snooping the plan's §0 loop exists to avoid. The
floor is also a noisy line: a year out of sample puts about ±1 on a Sharpe,
so a rule at 1.2 crosses 1.0 on many days for no reason, and every crossing
would liquidate a book at five basis points a side, at the bottom of a
drawdown by construction.

So `internal/research.Schedule` re-runs every installed spec **once a
month, outside market hours, with the training cutoff it was produced
with**. Parameters cannot drift, the out-of-sample window only grows, and the
question each run asks is the honest one. A run that promotes rewrites the
spec with a fresh `generated_at`; a run that does not leaves the spec alone
and it expires at its TTL, the runtime refuses it, and the runner's targets
for it go to zero. That expiry is the only automatic disarm. The fast ones —
the drawdown kill switch and the live-versus-paper divergence in §6 — stay
in the risk gate, acting on the money rather than on a backtest.

Arming stays automatic only on paper. `internal/stage` records which specs
the operator has promoted to live, in the data directory rather than in the
spec, so research cannot make that decision and a re-validation cannot
unmake it. In live mode the runner arms only promoted specs; in dry-run the
stage changes nothing, because paper is what the book is. That is the
plan's Phase 4 gate, a month of paper agreeing with the backtest, made
explicit per spec.

Not done: the §9 webhook for promotions and expiries. The re-validation
history is on the Research tab and in `<data>/research/revalidate.json`,
and an expiry shows on the next run's journal as a refused spec.

## Execution timing

The strategy expansion plan asks every new strategy to be researched under
`fill_at: next_open` — the targets read at a bar's close fill at the next
bar's adjusted open — because a backtest that fills at the close it has just
read overstates a short-horizon rule. Both engines now implement that fill
(docs/CONTRACTS.md, Execution) and a spec's `execution` block says which it
was researched with; a spec without the block is `same_close_legacy`.

What the runtime actually does is neither, exactly. The scheduler fires once
a day ten minutes before the close; the runner appends the last quote as the
day's bar, asks the strategy for targets on it, and trades at that quote. That
is `same_close_legacy` with a ten-minute gap on both the signal and the fill,
which the slippage model is there to cover. A `next_open` spec needs a run
the runtime does not have: at the open, targets from bars through the
previous close (no quote bar appended), orders at the opening quote.

Until that open-session run exists, the close-time run **does not arm** a
`next_open` spec and journals why (`fills at next_open: this run fills at the
close; not armed`). This keeps the paper book honest — it never trades a
model a spec was not backtested on — at the cost that the new strategies are
backtest- and research-only until the open session is built. That is the
next runtime task: `sched.Session` gains an open, `sched.Loop` a second fire
time, the runner a phase that skips the quote bar and arms only the specs
whose fill matches, and run ids distinguish the two sessions.

## The strategy expansion, and the data it was first run on

The expansion plan's first wave — `time_series_momentum`,
`donchian_breakout`, `risk_parity_trend`, `rsi2_reversion` and the
`sector_rotation` study — was built in the same sandbox as the ratio study:
Yahoo unreachable, the Kaggle mirror reachable. Every golden under
`golden/` for those five is from that mirror (2005-02-25 to 2017-11-10,
training to 2013-12-31, testing 2014–2017), and none of the five was
promoted; the write-ups in `docs/strategies/` record each verdict and why
the window is a poor judge of it. `make golden-study STUDY=<name>` on a
machine that reaches Yahoo regenerates the goldens and lets the study
promote. The research registry now lists all nine studies, and re-validation
runs a spec through the study its provenance names.

What the four-year mirror window did say, uniformly: the rules that hold
for days (`rsi2_reversion`) or trade on false breakouts (`donchian_breakout`)
paid for their turnover, and the slippage stress in each `robustness.json`
is the number to read first; the rules that turn monthly
(`risk_parity_trend`, `sector_rotation`) kept most of their Sharpe at 20 bp.

## A run id is a date, not a run

The journal is one file per run id and the run id is the trading date the
session is for (`sched.Session.RunID`, which on a holiday names the next
session — a run started on Labor Day is `2026-09-08`). `journal.Open`
appends and never truncates, so re-running a date puts **several invocations
in one file**: a run that failed at 10:42 for want of an armed spec and the
re-run that armed and completed at 12:53 are the same journal.

That splits what the run page can say in two. What *happened* under the date
— events, orders, fills, rejections, errors — is the total over every
invocation, and `journal.Summarize` counts it that way; the status is the
last invocation's, because that is the run's outcome. But what the run *is*,
and what the operator should do next, is the last invocation alone. The
"Nothing was armed" card read the last error anywhere in the file, so a
completed re-run still showed the dead attempt's failure under a Completed
badge. `apps/web/src/lib/journal.ts` draws the boundary now, and a file with
more than one invocation says so on the card.
