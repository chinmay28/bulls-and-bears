import { Link } from 'react-router-dom'
import { api } from '../api'
import { TabPage } from '../components/Layout'
import { StrategyBadge } from '../components/status'
import { Empty, Loading, Meter, useLoader } from '../components/ui'
import { num, parts } from '../lib/format'
import type { Strategy } from '../types'

/** The specs the runtime found, armed or refused. Specs are written by the
 *  research side and read here; the phone can see why one was refused but
 *  cannot edit one, because a spec without a backtest behind it is exactly
 *  what the runtime is built to refuse. */
export default function Strategies() {
  const { data, error, loading, offline } = useLoader(() => api.strategies(), [], 10000)

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />
      {!data && loading && <div className="empty">Loading…</div>}

      {data?.length === 0 && (
        <Empty message="No specs found. A spec is a YAML file in specs/ with its backtest window, out-of-sample Sharpe and a TTL; the runtime refuses anything missing those, and anything with a Sharpe under 1.0." />
      )}

      {data?.map((s) => <StrategyCard key={s.name} s={s} />)}
    </TabPage>
  )
}

function StrategyCard({ s }: { s: Strategy }) {
  const p = s.provenance
  return (
    <Link className="card" to={`/strategies/${s.name}`}>
      <div className="row between">
        <div className="grow">
          <div className="title">{s.name}</div>
          <div className="sub">
            {s.status === 'armed'
              ? parts(s.strategy, s.universe?.join(' / '), p && `Sharpe ${num(p.oosSharpe, 2)}`)
              : parts(s.strategy ?? s.path, s.reason)}
          </div>
        </div>
        <StrategyBadge status={s.status} />
      </div>
      {p && (
        <div style={{ marginTop: 12 }}>
          <Meter
            label="Spec fresh for"
            value={(s.freshDays / p.ttlDays) * 100}
            display={s.freshDays > 0 ? `${Math.floor(s.freshDays)} of ${p.ttlDays} days` : 'expired'}
            tone="progress"
          />
        </div>
      )}
      {s.signal && (
        <div className="sub" style={{ marginTop: 10 }}>
          {parts(
            s.signal.zDefined ? `z ${num(s.signal.z, 2)}` : 'z undefined',
            s.signal.state === 0 ? 'flat' : s.signal.state > 0 ? 'long the spread' : 'short the spread',
            `bars through ${s.signal.lastBar}`,
            s.signal.stale && 'stale',
          )}
        </div>
      )}
    </Link>
  )
}
