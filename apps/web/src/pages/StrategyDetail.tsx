import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ApiError, api } from '../api'
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
  const { data: s, error, offline, reload } = useLoader(() => api.strategy(name), [name], 15000)
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

          {s.provenance && (
            <>
              <SectionTitle>Stage</SectionTitle>
              <StageCard name={name} stage={s.stage} onChanged={reload} />
            </>
          )}

          <SectionTitle>Remove</SectionTitle>
          <RemoveCard name={name} />
        </>
      )}
    </Page>
  )
}

/** StageCard is the operator's decision, separate from research's: a spec
 *  earns its place on paper by clearing the floor; only a promotion here
 *  lets it touch real money once a live broker is connected. */
function StageCard({ name, stage, onChanged }: { name: string; stage: 'paper' | 'live'; onChanged: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = async (next: 'paper' | 'live') => {
    setBusy(true)
    setError('')
    try {
      await api.setStage(name, next)
      onChanged()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Card>
      <div className="row between">
        <div className="grow">
          <div className="title">{stage === 'live' ? 'Promoted to live' : 'Paper'}</div>
          <div className="sub">
            {stage === 'live'
              ? 'When a live broker is connected, this spec trades real money. Until then it trades on paper like every other.'
              : 'Trades on paper. When a live broker is connected, only promoted specs trade real money; the plan asks for a month of paper agreeing with the backtest first.'}
          </div>
        </div>
        <Badge tone={stage === 'live' ? 'warn' : 'accent'}>{stage === 'live' ? 'Live' : 'Paper'}</Badge>
      </div>
      {error && (
        <div style={{ marginTop: 10 }}>
          <Banner tone="bad">{error}</Banner>
        </div>
      )}
      <div className="actions">
        {stage === 'live' ? (
          <button className="secondary" disabled={busy} onClick={() => set('paper')}>
            Back to paper
          </button>
        ) : (
          <button className="secondary" disabled={busy} onClick={() => set('live')}>
            Promote to live
          </button>
        )}
      </div>
    </Card>
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

/** RemoveCard deletes the spec file. A spec imported by mistake, or one long
 *  past its TTL that only clutters the list, should not need a terminal to
 *  get rid of. The bars stay: they cost a fetch and belong to no one spec. */
function RemoveCard({ name }: { name: string }) {
  const navigate = useNavigate()
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const remove = async () => {
    setBusy(true)
    setError('')
    try {
      await api.deleteStrategy(name)
      navigate('/strategies')
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <Card>
      <p className="sub" style={{ margin: '0 0 12px' }}>
        Deletes <span className="mono">{name}</span> from the specs directory. The bars it used stay on disk, and
        nothing that has already traded is touched — the journal keeps every run this spec was part of.
      </p>
      {error && (
        <div style={{ marginBottom: 12 }}>
          <Banner tone="bad">{error}</Banner>
        </div>
      )}
      {confirming ? (
        <div className="actions">
          <button className="secondary" onClick={() => setConfirming(false)} disabled={busy}>
            Keep it
          </button>
          <button className="danger" onClick={remove} disabled={busy}>
            {busy ? 'Removing…' : 'Remove the spec'}
          </button>
        </div>
      ) : (
        <button className="secondary block" onClick={() => setConfirming(true)}>
          Remove this spec
        </button>
      )}
    </Card>
  )
}
