import { api } from '../api'
import { TabPage } from '../components/Layout'
import { StrategyBadge } from '../components/status'
import { Card, Empty, Loading, useLoader } from '../components/ui'

/** The specs the runtime found, armed or refused. Specs are written by the
 *  research side and read here; the phone can see why one was refused but
 *  cannot edit one, because a spec without a backtest behind it is exactly
 *  what the runtime is built to refuse. */
export default function Strategies() {
  const { data, error, loading, offline } = useLoader(() => api.overview(), [], 10000)

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />
      {!data && loading && <div className="empty">Loading…</div>}

      {data?.strategies.length === 0 && (
        <Empty message="No specs found. A spec is a YAML file in specs/ with its backtest window, out-of-sample Sharpe and a TTL; the runtime refuses anything missing those, and anything with a Sharpe under 1.0." />
      )}

      {data?.strategies.map((s) => (
        <Card key={s.name}>
          <div className="row between">
            <div className="grow">
              <div className="title">{s.name}</div>
              {s.reason && <div className="sub">{s.reason}</div>}
            </div>
            <StrategyBadge status={s.status} />
          </div>
        </Card>
      ))}
    </TabPage>
  )
}
