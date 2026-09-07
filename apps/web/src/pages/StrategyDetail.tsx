import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { describeSignal, EquityLine, ZGauge } from '../components/signal'
import { StrategyBadge } from '../components/status'
import { Badge, Banner, Card, Loading, Meter, SectionTitle, useLoader } from '../components/ui'
import { num, parts, pct, usd } from '../lib/format'
import type { Backtest } from '../types'

const PARAM_LABEL: Record<string, string> = {
  hedge_ratio: 'hedge ratio',
  lookback: 'lookback',
  entry_z: 'entry z',
  exit_z: 'exit z',
  max_hold_days: 'max hold',
}

export default function StrategyDetail() {
  const { name = '' } = useParams()
  const { data: s, error, offline } = useLoader(() => api.strategy(name), [name], 15000)
  const [bt, setBt] = useState<Backtest | null>(null)
  const [busy, setBusy] = useState(false)
  const [btError, setBtError] = useState<string | null>(null)

  const backtest = async () => {
    setBusy(true)
    setBtError(null)
    try {
      setBt(await api.backtest(name))
    } catch (e) {
      setBtError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const p = s?.provenance
  return (
    <Page title={name} back="/strategies">
      <Loading error={error} offline={offline} hasData={!!s} />
      {s && (
        <>
          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">{parts(s.strategy ?? s.path, s.universe?.join(' / '))}</div>
                <div className="sub">
                  {p ? parts(`generated ${p.generatedAt.slice(0, 10)}`, `research ${p.researchGitSha}`, `${p.ttlDays}-day TTL`) : s.path}
                </div>
              </div>
              <StrategyBadge status={s.status} />
            </div>
            {s.reason && (
              <div style={{ marginTop: 10 }}>
                <Banner tone={s.status === 'invalid' ? 'bad' : 'warn'}>{s.reason}</Banner>
              </div>
            )}
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
          </Card>

          {p && (
            <>
              <SectionTitle>Out of sample</SectionTitle>
              <Card>
                <div className="meters" style={{ marginTop: 0 }}>
                  <Stat label="Sharpe" value={num(p.oosSharpe, 2)} sub="floor 1.0" tone={p.oosSharpe >= 1 ? 'good' : 'bad'} />
                  <Stat label="Max drawdown" value={pct(p.oosMaxDrawdown)} sub="" tone="bad" />
                  <Stat label="Costs" value={`${num(p.slippageBps, 1)} bps`} sub={p.commissionUsd ? `+ ${usd(p.commissionUsd)}/order` : 'no commission'} />
                </div>
                <div className="list-divider" />
                <div className="kv">
                  <span className="k">trained on</span>
                  <span className="v">
                    {p.trainFrom} – {p.trainTo}
                  </span>
                </div>
                <div className="kv">
                  <span className="k">tested on</span>
                  <span className="v">
                    {p.testFrom} – {p.testTo}
                  </span>
                </div>
              </Card>
            </>
          )}

          {s.signal && (
            <>
              <SectionTitle>Signal now</SectionTitle>
              <Card>
                <div className="meter-label">
                  <span>Spread z-score · bars through {s.signal.lastBar}</span>
                  <b>{s.signal.zDefined ? num(s.signal.z, 2) : '—'}</b>
                </div>
                <ZGauge signal={s.signal} />
                <div className="sub" style={{ marginTop: 8 }}>
                  {describeSignal(s.signal, s.universe)}
                  {s.signal.stale && ' Bars are stale: the runner will refuse to trade until they are refreshed.'}
                </div>
              </Card>
            </>
          )}
          {s.signalError && (
            <Banner tone="warn">Signal unavailable: {s.signalError}</Banner>
          )}

          {s.params && (
            <>
              <SectionTitle>Parameters</SectionTitle>
              <Card>
                {Object.entries(s.params).map(([k, v]) => (
                  <div className="kv" key={k}>
                    <span className="k">{PARAM_LABEL[k] ?? k}</span>
                    <span className="v mono">{num(v)}</span>
                  </div>
                ))}
                {s.sizing && (
                  <>
                    <div className="kv">
                      <span className="k">gross leverage</span>
                      <span className="v">{num(s.sizing.grossLeverage, 3)} · half-Kelly, capped</span>
                    </div>
                    <div className="kv">
                      <span className="k">max per leg</span>
                      <span className="v">{usd(s.sizing.maxNotionalPerLegUsd)}</span>
                    </div>
                  </>
                )}
              </Card>
            </>
          )}

          {s.provenance && (
            <>
              <SectionTitle>Backtest here</SectionTitle>
              <Card>
                <p className="sub" style={{ margin: 0 }}>
                  Replays the spec over the bars on this machine from {s.provenance.testFrom}, with its own cost model. The
                  same engine the research side used; the numbers should agree with the spec's.
                </p>
                {btError && (
                  <div style={{ marginTop: 10 }}>
                    <Banner tone="bad">{btError}</Banner>
                  </div>
                )}
                {bt && <BacktestResult bt={bt} />}
                <div className="actions">
                  <button className="primary" onClick={backtest} disabled={busy}>
                    {busy ? 'Running…' : bt ? 'Run again' : 'Run backtest'}
                  </button>
                </div>
              </Card>
            </>
          )}
        </>
      )}
    </Page>
  )
}

function Stat({ label, value, sub, tone }: { label: string; value: string; sub: string; tone?: 'good' | 'bad' }) {
  return (
    <div>
      <div className="sub" style={{ fontSize: 11 }}>
        {label}
      </div>
      <div className="title" style={{ fontSize: 22, color: tone ? `var(--${tone})` : undefined }}>
        {value}
      </div>
      <div className="sub" style={{ fontSize: 11 }}>
        {sub}
      </div>
    </div>
  )
}

function BacktestResult({ bt }: { bt: Backtest }) {
  const agree = bt.sharpe != null && Math.abs(bt.sharpe - bt.specSharpe) <= 0.05
  return (
    <div style={{ marginTop: 12 }}>
      <EquityLine values={bt.equity.map((p) => p.equity)} />
      <div className="meters">
        <Stat label="Sharpe" value={bt.sharpe == null ? '—' : num(bt.sharpe, 2)} sub={`spec ${num(bt.specSharpe, 2)}`} tone={bt.sharpe != null && bt.sharpe >= 1 ? 'good' : 'bad'} />
        <Stat label="Max drawdown" value={pct(bt.maxDrawdown)} sub={`${bt.maxDrawdownDuration} bars under`} tone="bad" />
        <Stat label="Return" value={pct(bt.totalReturn, true)} sub={`${bt.bars} bars · ${bt.trades} trades`} />
      </div>
      <div className="row" style={{ marginTop: 10, gap: 6 }}>
        <span className="sub">
          {bt.from} – {bt.to}
        </span>
        <span className="grow" />
        {bt.sharpe != null && (agree ? <Badge tone="good">Matches the spec</Badge> : <Badge tone="warn">Differs from the spec</Badge>)}
      </div>
    </div>
  )
}
