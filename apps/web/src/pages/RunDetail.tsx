import { Link, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { RunBadge } from '../components/status'
import { Banner, Card, Loading, useLoader } from '../components/ui'
import { clock, parts, time } from '../lib/format'
import { invocationCount, latestInvocation } from '../lib/journal'
import type { JournalEvent } from '../types'

/** One run's journal, line by line: the quote as it was seen, the targets,
 *  every risk decision, every order and fill. Every order here follows an
 *  allowed decision with the same intent id, and the badge says so. */
export default function RunDetail() {
  const { id = '' } = useParams()
  const { data, error, offline } = useLoader(() => api.run(id), [id], 10000)
  const run = data?.run
  // The counts and the strip cover the whole file; the next step covers the
  // last invocation, which is the only one still true (lib/journal).
  const runs = data ? invocationCount(data.events) : 0

  return (
    <Page
      title={`Run ${id}`}
      back="/"
      action={
        <a className="ghost" href={`/api/runs/${encodeURIComponent(id)}/jsonl`} download style={{ fontWeight: 600 }}>
          JSONL
        </a>
      }
    >
      <Loading error={error} offline={offline} hasData={!!data} />
      {data && run && (
        <>
          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">
                  {run.mode === 'live' ? 'Live' : 'Dry-run'} · {time(run.startedAt)}
                </div>
                <div className="sub">
                  {parts(`${run.events} events`, `${run.orders} orders`, `${run.fills} fills`, run.rejected > 0 && `${run.rejected} rejected`, run.errors > 0 && `${run.errors} errors`)}
                </div>
              </div>
              <RunBadge status={run.status} />
            </div>
            <div className="seg-strip" style={{ marginTop: 12 }}>
              {data.events.map((e, i) => (
                <span key={i} className={tone(e)} />
              ))}
            </div>
            <div className="sub" style={{ marginTop: 6 }}>
              {run.invariant ? run.invariant : 'Every order follows an allowed risk decision with the same intent id.'}
            </div>
            {runs > 1 && (
              <div className="sub" style={{ marginTop: 6 }}>
                Run {runs} times under this date: the counts above are the totals, the badge is the last run.
              </div>
            )}
            {data.truncated && (
              <div style={{ marginTop: 10 }}>
                <Banner tone="warn">The journal ends mid-line: the process stopped while writing.</Banner>
              </div>
            )}
          </Card>

          <NextStep events={latestInvocation(data.events)} />

          <Card>
            {data.events.map((e, i) => (
              <div key={i}>
                {i > 0 && <div className="list-divider inset" />}
                <Event e={e} />
              </div>
            ))}
          </Card>
        </>
      )}
    </Page>
  )
}

/** NextStep turns the two failures an operator can actually fix into the tap
 *  that fixes them. A run that ends "no armed strategy" or "bars: ... stale"
 *  is not a bug to report, it is a missing spec or missing bars — and both
 *  now have a screen. Anything else is left to the journal below.
 *
 *  It must be given one invocation's events, not the whole file: a re-run that
 *  armed and completed leaves the failed attempt's error behind it in the same
 *  journal, and pointing the operator at Strategies over a failure they have
 *  already fixed is worse than saying nothing. */
export function NextStep({ events }: { events: JournalEvent[] }) {
  const failure = [...events].reverse().find((e) => e.kind === 'error')
  const text = failure ? String((failure.data as { error?: string } | undefined)?.error ?? '') : ''
  if (!text) return null

  if (text.includes('no armed strategy') || text.includes('no strategy specs')) {
    return (
      <Card>
        <div className="title">Nothing was armed</div>
        <p className="sub" style={{ margin: '6px 0 12px' }}>
          {text.includes('no strategy specs')
            ? 'There are no spec files at all. A spec is what research writes when a backtest clears the 1.0 out-of-sample Sharpe floor; import one and the next run has something to trade.'
            : 'Every spec on disk was refused. The reasons are in the journal below and on each strategy.'}
        </p>
        <Link className="primary block" to="/strategies" style={{ textAlign: 'center', textDecoration: 'none' }}>
          Go to Strategies
        </Link>
      </Card>
    )
  }

  if (text.startsWith('bars:')) {
    return (
      <Card>
        <div className="title">The bars were not good enough to trade on</div>
        <p className="sub" style={{ margin: '6px 0 12px' }}>
          {text} The runner refuses stale or missing history rather than guessing at it.
        </p>
        <Link className="primary block" to="/settings" style={{ textAlign: 'center', textDecoration: 'none' }}>
          Refresh bars in Settings
        </Link>
      </Card>
    )
  }

  return null
}

function tone(e: JournalEvent): string {
  if (e.kind === 'error' || e.kind === 'halt' || (e.kind === 'risk_decision' && !e.allowed) || refused(e)) return 'b'
  if (e.kind === 'fill' || (e.kind === 'risk_decision' && e.allowed)) return 'g'
  return ''
}

/** A spec the run would not arm: the reason a run with no orders had none. */
function refused(e: JournalEvent): boolean {
  return e.kind === 'strategy' && (e.data as { armed?: boolean } | undefined)?.armed === false
}

function Event({ e }: { e: JournalEvent }) {
  const color =
    e.kind === 'error' || e.kind === 'halt' || (e.kind === 'risk_decision' && !e.allowed) || refused(e)
      ? 'var(--bad)'
      : e.kind === 'fill' || (e.kind === 'risk_decision' && e.allowed)
        ? 'var(--good)'
        : undefined
  return (
    <div className="ev">
      <span className="t">{clock(e.ts)}</span>
      <div className="grow" style={{ minWidth: 0 }}>
        <div className="kind" style={{ color }}>
          {e.kind}
          {e.intent_id && <span className="sub"> · {e.intent_id}</span>}
        </div>
        <div className="d">{describe(e)}</div>
      </div>
    </div>
  )
}

/** One line about an event, from its data. */
function describe(e: JournalEvent): string {
  const d = (e.data ?? {}) as Record<string, unknown>
  const n = (v: unknown, places = 2) => (typeof v === 'number' ? v.toFixed(places) : String(v ?? ''))
  switch (e.kind) {
    case 'run':
      return parts(String(d.status ?? ''), d.reason ? String(d.reason) : '', d.equity != null ? `equity ${n(d.equity)}` : '')
    case 'quote':
      return `${d.Symbol} ${n(d.Bid)} / ${n(d.Ask)} · last ${n(d.Last)}`
    case 'bars':
      return `${d.symbol} · ${d.bars} bars through ${d.last} · ${d.source}`
    case 'strategy':
      return d.armed ? `${d.name} · armed` : `${d.name} · not armed · ${d.reason}`
    case 'targets': {
      const w = (d.weights ?? {}) as Record<string, number>
      return `${d.strategy} · ${Object.entries(w)
        .map(([s, v]) => `${s} ${v >= 0 ? '+' : ''}${v.toFixed(3)}`)
        .join(' · ')}`
    }
    case 'risk_decision': {
      const o = (d.order ?? {}) as Record<string, unknown>
      return parts(e.allowed ? 'allowed' : 'rejected', `${side(o.Side)} ${o.Symbol} ${n(d.qty, 3)}`, String(d.reason ?? ''))
    }
    case 'order_submitted': {
      const o = (d.order ?? {}) as Record<string, unknown>
      return `${side(o.Side)} ${o.Symbol} ${n(o.Qty, 3)}${o.Limit ? ` limit ${n(o.Limit)}` : ' market'}`
    }
    case 'fill':
      return `${d.OrderID} · ${Number(d.Qty) >= 0 ? 'bought' : 'sold'} ${Math.abs(Number(d.Qty)).toFixed(3)} ${d.Symbol} @ ${n(d.Price, 4)}`
    case 'error':
      return String(d.error ?? '')
    case 'halt':
      return parts(String(d.reason ?? ''), d.by ? `by ${d.by}` : '')
    default:
      return JSON.stringify(d)
  }
}

function side(v: unknown): string {
  return v === 1 ? 'BUY' : v === 2 ? 'SELL' : String(v ?? '')
}
