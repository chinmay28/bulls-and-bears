import { api } from '../api'
import { Page } from '../components/Layout'
import { Badge, Card, Copyable, Empty, Loading, Meter, SectionTitle, useLoader } from '../components/ui'
import { num, parts, pct, time, usd } from '../lib/format'
import type { Rotation as RotationData, RotationEntry } from '../types'

/** The XLK/SATA rotation's one lot: where it is, what it has banked against
 *  its target, and what it is waiting on.
 *
 *  Read-only on purpose. The rotation is a separate process holding the
 *  broker connection and the data directory's lock; this screen reads the
 *  ledger and never takes that lock, because a status card that could block
 *  a trading cycle would be worse than no card. The way to stop it is the
 *  halt marker on Settings, which every cycle reads before it acts. */
export default function Rotation() {
  const { data, error, offline } = useLoader(() => api.rotation(), [], 15000)

  return (
    <Page title="Rotation" back="/book">
      <Loading error={error} offline={offline} hasData={!!data} />
      {data && <RotationView r={data} />}
    </Page>
  )
}

function modeTone(mode: string): 'good' | 'warn' | 'neutral' {
  if (mode === 'recovery') return 'warn'
  if (mode === 'held') return 'good'
  return 'neutral'
}

export function RotationView({ r }: { r: RotationData }) {
  const open = r.mode === 'held' || r.mode === 'recovery'
  return (
    <>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">
              {r.risk} / {r.park}
            </div>
            <div className="sub">
              {parts(
                r.running ? `running (pid ${r.holderPid})` : r.configured ? 'not running' : 'never run here',
                r.updatedAt && `book ${time(r.updatedAt)}`,
              )}
            </div>
          </div>
          <Badge tone={modeTone(r.mode)}>{r.mode}</Badge>
        </div>

        {!r.configured && (
          <div className="sub" style={{ marginTop: 10 }}>
            No lot has ever been opened in this data directory. The schedule and the rules below are what it would
            follow.
          </div>
        )}

        {open && <LotProgress r={r} />}
        {r.mode === 'flat' && r.configured && (
          <div className="sub" style={{ marginTop: 10 }}>
            Flat: every idle dollar belongs in {r.park} until the next entry.
          </div>
        )}
      </Card>

      {r.pending && (
        <Card>
          <SectionTitle>Waiting on an order</SectionTitle>
          <div className="row between">
            <div className="grow">
              <div className="title">{r.pending.kind.replace(/_/g, ' ')}</div>
              <div className="sub">
                {parts(
                  r.pending.symbol,
                  r.pending.strike > 0 && `strike ${usd(r.pending.strike)}`,
                  `placed ${time(r.pending.placedAt)}`,
                )}
              </div>
            </div>
          </div>
          <div className="sub" style={{ marginTop: 8 }}>
            Nothing else is placed while this is out — that is what keeps a repeated cycle from doubling the position.
          </div>
        </Card>
      )}

      {r.shortCall && (
        <Card>
          <SectionTitle>Covered call</SectionTitle>
          <div className="row between">
            <div className="grow">
              <div className="title">
                {num(r.shortCall.strike, 2)} call · {r.shortCall.expiration.slice(0, 10)}
              </div>
              <div className="sub">{usd(r.shortCall.credit)} a share received</div>
            </div>
            <Badge tone={r.shortCall.assignedPct >= r.recoveryTargetPct ? 'good' : 'bad'}>
              {pct(r.shortCall.assignedPct, true)} if assigned
            </Badge>
          </div>
          <div className="sub" style={{ marginTop: 8 }}>
            A call is only written when being called away at its strike would still return{' '}
            {pct(r.recoveryTargetPct, true)}.
          </div>
        </Card>
      )}

      {open && (
        <Card>
          <SectionTitle>The lot</SectionTitle>
          <Row label="Entry" value={`${usd(r.entryPrice)} on ${r.entryDate.slice(0, 10)}`} />
          <Row label="Basis" value={usd(r.basis)} />
          <Row label="Option cash" value={usd(r.optionPnl, true)} />
          <Row label="Dividends" value={usd(r.dividends, true)} />
          <Row label="Costs" value={usd(-r.costs, true)} />
          <Row label="Banked" value={usd(r.banked, true)} strong />
          <Row label={`Target (${pct(r.recoveryTargetPct, true)})`} value={usd(r.recoveryTargetUsd)} />
          <Row label={`${r.risk} needed`} value={usd(r.breakEvenPrice)} strong />
          <div className="sub" style={{ marginTop: 8 }}>
            {r.park} income is deliberately not counted toward this target.
          </div>
        </Card>
      )}

      <Card>
        <SectionTitle>Today</SectionTitle>
        <Row label="Orders" value={`${r.ordersToday}`} />
        {r.startOfDayEquity > 0 && <Row label="Equity at the open" value={usd(r.startOfDayEquity)} />}
        {r.highWaterEquity > 0 && <Row label="High water" value={usd(r.highWaterEquity)} />}
        {r.dailyLimitHits > 0 && <Row label="Daily-limit days in a row" value={`${r.dailyLimitHits}`} />}
        {r.marksDay && <div className="sub" style={{ marginTop: 8 }}>Counted for {r.marksDay}.</div>}
      </Card>

      <Card>
        <SectionTitle>Schedule</SectionTitle>
        {r.schedule.map((p) => (
          <div key={p.name} style={{ marginBottom: 12 }}>
            <div className="row between">
              <div className="grow">
                <div className="title">{p.name}</div>
                <div className="sub">{p.what}</div>
              </div>
              <div className="sub" style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                {p.pt} PT
                <br />
                {p.utc} UTC
              </div>
            </div>
            <Copyable text={p.cron} />
          </div>
        ))}
        <div className="sub">
          Cron is UTC and cannot follow daylight saving, so these times hold the gap between the phases and stay inside
          the session all year rather than holding the wall-clock times.
        </div>
      </Card>

      <Card>
        <SectionTitle>The rules it follows</SectionTitle>
        {r.rules.map((rule) => (
          <Row key={rule.label} label={rule.label} value={rule.value} />
        ))}
      </Card>

      <Card>
        <SectionTitle>Ledger</SectionTitle>
        {r.history.length === 0 ? (
          <Empty message="Nothing written yet." />
        ) : (
          r.history.map((e, i) => <HistoryRow key={`${e.at}-${i}`} e={e} />)
        )}
        <div className="sub" style={{ marginTop: 8 }}>
          Every state the book has been in, newest first. Nothing here is ever rewritten.
        </div>
      </Card>
    </>
  )
}

/** How far the lot is toward its target, counting only what is banked. The
 *  shares' own move is not in it: this screen has no quote, and a bar that
 *  guessed one would be worse than a bar that says what it knows. */
function LotProgress({ r }: { r: RotationData }) {
  const toward = r.recoveryTargetUsd > 0 ? Math.max(0, Math.min(100, (r.banked / r.recoveryTargetUsd) * 100)) : 0
  return (
    <div style={{ marginTop: 10 }}>
      <Meter
        label="Banked toward the target"
        value={toward}
        display={`${usd(r.banked, true)} of ${usd(r.recoveryTargetUsd)}`}
      />
      <div className="sub" style={{ marginTop: 6 }}>
        Premium, dividends and costs only. {r.risk} at {usd(r.breakEvenPrice)} closes the rest.
      </div>
    </div>
  )
}

function HistoryRow({ e }: { e: RotationEntry }) {
  return (
    <div className="row between" style={{ padding: '6px 0' }}>
      <div className="grow">
        <div style={{ fontWeight: 600 }}>{e.mode}</div>
        <div className="sub">
          {parts(
            e.entry > 0 && `entry ${usd(e.entry)}`,
            e.optionPnl !== 0 && `option ${usd(e.optionPnl, true)}`,
            e.strike > 0 && `short ${usd(e.strike)}`,
            e.pending && `pending ${e.pending.replace(/_/g, ' ')}`,
          )}
        </div>
      </div>
      <div className="sub" style={{ whiteSpace: 'nowrap' }}>{time(e.at)}</div>
    </div>
  )
}

function Row({ label, value, strong = false }: { label: string; value: string; strong?: boolean }) {
  return (
    <div className="row between" style={{ padding: '4px 0' }}>
      <div className="sub">{label}</div>
      <div style={{ fontWeight: strong ? 700 : 500 }}>{value}</div>
    </div>
  )
}
