import { api } from '../api'
import { TabPage } from '../components/Layout'
import { HaltBanner, ModeBadge } from '../components/status'
import { Badge, Card, Loading, SectionTitle, useLoader } from '../components/ui'

export default function Settings() {
  // Refreshed on a timer so a halt from elsewhere surfaces without a reload.
  const { data: self, error, offline, reload } = useLoader(() => api.self(), [], 5000)

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
                version <span className="mono">{self.version}</span> · builds from{' '}
                <span className="mono">{self.ref}</span>
              </div>
            </div>
            <ModeBadge mode={self.mode} />
          </div>
          <p className="sub" style={{ margin: '10px 0 0' }}>
            Upgrading is the same command that installed it, run again on the machine. Updating from
            here arrives with the scheduler.
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

      <SectionTitle>Robinhood</SectionTitle>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">Not connected</div>
            <div className="sub">
              Quotes and orders go through the Robinhood Trading MCP. Signing in needs a desktop browser
              once; the token then lives in the data directory, never in the database.
            </div>
          </div>
          <Badge tone="neutral">Phase 3</Badge>
        </div>
      </Card>

      <SectionTitle>About</SectionTitle>
      <Card>
        <p className="sub" style={{ margin: 0 }}>
          Bulls and Bears will hold a token that can place real trades in a Robinhood Agentic account.
          Keep it on your LAN or Tailscale network. Everything it does is written to an append-only
          journal before it is done. Nothing here is investment advice.
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
