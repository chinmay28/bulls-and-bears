import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api'
import { TabPage } from '../components/Layout'
import { HaltBanner, HaltSheet, ModeBadge, RunBadge, StrategyBadge } from '../components/status'
import { Badge, Banner, Card, Empty, Loading, Meter, SectionTitle, useLoader } from '../components/ui'
import { num, parts, pct, time, until, usd } from '../lib/format'
import type { BookSummary, Overview as OverviewData, Strategy } from '../types'

/** The dashboard: the money, the guardrails, today's run, and the switch that
 *  stops everything — refreshed while it is open. */
export default function Overview() {
  const { data, error, loading, offline, reload } = useLoader(() => api.overview(), [], 5000)
  const [confirmHalt, setConfirmHalt] = useState(false)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const navigate = useNavigate()

  const runNow = async () => {
    setBusy(true)
    setNotice(null)
    try {
      const { run } = await api.runNow()
      reload()
      navigate(`/runs/${run.runId}`)
    } catch (e) {
      setNotice(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />
      {!data && loading && <div className="empty">Loading…</div>}
      {notice && <Banner tone="bad">{notice}</Banner>}

      {data && (
        <>
          {data.halted && <HaltBanner halt={data.halt} />}
          <BookCard data={data} />
          <RunCard data={data} busy={busy} onRunNow={runNow} />

          <SectionTitle>Strategies</SectionTitle>
          {data.strategies.length === 0 ? (
            <Empty message="No specs found. Research writes them into specs/; the runtime refuses any without provenance." />
          ) : (
            <Card>
              {data.strategies.map((s, i) => (
                <div key={s.name}>
                  {i > 0 && <div className="list-divider inset" />}
                  <StrategyRow s={s} />
                </div>
              ))}
            </Card>
          )}

          {data.halted ? (
            <button className="primary block" style={{ marginTop: 8 }} onClick={() => api.resume().then(reload)}>
              Resume trading
            </button>
          ) : (
            <button className="danger block" style={{ marginTop: 8 }} onClick={() => setConfirmHalt(true)}>
              Halt trading
            </button>
          )}
        </>
      )}

      {confirmHalt && (
        <HaltSheet
          onClose={() => setConfirmHalt(false)}
          onHalted={() => {
            setConfirmHalt(false)
            reload()
          }}
        />
      )}
    </TabPage>
  )
}

function BookCard({ data }: { data: OverviewData }) {
  return (
    <Link className="card" to="/book">
      <div className="row between">
        <div className="grow">
          <div className="title">Paper book</div>
          <div className="sub">
            {data.book
              ? parts('Dry-run', `${data.book.positions} position${data.book.positions === 1 ? '' : 's'}`)
              : (data.bookError ?? 'Nothing traded yet')}
          </div>
        </div>
        <div className="row" style={{ gap: 6 }}>
          <ModeBadge mode={data.mode} />
          {data.halted ? (
            <Badge tone="bad" dot>
              Halted
            </Badge>
          ) : (
            <Badge tone="good" dot>
              Running
            </Badge>
          )}
        </div>
      </div>
      {data.book ? (
        <BookSummaryView book={data.book} limits={data.limits} />
      ) : (
        <p className="sub" style={{ margin: '10px 0 0' }}>
          The paper book opens with the first run. Until then there is no equity to show, and no guardrail has
          anything to measure.
        </p>
      )}
    </Link>
  )
}

function BookSummaryView({ book, limits }: { book: BookSummary; limits: OverviewData['limits'] }) {
  return (
    <>
      <div className="equity">{usd(book.equity)}</div>
      <div className="sub">
        <span style={{ color: book.dayChange >= 0 ? 'var(--good)' : 'var(--bad)', fontWeight: 600 }}>
          {usd(book.dayChange, true)} today
        </span>{' '}
        · {pct(book.sinceStartPct, true)} since start
      </div>
      <div className="meters">
        <Meter label="Drawdown" value={(book.drawdownPct / limits.drawdownKillSwitch) * 100} display={pct(book.drawdownPct)} />
        <Meter label="Daily loss" value={(book.dailyLossPct / limits.dailyLossLimit) * 100} display={pct(book.dailyLossPct)} />
        <Meter
          label="Orders"
          value={(book.ordersToday / limits.maxOrdersPerDay) * 100}
          display={`${book.ordersToday} of ${limits.maxOrdersPerDay}`}
        />
      </div>
      <div className="sub" style={{ marginTop: 12 }}>
        Halts at {pct(limits.drawdownKillSwitch)} drawdown or {pct(limits.dailyLossLimit)} in a day. High water{' '}
        {usd(book.highWater)}.
      </div>
    </>
  )
}

function RunCard({ data, busy, onRunNow }: { data: OverviewData; busy: boolean; onRunNow: () => void }) {
  const run = data.lastRun
  return (
    <Card>
      <div className="row between">
        <div className="grow">
          <div className="title">{run ? `Run ${run.runId}` : 'No run yet'}</div>
          <div className="sub">
            {run
              ? parts(time(run.startedAt), `${run.events} events`, `${run.fills} fill${run.fills === 1 ? '' : 's'}`, run.rejected > 0 && `${run.rejected} rejected`)
              : 'Runs happen once a day, ten minutes before the close.'}
          </div>
        </div>
        {run && <RunBadge status={run.status} />}
      </div>
      {run?.invariant && (
        <div style={{ marginTop: 10 }}>
          <Banner tone="bad">{run.invariant}</Banner>
        </div>
      )}
      <div className="list-divider" />
      <div className="kv">
        <span className="k">next run</span>
        <span className="v">
          {data.nextRun ? `${data.nextRun.runId} · ${until(data.nextRun.at)}${data.nextRun.earlyClose ? ' · early close' : ''}` : 'scheduler off'}
        </span>
      </div>
      <div className="actions" style={{ marginTop: 10 }}>
        {run && (
          <Link to={`/runs/${run.runId}`}>
            <button className="secondary block">Journal</button>
          </Link>
        )}
        <button className="secondary" onClick={onRunNow} disabled={busy || !data.nextRun}>
          {busy ? 'Running…' : 'Run now'}
        </button>
      </div>
    </Card>
  )
}

function StrategyRow({ s }: { s: Strategy }) {
  const navigate = useNavigate()
  const sig = s.signal
  const sub =
    s.status === 'armed'
      ? sig
        ? parts(s.strategy, sig.zDefined ? `z ${num(sig.z, 2)}` : 'z undefined', sig.state === 0 ? 'flat' : sig.state > 0 ? 'long spread' : 'short spread', sig.stale && 'bars stale')
        : (s.signalError ?? '')
      : s.reason
  return (
    <div className="row between" style={{ minHeight: 44, cursor: 'pointer' }} onClick={() => navigate(`/strategies/${s.name}`)}>
      <div className="grow">
        <div className="title">{s.name}</div>
        <div className="sub truncate">{sub}</div>
      </div>
      <StrategyBadge status={s.status} />
    </div>
  )
}
