import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { TabPage } from '../components/Layout'
import { HaltBanner, HaltSheet, ModeBadge, StrategyBadge } from '../components/status'
import { Badge, Card, Empty, Loading, Meter, SectionTitle, useLoader } from '../components/ui'
import { pct, usd } from '../lib/format'
import type { Book } from '../types'

/** The dashboard: the money, the guardrails, today's run, and the switch that
 *  stops everything — refreshed while it is open. */
export default function Overview() {
  const { data, error, loading, offline, reload } = useLoader(() => api.overview(), [], 5000)
  const [confirmHalt, setConfirmHalt] = useState(false)

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />
      {!data && loading && <div className="empty">Loading…</div>}

      {data && (
        <>
          {data.halted && <HaltBanner halt={data.halt} />}

          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">Paper book</div>
                <div className="sub">{data.book ? 'Dry-run' : 'Nothing traded yet'}</div>
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
              <BookSummary book={data.book} />
            ) : (
              <p className="sub" style={{ margin: '10px 0 0' }}>
                The paper book opens with the first run. Until then there is no equity to show, and no
                guardrail has anything to measure.
              </p>
            )}
          </Card>

          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">{data.lastRun ? `Run ${data.lastRun.runId}` : 'No run yet'}</div>
                <div className="sub">
                  {data.lastRun
                    ? `${data.lastRun.events} events`
                    : 'Runs happen once a day, ten minutes before the close.'}
                </div>
              </div>
              {data.lastRun && <Badge tone="neutral">{data.lastRun.status}</Badge>}
            </div>
          </Card>

          <SectionTitle>Strategies</SectionTitle>
          {data.strategies.length === 0 ? (
            <Empty
              message="No specs yet. Research writes them into specs/; the runtime refuses any without provenance."
              action={
                <Link to="/strategies">
                  <button className="secondary">About specs</button>
                </Link>
              }
            />
          ) : (
            <Card>
              {data.strategies.map((s, i) => (
                <div key={s.name}>
                  {i > 0 && <div className="list-divider inset" />}
                  <div className="row between" style={{ minHeight: 44 }}>
                    <div className="grow">
                      <div className="title">{s.name}</div>
                      {s.reason && <div className="sub">{s.reason}</div>}
                    </div>
                    <StrategyBadge status={s.status} />
                  </div>
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

function BookSummary({ book }: { book: Book }) {
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
        <Meter label="Drawdown" value={book.drawdownPct * 1000} display={pct(book.drawdownPct)} />
        <Meter label="Daily loss" value={(book.dailyLossPct / 0.03) * 100} display={pct(book.dailyLossPct)} />
        <Meter label="Orders" value={book.ordersToday * 10} display={`${book.ordersToday} of 10`} />
      </div>
    </>
  )
}
