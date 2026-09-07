import { Link } from 'react-router-dom'
import { api } from '../api'
import { TabPage } from '../components/Layout'
import { HaltBanner, ModeBadge, RunBadge } from '../components/status'
import { Badge, Card, Loading, SectionTitle, useLoader } from '../components/ui'
import { parts, time } from '../lib/format'

export default function Settings() {
  // Refreshed on a timer so a halt from elsewhere surfaces without a reload.
  const { data: self, error, offline, reload } = useLoader(() => api.self(), [], 5000)
  const { data: bars } = useLoader(() => api.bars(), [], 30000)
  const { data: runs } = useLoader(() => api.runs(), [], 15000)

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!self} />

      <SectionTitle>This machine</SectionTitle>
      {self && (
        <Card>
          <div className="row between">
            <div className="grow">
              <div className="title">Bulls and Bears</div>
              <div className="sub">
                version <span className="mono">{self.version}</span> · builds from <span className="mono">{self.ref}</span>
              </div>
            </div>
            <ModeBadge mode={self.mode} />
          </div>
          <p className="sub" style={{ margin: '10px 0 0' }}>
            Upgrading is the same command that installed it, run again on the machine.
          </p>
        </Card>
      )}

      <SectionTitle>Trading</SectionTitle>
      {self && (
        <Card>
          {self.halted ? (
            <>
              <HaltBanner halt={self.halt} />
              <button className="primary block" onClick={() => api.resume().then(reload)}>
                Resume trading
              </button>
            </>
          ) : (
            <div className="row between">
              <div className="grow">
                <div className="title">Not halted</div>
                <div className="sub">The halt switch lives on the Overview, one tap from anywhere.</div>
              </div>
              <Badge tone="good" dot>
                Running
              </Badge>
            </div>
          )}
        </Card>
      )}

      <SectionTitle>Bars</SectionTitle>
      <Card>
        {!bars || bars.length === 0 ? (
          <p className="sub" style={{ margin: 0 }}>
            No symbols to show: no spec names any. Bars are Parquet files the research side writes, one per symbol.
          </p>
        ) : (
          <>
            {bars.map((b) => (
              <div className="kv" key={b.symbol}>
                <span className="k">{b.symbol}</span>
                <span className="v" style={{ color: b.stale ? 'var(--warn)' : undefined }}>
                  {b.error ? b.error : parts(b.source, `through ${b.last}`, b.stale && 'stale')}
                </span>
              </div>
            ))}
            <p className="sub" style={{ margin: '8px 0 0' }}>
              The runner refuses to trade on bars more than three trading days old. Refresh them with{' '}
              <span className="mono">research/scripts/gld_gdx.py</span> or the fetcher.
            </p>
          </>
        )}
      </Card>

      <SectionTitle>Runs</SectionTitle>
      <Card>
        {!runs || runs.length === 0 ? (
          <p className="sub" style={{ margin: 0 }}>
            No runs yet. One happens every trading day, ten minutes before the close.
          </p>
        ) : (
          runs.slice(0, 10).map((r, i) => (
            <div key={r.runId}>
              {i > 0 && <div className="list-divider inset" />}
              <Link to={`/runs/${r.runId}`} className="row between" style={{ minHeight: 44, textDecoration: 'none', color: 'inherit' }}>
                <div className="grow">
                  <div className="title">{r.runId}</div>
                  <div className="sub">{parts(time(r.startedAt), `${r.orders} orders`, r.rejected > 0 && `${r.rejected} rejected`)}</div>
                </div>
                <RunBadge status={r.status} />
              </Link>
            </div>
          ))
        )}
      </Card>

      <SectionTitle>Robinhood</SectionTitle>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">Not connected</div>
            <div className="sub">
              Quotes come from the newest bar on disk until the Robinhood Trading MCP is connected. Signing in needs
              a desktop browser once; the token then lives in the data directory, never in the database.
            </div>
          </div>
          <Badge tone="neutral">Phase 3</Badge>
        </div>
      </Card>

      <SectionTitle>About</SectionTitle>
      <Card>
        <p className="sub" style={{ margin: 0 }}>
          Bulls and Bears will hold a token that can place real trades in a Robinhood Agentic account. Keep it on
          your LAN or Tailscale network. Everything it does is written to an append-only journal before it is
          done. Nothing here is investment advice.
        </p>
        {self && (
          <div className="row between sub" style={{ marginTop: 10 }}>
            <span>Version</span>
            <span className="mono">{self.version}</span>
          </div>
        )}
      </Card>
    </TabPage>
  )
}
